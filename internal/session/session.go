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
	mu      sync.Mutex
	dir     string
	pathKey string // 路径作用域（如 "--data-codes-mizar--"），空=平铺
}

// New 创建会话存储目录。
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Scoped 返回绑定路径作用域的 Store（模仿 pi Agent：会话按工作路径分类）。
// pathKey 为空则行为与 New 一致（平铺存储）。
func (s *Store) Scoped(pathKey string) *Store {
	return &Store{dir: s.dir, pathKey: pathKey}
}

// PathKey 返回当前路径作用域。
func (s *Store) PathKey() string { return s.pathKey }

// sessionFile 返回会话文件路径。
func (s *Store) sessionFile(id string) string {
	if s.pathKey != "" {
		return filepath.Join(s.dir, s.pathKey, id+".jsonl")
	}
	return filepath.Join(s.dir, id+".jsonl")
}

// sessionDir 返回会话实际目录（路径作用域下的子目录）。
func (s *Store) sessionDir() string {
	if s.pathKey != "" {
		return filepath.Join(s.dir, s.pathKey)
	}
	return s.dir
}

// Append 追加一条消息。
func (s *Store) Append(id string, msg agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(id, "/") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid session id: %q", id)
	}
	if err := os.MkdirAll(s.sessionDir(), 0o755); err != nil {
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

// List 列出当前作用域（路径）下的会话 ID。
func (s *Store) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.sessionDir())
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

// Latest 返回当前作用域下最近修改的会话 ID（用于自动恢复路径记忆）。
// 无会话时返回空字符串。
func (s *Store) Latest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.sessionDir())
	if err != nil {
		return ""
	}
	var latest string
	var latestMod int64 = -1
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() > latestMod {
			latestMod = info.ModTime().UnixNano()
			latest = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return latest
}

// ScopedList 列出所有路径作用域及其会话数（用于记忆概览）。
func (s *Store) ScopedList() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]string{}
	dirs, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "--") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.dir, d.Name()))
		if err != nil {
			continue
		}
		var ids []string
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				ids = append(ids, strings.TrimSuffix(f.Name(), ".jsonl"))
			}
		}
		if len(ids) > 0 {
			sort.Strings(ids)
			out[d.Name()] = ids
		}
	}
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

// EncodePathKey 将绝对路径编码为路径作用域 key（模仿 pi Agent：--data-codes-mizar--）。
// 空路径或 "." 返回空串（平铺）。
func EncodePathKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	abs = filepath.Clean(abs)
	// 统一斜杠、去掉盘符冒号，/ -> -
	p := filepath.ToSlash(abs)
	p = strings.TrimPrefix(p, "/")
	p = strings.ReplaceAll(p, "/", "-")
	p = strings.ReplaceAll(p, ":", "")
	if p == "" || p == "." {
		return ""
	}
	return "--" + p + "--"
}
