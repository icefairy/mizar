package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
}

// webSettings 对话参数设置（进程级，全局生效）。
type webSettings struct {
	Model       string   `json:"model,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	System      string   `json:"system,omitempty"`
	Thinking    string   `json:"thinking,omitempty"`    // auto/off/low/medium/high
	MaxHistory  int      `json:"max_history,omitempty"` // 普通对话模式最多发送的历史消息条数（默认 50；agent 模式不生效）
}

// webStore 会话 + 设置存储（带文件持久化）。
type webStore struct {
	mu    sync.Mutex
	path  string
	Convs []*conversation `json:"conversations"`
	Set   *webSettings    `json:"settings,omitempty"`
}

func newWebStore(path string) *webStore {
	s := &webStore{path: path, Set: &webSettings{}}
	s.load()
	return s
}

func (s *webStore) load() {
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

// list 返回会话摘要（按更新时间倒序）。
func (s *webStore) list() []*conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*conversation, len(s.Convs))
	copy(out, s.Convs)
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// get 取单个会话（返回副本，避免调用方越锁读写）。
func (s *webStore) get(id string) *conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	c := &conversation{
		ID:        "c" + time.Now().Format("20060102150405") + randHex(4),
		Title:     "新会话",
		Mode:      mode,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.Convs = append(s.Convs, c)
	_ = s.saveLocked()
	return c
}

// delete 删除会话，返回是否存在。
func (s *webStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.Convs {
		if c.ID == id {
			s.Convs = append(s.Convs[:i], s.Convs[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// rename 重命名会话。
func (s *webStore) rename(id, title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Title = title
			c.UpdatedAt = time.Now()
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// appendMsg 追加一条消息（首条 user 消息自动作为标题）。
func (s *webStore) appendMsg(id string, m convMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
			_ = s.saveLocked()
			return
		}
	}
}

// setMode 记录会话上次使用的模式。
func (s *webStore) setMode(id, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Mode = mode
			_ = s.saveLocked()
			return
		}
	}
}

// setMessages 用新消息列表替换某会话历史（供 /compact 回写压缩结果）。
func (s *webStore) setMessages(id string, msgs []convMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Convs {
		if c.ID == id {
			c.Messages = msgs
			c.UpdatedAt = time.Now()
			_ = s.saveLocked()
			return
		}
	}
}

// getSettings 读取设置副本。
func (s *webStore) getSettings() webSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Set == nil {
		return webSettings{}
	}
	return *s.Set
}

// saveSettings 保存设置。
func (s *webStore) saveSettings(v webSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Set = &v
	_ = s.saveLocked()
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
