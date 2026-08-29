// Package schedule 实现会话内定时提醒（复刻 deepseek-harness schedule）。
//
// 简化版：支持一次延迟 / 绝对时间 / 固定间隔提醒，到期后注入 user 消息。
// 无外部通知渠道，仅在会话活跃期间投递。
package schedule

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Reminder 一条定时提醒记录。
type Reminder struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"` // "after" | "at" | "every"
	Prompt      string    `json:"prompt"`
	ScheduledAt time.Time `json:"scheduledAt"`
	EverySecs   int       `json:"everySecs,omitempty"`
	AfterSecs   int       `json:"afterSecs,omitempty"`
	Delivered   bool      `json:"delivered"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Registry 定时提醒注册表。
type Registry struct {
	mu       sync.RWMutex
	reminders map[string]*Reminder
	nextSeq  atomic.Int64
	done     chan struct{} // 关闭时停止调度 goroutine
	deliver  func(id, prompt string) // 投递回调（由 main.go 设置）
}

// NewRegistry 创建提醒注册表。deliver 为 nil 时提醒仅记录不投递。
func NewRegistry(deliver func(id, prompt string)) *Registry {
	r := &Registry{
		reminders: make(map[string]*Reminder),
		done:      make(chan struct{}),
		deliver:   deliver,
	}
	go r.run()
	return r
}

// Create 创建一条提醒。返回提醒 ID 或错误。
// kind: "after"<n>秒 / "at"<RFC3339> / "every"<n>秒（最小 300 秒 = 5 分钟）。
func (r *Registry) Create(kind, prompt string, afterSecs int, atTime time.Time, everySecs int) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("schedule: prompt required")
	}
	var scheduledAt time.Time
	switch kind {
	case "after":
		if afterSecs <= 0 {
			return "", fmt.Errorf("schedule: after_sec requires positive integer")
		}
		scheduledAt = time.Now().Add(time.Duration(afterSecs) * time.Second)
	case "at":
		if atTime.IsZero() || atTime.Before(time.Now()) {
			return "", fmt.Errorf("schedule: at must be a future time")
		}
		scheduledAt = atTime
	case "every":
		if everySecs < 300 {
			return "", fmt.Errorf("schedule: every interval must be >= 300 seconds (5 minutes)")
		}
		// 对齐到最近的未来整点
		now := time.Now()
		scheduledAt = now.Add(time.Duration(((everySecs - int(now.Unix())%everySecs)%everySecs+everySecs)%everySecs) * time.Second)
	default:
		return "", fmt.Errorf("schedule: unknown kind %q (expected after/at/every)", kind)
	}
	r.mu.Lock()
	seq := r.nextSeq.Add(1)
	id := fmt.Sprintf("sched-%d", seq)
	rec := &Reminder{
		ID:          id,
		Kind:        kind,
		Prompt:      strings.TrimSpace(prompt),
		ScheduledAt: scheduledAt,
		AfterSecs:   afterSecs,
		EverySecs:   everySecs,
		CreatedAt:   time.Now(),
	}
	r.reminders[id] = rec
	r.mu.Unlock()
	return id, nil
}

// List 列出所有未投递的提醒。
func (r *Registry) List() []Reminder {
	r.mu.RLock()
	defer r.mu.RLock()
	var out []Reminder
	for _, rec := range r.reminders {
		if !rec.Delivered {
			out = append(out, *rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScheduledAt.Before(out[j].ScheduledAt) })
	return out
}

// Delete 删除一条提醒。
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.reminders[id]
	if !ok {
		return fmt.Errorf("schedule: reminder %q not found", id)
	}
	delete(r.reminders, id)
	return nil
}

// run 后台调度循环：检测到期提醒并投递。
func (r *Registry) run() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.checkDue()
		case <-r.done:
			return
		}
	}
}

func (r *Registry) checkDue() {
	r.mu.Lock()
	now := time.Now()
	var due []*Reminder
	for _, rec := range r.reminders {
		if rec.Delivered {
			continue
		}
		if now.Before(rec.ScheduledAt) {
			continue
		}
		due = append(due, rec)
	}
	r.mu.Unlock()
	for _, rec := range due {
		r.deliver(rec.ID, rec.Prompt)
		r.mu.Lock()
		rec.Delivered = true
		// 固定间隔提醒：调度下一次
		if rec.Kind == "every" && rec.EverySecs > 0 {
			rec.ScheduledAt = rec.ScheduledAt.Add(time.Duration(rec.EverySecs) * time.Second)
			if rec.ScheduledAt.Before(now) {
				rec.ScheduledAt = now.Add(time.Duration(rec.EverySecs) * time.Second)
			}
		}
		r.mu.Unlock()
	}
}

// Stop 停止调度循环（不删除待投递提醒）。
func (r *Registry) Stop() {
	close(r.done)
}

// RenderList 将提醒列表渲染为文本。
func RenderList(rs []Reminder) string {
	if len(rs) == 0 {
		return "（无待投递提醒）"
	}
	var sb strings.Builder
	for _, r := range rs {
		when := r.ScheduledAt.Format("2006-01-02 15:04:05")
		fmt.Fprintf(&sb, "  %-12s %-8s %s  %s\n", r.ID, r.Kind, when, truncate(r.Prompt, 50))
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
