package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Web UI 会话存储：多会话 + 消息历史 + 参数设置，持久化到 JSON 文件。
// 结构: {"conversations":[...],"settings":{...}}
// 位置: ~/.mizar/webui.json（随 HOME 走，测试时可用临时 HOME 隔离）
// ============================================================================

// convMessage 一条会话消息。
type convMessage struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
	// Ts 消息时间（RFC3339）。旧数据为空，加载时继承 createdAt 兼容。
	Ts string `json:"ts,omitempty"`
}

// conversation 一个独立会话。
type conversation struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Mode      string        `json:"mode"` // chat | agent（上次使用的模式）
	Messages  []convMessage `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	// TitleGenAt 上次自动生成标题时的消息条数（0 = 从未生成）。
	// 用于避免每轮都调一次小模型：新增消息达到阈值才重新提炼。
	TitleGenAt int `json:"title_gen_at,omitempty"`
	// TitleManual 用户手动改过标题（PATCH）；为 true 时不再自动覆盖。
	TitleManual bool `json:"title_manual,omitempty"`
}

// webSettings 对话参数设置（进程级，全局生效）。
type webSettings struct {
	Model         string   `json:"model,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	MaxTokens     int      `json:"max_tokens,omitempty"`
	System        string   `json:"system,omitempty"`
	Thinking      string   `json:"thinking,omitempty"`        // auto/off/low/medium/high
	MaxHistory    int      `json:"max_history,omitempty"`     // 普通对话模式最多发送的历史消息条数（默认 50；agent 模式不生效）
	ShowCacheMiss bool     `json:"show_cache_miss,omitempty"` // 是否显示缓存命中率下跌提示（默认关闭）
	// TitleModel 自动会话标题提炼用的模型（空 = 用当前主模型）。
	// 建议填一个免费/小尺寸模型，避免额外费用与延迟。
	TitleModel string `json:"title_model,omitempty"`
}

// webStore 会话 + 设置存储（带文件持久化）。
type webStore struct {
	mu    sync.Mutex
	path  string
	Convs []*conversation `json:"conversations"`
	Set   *webSettings    `json:"settings,omitempty"`
	// stamp 最近一次「读到/写下的」磁盘文件指纹。用于发现外部修改：
	// 若磁盘指纹与 stamp 不一致，说明文件被别的进程/编辑器改过，下次访问前自动重载，
	// 避免用内存里的旧数据把外部修改覆盖回去。
	stamp fileStamp
	// dirty 自上次成功落盘以来是否有本进程的内存改动。无改动时不回写。
	dirty bool
}

// fileStamp 文件指纹（mtime 纳秒 + size），用于廉价检测外部修改。
type fileStamp struct {
	modNano int64
	size    int64
}

func statStamp(path string) (fileStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{modNano: fi.ModTime().UnixNano(), size: fi.Size()}, true
}

func newWebStore(path string) *webStore {
	s := &webStore{path: path, Set: &webSettings{}}
	s.load()
	return s
}

// ensureFreshLocked 若磁盘文件指纹变化（被外部修改），则重新加载，
// 保证外部编辑（手工删会话、其他进程写入）不会被内存旧数据覆盖。
// 调用方需持锁。
func (s *webStore) ensureFreshLocked() {
	if s.dirty {
		return // 本进程有未落盘改动，以内存为准
	}
	st, ok := statStamp(s.path)
	if ok && st == s.stamp {
		return
	}
	s.load()
}

func (s *webStore) load() {
	s.stamp, _ = statStamp(s.path)
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var tmp struct {
		Convs []*conversation `json:"conversations"`
		Set   *webSettings    `json:"settings"`
	}
	if json.Unmarshal(data, &tmp) != nil {
		return
	}
	s.Convs = tmp.Convs
	if tmp.Set != nil {
		s.Set = tmp.Set
	}
	// 兼容旧数据：历史消息无时间戳时继承会话创建时间，避免界面出现空时间。
	for _, c := range s.Convs {
		for i := range c.Messages {
			if c.Messages[i].Ts == "" && !c.CreatedAt.IsZero() {
				c.Messages[i].Ts = c.CreatedAt.Format(time.RFC3339)
			}
		}
	}
}

