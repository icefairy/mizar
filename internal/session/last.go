package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// lastFile 记录「全局最后一个退出的会话」的状态文件。
//
// 它放在会话根目录（默认 ~/.mizar/sessions/）而不是各路径作用域子目录里：
// `mizar -c` 的语义是「续接全局最后退出的会话」，既然是全局唯一的一份状态，
// 就应该只有一个文件，而不是每个作用域各写一份再按 mtime 猜哪份最新
// （那样还要处理「作用域目录被删/改名」等边界）。
//
// 文件名不以 .jsonl 结尾，因此不会被 List/Latest/ScopedList 当成会话条目。
const lastFile = "last-session.json"

// LastState 是 last-session.json 的结构。
type LastState struct {
	// ID 会话 ID。
	ID string `json:"id"`
	// Scope 会话所在的路径作用域目录名（如 "--data-codes-mizar--"），空=平铺存储。
	Scope string `json:"scope,omitempty"`
	// UpdatedAt 记录时间（RFC3339），仅供诊断查看。
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SetLast 记录当前作用域下最后一个退出的会话 ID（全局唯一一份，覆盖写）。
// 供下次 `mizar -c` 直接续接。
func (s *Store) SetLast(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID(id); err != nil {
		return err
	}
	// s.dir 始终是会话根目录（Scoped 只改 pathKey，不改 dir）
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(LastState{ID: id, Scope: s.pathKey}, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：先写临时文件再 rename，避免并发/中断留下半个 JSON。
	path := filepath.Join(s.dir, lastFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Last 读取全局最后一个退出会话的记录（无记录返回空 ID）。
// 返回的 scope 为该会话所在的路径作用域（空=平铺）。
//
// 兼容旧格式：早期版本把纯文本 ID 写在 <scope>/last-session，这里认得并回填 scope。
func (s *Store) Last() (id, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, lastFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return s.legacyLast()
	}
	var st LastState
	if err := json.Unmarshal(b, &st); err != nil {
		// 不是 JSON：按旧「纯文本」格式处理
		v := strings.TrimSpace(string(b))
		return v, ""
	}
	return strings.TrimSpace(st.ID), st.Scope
}

// legacyLast 读取旧版按作用域存放的最后一手记录（<root>/<scope>/last-session）。
// 仅用于升级平滑，不参与新写入。
func (s *Store) legacyLast() (id, scope string) {
	const legacyName = "last-session"
	var bestMod int64 = -1
	consider := func(dir, scopeName string) {
		p := filepath.Join(dir, legacyName)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			return
		}
		if info.ModTime().UnixNano() <= bestMod {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return
		}
		id, scope, bestMod = v, scopeName, info.ModTime().UnixNano()
	}
	consider(s.dir, "")
	if dirs, err := os.ReadDir(s.dir); err == nil {
		for _, d := range dirs {
			if !d.IsDir() || !strings.HasPrefix(d.Name(), "--") {
				continue
			}
			consider(filepath.Join(s.dir, d.Name()), d.Name())
		}
	}
	return id, scope
}

// Exists 判断某个会话是否已落盘（有消息即存在）。
func (s *Store) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID(id); err != nil {
		return false
	}
	_, err := os.Stat(s.sessionFile(id))
	return err == nil
}
