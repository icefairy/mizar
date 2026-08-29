// Package session 新增会话标题能力（复刻 deepseek-harness session-title）。
//
// 行为：
//   - DeriveTitle(msgs)：取第一条 RoleUser 消息，去掉换行，截断到 60 字符作为 fallback 标题。
//   - Store 增加 SaveTitle/LoadTitle（titles.json 缓存），独立于会话日志。
//   - /sessions 命令使用 titles 展示。
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mizar/internal/agent"
)

// LoadTitles 从 titles.json 加载标题映射（每个路径作用域一份）。
func LoadTitles(dir string) (map[string]string, error) {
	path := filepath.Join(dir, "titles.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("load titles: %w", err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse titles: %w", err)
	}
	return out, nil
}

// SaveTitles 写入 titles.json。
func SaveTitles(dir string, titles map[string]string) error {
	path := filepath.Join(dir, "titles.json")
	data, err := json.MarshalIndent(titles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal titles: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// TitleCache 线程安全、在内存中维护标题映射（配合 SaveTitles 做持久化）。
type TitleCache struct {
	mu     sync.RWMutex
	dir    string
	titles map[string]string
}

// NewTitleCache 创建缓存（dir 为 session store 根目录，titles.json 存放于此）。
func NewTitleCache(dir string) (*TitleCache, error) {
	titles, err := LoadTitles(dir)
	if err != nil {
		return nil, err
	}
	return &TitleCache{dir: dir, titles: titles}, nil
}

// Get 查询标题（未设置返回空串）。
func (tc *TitleCache) Get(id string) string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.titles[id]
}

// Set 设置标题（同时持久化到 titles.json）。
func (tc *TitleCache) Set(id, title string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if title == "" {
		delete(tc.titles, id)
	} else {
		tc.titles[id] = title
	}
	return SaveTitles(tc.dir, tc.titles)
}

// ListAll 返回所有会话 ID 及对应标题（nil title 的会话返回空串）。
func (tc *TitleCache) ListAll() map[string]string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	out := make(map[string]string, len(tc.titles))
	for k, v := range tc.titles {
		out[k] = v
	}
	return out
}

// DeriveTitle 从消息列表推导 fallback 标题（对齐 dsh fallback：首条 user 消息）。
func DeriveTitle(msgs []agent.Message) string {
	for _, m := range msgs {
		if m.Role == agent.RoleUser && m.Content != "" {
			s := strings.ReplaceAll(m.Content, "\n", " ")
			s = strings.TrimSpace(s)
			if len(s) > 60 {
				s = s[:60] + "…"
			}
			return s
		}
	}
	return ""
}
