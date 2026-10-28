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

// entryType 会话文件条目的类型标识。
//
// 文件仍是「一行一个 JSON」的 JSONL。为了向后兼容，消息行不带 type 字段
// （旧文件原样可读），新增的元记录才带 type。
const (
	entryTypeMessage    = "message"    // 普通消息（旧文件缺省即此类型）
	entryTypeCompaction = "compaction" // 压缩记录（追加式，不改写历史）
)

// envelope 仅用于取出一行的 type 字段，决定后续按哪种结构解析。
type envelope struct {
	Type string `json:"type,omitempty"`
}

// Compaction 一次压缩的记录（对齐 pi 的 CompactionEntry）。
//
// 设计要点：压缩是「追加一条记录」，原始消息一行不删。
// 加载时若存在压缩记录，重建为 summary + 从 FirstKeptIndex 起的所有消息，
// 从而保证跨进程续接仍享受压缩成果，且 TUI/导出可看完整历史。
type Compaction struct {
	// Summary 被摘要掉的那段历史的摘要文本。
	Summary string `json:"summary"`
	// FirstKeptIndex 从第几条消息开始保留（按消息在文件中的出现顺序计数）。
	// 等于消息总数表示不保留任何前置消息（retain-none）。
	FirstKeptIndex int `json:"first_kept_index"`
	// TokensBefore 压缩前的估算 token 数（诊断用）。
	TokensBefore int `json:"tokens_before,omitempty"`
	// Emergency 标记这是一条「自愈」裁剪记录（上下文溢出应急降级）。
	// 与常规压缩同一套加载逻辑（summary + FirstKeptIndex 起保留），
	// 区别仅在于 Emergency 没有 LLM 摘要，Summary 通常为空（纯截断）。
	Emergency bool `json:"emergency,omitempty"`
	// EmergencyPct 应急裁剪的比例（%），仅 Emergency 时有效（诊断用）。
	EmergencyPct int `json:"emergency_pct,omitempty"`
}

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

// validateID 校验会话 ID（防路径穿越）。
func validateID(id string) error {
	if strings.Contains(id, "/") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid session id: %q", id)
	}
	return nil
}

// appendLine 追加一行原始 JSON（调用方需自行加锁/建目录）。
func (s *Store) appendLine(id string, b []byte) error {
	f, err := os.OpenFile(s.sessionFile(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Append 追加一条消息。
func (s *Store) Append(id string, msg agent.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.MkdirAll(s.sessionDir(), 0o755); err != nil {
		return err
	}
	// 落盘时补写时间戳（未设置才填）：所有调用方无需关心，单条消息一个时间点。
	msg.EnsureTimestamp()
	// 消息行不写 type 字段，保持旧格式（旧版本 mizar 仍能读）。
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.appendLine(id, b)
}

// AppendCompaction 追加一条压缩记录（追加式，不改写已有历史）。
//
// firstKeptIndex 是「保留起点」在消息序列中的下标；等于消息总数表示不保留前置消息。
// 调用方（压缩发生处）已知切点，直接传入即可。
func (s *Store) AppendCompaction(id, summary string, firstKeptIndex, tokensBefore int) error {
	return s.appendCompactionRecord(id, Compaction{
		Summary:        summary,
		FirstKeptIndex: firstKeptIndex,
		TokensBefore:   tokensBefore,
	})
}

// AppendEmergencyTrim 追加一条「自愈」裁剪记录（上下文溢出应急降级）。
//
// 与 AppendCompaction 共用加载逻辑：下次 Load 时从 firstKeptIndex 开始保留。
// 持久化的意义：用户退出会话再进来时，不会再重新撞一次 400。
func (s *Store) AppendEmergencyTrim(id string, firstKeptIndex, tokensBefore, pct int) error {
	return s.appendCompactionRecord(id, Compaction{
		FirstKeptIndex: firstKeptIndex,
		TokensBefore:   tokensBefore,
		Emergency:      true,
		EmergencyPct:   pct,
	})
}

// appendCompactionRecord 追加一条压缩/应急记录（两者格式同构）。
func (s *Store) appendCompactionRecord(id string, rec Compaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.MkdirAll(s.sessionDir(), 0o755); err != nil {
		return err
	}
	if rec.FirstKeptIndex < 0 {
		rec.FirstKeptIndex = 0
	}
	// 平铺字段：{"type":"compaction","summary":...,"first_kept_index":...}
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	m["type"] = entryTypeCompaction
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.appendLine(id, b)
}

// readEntries 读取文件并按类型拆分消息与压缩记录（保持消息的原始顺序）。
func (s *Store) readEntries(id string) (msgs []agent.Message, comps []Compaction, err error) {
	f, err := os.Open(s.sessionFile(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("session %q not found", id)
		}
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// 单行可能是长工具输出，放宽扫描上限（默认 64KB 太小）。
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue // 跳过坏行
		}
		switch env.Type {
		case entryTypeCompaction:
			var c Compaction
			if err := json.Unmarshal([]byte(line), &c); err == nil {
				comps = append(comps, c)
			}
		default: // 空 type、message 或未知类型（容错当作消息）
			var m agent.Message
			if err := json.Unmarshal([]byte(line), &m); err == nil {
				msgs = append(msgs, m)
			}
		}
	}
	return msgs, comps, sc.Err()
}

// Load 读取会话（应用压缩：返回 summary + 保留范围 + 压缩后新增的消息）。
func (s *Store) Load(id string) ([]agent.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, comps, err := s.readEntries(id)
	if err != nil {
		return nil, err
	}
	if len(comps) == 0 {
		return msgs, nil
	}
	// 只取最新一条压缩记录生效（与 pi 一致）。
	latest := comps[len(comps)-1]
	idx := latest.FirstKeptIndex
	if idx < 0 {
		idx = 0
	}
	if idx > len(msgs) {
		idx = len(msgs)
	}
	out := make([]agent.Message, 0, 1+len(msgs)-idx)
	if strings.TrimSpace(latest.Summary) != "" {
		out = append(out, agent.Message{
			Role:    agent.RoleUser,
			Kind:    agent.KindSummary,
			Content: latest.Summary,
		})
	}
	out = append(out, msgs[idx:]...)
	return out, nil
}

// LoadRaw 读取会话的原始消息（忽略压缩记录，供导出/诊断使用）。
func (s *Store) LoadRaw(id string) ([]agent.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, _, err := s.readEntries(id)
	return msgs, err
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
