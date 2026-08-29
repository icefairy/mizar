// Package jobs 实现后台任务注册表（复刻 deepseek-harness 的 ctx.jobs）。
//
// 定位：进程内、session-scoped 的最简后台任务调度器。
//   - job 通过 kind（如 "bash"）+ label 命名，id 由 registry 生成为 "<kind>-N"
//   - 支持 start / list / output / kill
//   - drainDone() 返回已完成但未领取的通知列表（主循环消费后自动清除）
//   - 无外部依赖（不 import agent/plugins），方便跨包引用
package jobs

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Status job 状态。
type Status string

const (
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusErrored Status = "errored"
	StatusKilled  Status = "killed"
)

// Job 一条后台任务的快照（只读）。
type Job struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Label  string  `json:"label,omitempty"`
	Status Status  `json:"status"`
	Output string  `json:"output,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// Registry 后台任务注册表（线程安全）。
type Registry struct {
	mu       sync.Mutex
	jobs     map[string]*jobRecord
	nextSeq  atomic.Int64
	doneChs  map[string]chan string // output streamers：被 job_output 读取时写入
}

type jobRecord struct {
	Job
	doneNotify chan struct{} // close 后表示任务已终结
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{
		jobs:    make(map[string]*jobRecord),
		doneChs: make(map[string]chan string),
	}
}

// Start 启动一个后台任务。run 返回 (output, error)。
// id 自动生成形如 "<kind>-N" 的 predictable 标识（方便模型记住并后续查询）。
func (r *Registry) Start(kind, label string, run func() (string, error)) string {
	r.mu.Lock()
	seq := r.nextSeq.Add(1)
	id := fmt.Sprintf("%s-%d", kind, seq)
	rec := &jobRecord{
		Job:      Job{ID: id, Kind: kind, Label: label, Status: StatusRunning},
		doneNotify: make(chan struct{}),
	}
	r.jobs[id] = rec
	// 在独立 goroutine 执行任务
	go func() {
		out, err := run()
		r.mu.Lock()
		defer r.mu.Unlock()
		rec.Output = out
		if err != nil {
			rec.Error = err.Error()
			rec.Status = StatusErrored
		} else {
			rec.Status = StatusDone
		}
		close(rec.doneNotify)
		// 清理 output streamers
		for _, ch := range r.doneChs {
			close(ch)
		}
		delete(r.doneChs, id)
	}()
	r.mu.Unlock()
	return id
}

// List 列出当前所有任务（含历史）。
func (r *Registry) List() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, 0, len(r.jobs))
	for _, rec := range r.jobs {
		out = append(out, rec.Job)
	}
	return out
}

// Output 读取指定任务自上次读取以来的增量输出（流式读取语义；对齐 dsh job_output wait=true 的流式版）。
// 若任务未运行或已终结，返回累计全部输出。
// 注意：本实现不持流式偏移（简化），每次调用返回从上次调用后新增的部分——这需要 caller 维护 lastOffset。
// 为保持简单：直接返回累计完整输出（包含 error）。
func (r *Registry) Output(id string) (string, Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.jobs[id]
	if !ok {
		return "", "", false
	}
	return rec.Output, rec.Status, true
}

// Kill 终止指定任务（通过 error 通知——目前 run 应定期检查 context；
// 这里仅做标记，由 run 函数自行响应）。
// 注：由于 run 是闭包且不可控，这里采用"打标记 + 关闭 doneNotify"策略，
// 让已阻塞在 doneNotify 上的消费者（如有）能退出。对正在运行的 goroutine 本身无法强制 kill。
func (r *Registry) Kill(id, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.jobs[id]
	if !ok {
		return fmt.Errorf("unknown job: %s", id)
	}
	if rec.Status == StatusRunning {
		rec.Status = StatusKilled
		rec.Error = reason
	}
	// 若 doneNotify 未关闭（任务仍在运行），close 它让阻塞者退出；
	// 但本实现不阻塞在 doneNotify 上，仅用于 future 扩展。
	select {
	case <-rec.doneNotify:
	default:
		// 仍在运行：close 让它不再阻塞
		close(rec.doneNotify)
	}
	return nil
}

// DrainDone 返回已终结但未领取的任务 id 列表（并清除这些任务的通知）。
// 主循环每步调用一次以注入完成通知。
func (r *Registry) DrainDone() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, rec := range r.jobs {
		// 判断是否已终结：看 doneNotify 是否 closed（收不到 blocking 即已 close）
		select {
		case <-rec.doneNotify:
			// 已终结：收集 id
			ids = append(ids, id)
			delete(r.jobs, id)
		default:
		}
	}
	return ids
}

// Count 当前 running + 未 drain 的任务数（不含已 drain 的 done/errored）。
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.jobs {
		switch rec.Status {
		case StatusRunning, StatusDone, StatusErrored, StatusKilled:
			n++
		}
	}
	return n
}