// saveLocked 落盘（调用方需持锁）。
func (s *webStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Convs []*conversation `json:"conversations"`
		Set   *webSettings    `json:"settings"`
	}{s.Convs, s.Set}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// saveIfDirtyLocked 仅在内存有改动时落盘，成功则刷新指纹并清除 dirty。
// 调用方需持锁。
func (s *webStore) saveIfDirtyLocked() {
	if !s.dirty {
		return
	}
	if err := s.saveLocked(); err == nil {
		s.dirty = false
		s.stamp, _ = statStamp(s.path)
	}
}

// list 返回会话摘要（按更新时间倒序）。
func (s *webStore) list() []*conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	out := make([]*conversation, len(s.Convs))
	copy(out, s.Convs)
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// get 取单个会话（返回副本，避免调用方越锁读写）。
func (s *webStore) get(id string) *conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			cp := *c
			cp.Messages = append([]convMessage(nil), c.Messages...)
			return &cp
		}
	}
	return nil
}

// create 新建会话。
func (s *webStore) create(mode string) *conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	c := &conversation{
		ID:        "c" + time.Now().Format("20060102150405") + randHex(4),
		Title:     "新会话",
		Mode:      mode,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.Convs = append(s.Convs, c)
	s.dirty = true
	s.saveIfDirtyLocked()
	return c
}

// delete 删除会话，返回是否存在。
func (s *webStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for i, c := range s.Convs {
		if c.ID == id {
			s.Convs = append(s.Convs[:i], s.Convs[i+1:]...)
			s.dirty = true
			s.saveIfDirtyLocked()
			return true
		}
	}
	return false
}

// rename 重命名会话。
func (s *webStore) rename(id, title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Title = title
			c.UpdatedAt = time.Now()
			s.dirty = true
			s.saveIfDirtyLocked()
			return true
		}
	}
	return false
}

// setAutoTitle 写入自动提炼的标题（不覆盖用户手动标题）。
// genAt 记录生成时的消息条数，供下次判断是否需要重新提炼。返回是否写入。
func (s *webStore) setAutoTitle(id, title string, genAt int) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			if c.TitleManual {
				return false
			}
			c.Title = title
			c.TitleGenAt = genAt
			s.dirty = true
			s.saveIfDirtyLocked()
			return true
		}
	}
	return false
}

// markTitleManual 标记该会话标题已被用户手动指定（之后不再自动覆盖）。
func (s *webStore) markTitleManual(id string, manual bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			c.TitleManual = manual
			s.dirty = true
			s.saveIfDirtyLocked()
			return
		}
	}
}

// appendMsg 追加一条消息（首条 user 消息自动作为标题）。
func (s *webStore) appendMsg(id string, m convMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			if m.Ts == "" {
				m.Ts = time.Now().Format(time.RFC3339)
			}
			c.Messages = append(c.Messages, m)
			c.UpdatedAt = time.Now()
			if m.Role == "user" && (c.Title == "新会话" || c.Title == "") {
				c.Title = titleFrom(m.Content)
			}
			s.dirty = true
			s.saveIfDirtyLocked()
			return
		}
	}
}

// setMode 记录会话上次使用的模式。
func (s *webStore) setMode(id, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Mode = mode
			s.dirty = true
			s.saveIfDirtyLocked()
			return
		}
	}
}

// setMessages 用新消息列表替换某会话历史（供 /compact 回写压缩结果）。
func (s *webStore) setMessages(id string, msgs []convMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Messages = msgs
			c.UpdatedAt = time.Now()
			s.dirty = true
			s.saveIfDirtyLocked()
			return
		}
	}
}

// getSettings 读取设置副本。
func (s *webStore) getSettings() webSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	if s.Set == nil {
		return webSettings{}
	}
	return *s.Set
}

// saveSettings 保存设置。
func (s *webStore) saveSettings(v webSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureFreshLocked()
	s.Set = &v
	s.dirty = true
	s.saveIfDirtyLocked()
}

// titleFrom 从消息内容生成标题（取首行前 24 字符）。
func titleFrom(s string) string {
	r := []rune(s)
	// 取首行
	for i, ch := range r {
		if ch == '\n' {
			r = r[:i]
			break
		}
	}
	if len(r) > 24 {
		r = r[:24]
	}
	if len(r) == 0 {
		return "新会话"
	}
	return string(r)
}
