// Package session 提供 JSONL 会话持久化：每个会话一个 .jsonl 文件，逐行追加。
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mizar/internal/agent"
)

// Store 会话存储。
type Store struct {
	mu  sync.Mutex
	dir string
}

// New 创建会话存储目录。
func New(dir string) *Store {
	return &Store{dir: dir}
}

// sessionFile 返回会话文件路径。
func (s *Store) sessionFile(id string) string {
	return filepath.Join(s.dir, id+".jsonl")
}

// Append 追加一条消息。
func (s *Store) Append(id string, msg agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(id, "/") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid session id: %q", id)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.sessionFile(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load 读取整个会话。
func (s *Store) Load(id string) ([]agent.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.sessionFile(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q not found", id)
		}
		return nil, err
	}
	defer f.Close()
	var msgs []agent.Message
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m agent.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // 跳过坏行
		}
		msgs = append(msgs, m)
	}
	return msgs, sc.Err()
}

// List 列出会话 ID。
func (s *Store) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Strings(out)
	return out
}

// Delete 删除会话。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.sessionFile(id))
	if err != nil {
		return fmt.Errorf("delete session %q: %w", id, err)
	}
	return nil
}
