// Package sessionsearch 实现会话全文搜索（H-1，对齐 hermes session_search）。
//
// 问题：mizar 的会话是 JSONL append-only，`/sessions` 只有列表、没法按内容找
// ——「上周是不是问过这个？」只能手动 grep 一堆文件。
//
// 解法：SQLite FTS5 增量索引（modernc.org/sqlite 已在依赖里，纯 Go 无 cgo）：
//   - `~/.mizar/sessions/search.db`，表 messages(session_id, scope, seq, role, kind, tool, content)
//   - FTS5 虚表（tokenize=trigram，中文/英文都能子串匹配）
//   - 增量：记每个会话已索引的文件字节数，下次从该偏移继续读尾部，只索引新增行
//   - 只索引「人读得懂」的消息：user/assistant 普通文本、摘要、工具调用（tool:name args）；
//     工具的长输出不入索引（噪声且体积大，需要时用 SCROLL 模式回看原文）
//
// 与 hermes 的差异：hermes 有四模式（DISCOVERY/SCROLL/READ/BROWSE）合一工具；
// 本包用同一套模式语义，但按 mizar 的 JSONL + 路径作用域约定简化实现。
package sessionsearch

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"mizar/internal/agent"
)

// Hit 一条搜索命中。
type Hit struct {
	SessionID string `json:"session_id"`
	Scope     string `json:"scope,omitempty"`
	Seq       int    `json:"seq"` // 会话内第几条消息（1-based）
	Role      string `json:"role"`
	Tool      string `json:"tool,omitempty"`
	Snippet   string `json:"snippet"`
}

// SessionInfo 一个会话的索引摘要。
type SessionInfo struct {
	SessionID string `json:"session_id"`
	Scope     string `json:"scope,omitempty"`
	Messages  int    `json:"messages"`
	LastTs    string `json:"last_ts,omitempty"`
}

// Index 全文索引。
type Index struct {
	mu   sync.Mutex
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS indexed_sessions (
	session_id TEXT PRIMARY KEY,
	scope      TEXT NOT NULL DEFAULT '',
	path       TEXT NOT NULL,
	indexed_bytes INTEGER NOT NULL DEFAULT 0,
	msg_count  INTEGER NOT NULL DEFAULT 0,
	last_ts    TEXT NOT NULL DEFAULT ''
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages USING fts5(
	session_id UNINDEXED,
	scope UNINDEXED,
	seq UNINDEXED,
	role UNINDEXED,
	tool UNINDEXED,
	ts UNINDEXED,
	content,
	tokenize='trigram'
);
`

// Open 打开/创建索引（path 通常是 ~/.mizar/sessions/search.db）。
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sessionsearch: mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("sessionsearch: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sessionsearch: 建表失败（FTS5 不可用？）: %w", err)
	}
	return &Index{db: db, path: path}, nil
}

// Close 关闭索引。
func (ix *Index) Close() error {
	if ix == nil || ix.db == nil {
		return nil
	}
	return ix.db.Close()
}

// Path 返回索引文件路径。
func (ix *Index) Path() string { return ix.path }

// Sync 增量同步索引：扫描 sessionsRoot 下的所有会话文件（含路径作用域子目录），
// 对每个文件从上次的字节偏移继续索引。返回本次新增的消息条数。
//
// 容错：单个文件读失败只跳过它（不影响其余）；已删除的会话从索引里清掉。
func (ix *Index) Sync(sessionsRoot string) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	files, err := collectSessionFiles(sessionsRoot)
	if err != nil {
		return 0, err
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.sessionID] = true
		n, err := ix.syncFile(f)
		if err != nil {
			continue // 单文件失败不影响整体
		}
		total += n
	}
	// 清理已删除的会话
	rows, err := ix.db.Query(`SELECT session_id FROM indexed_sessions`)
	if err == nil {
		var gone []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !seen[id] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		for _, id := range gone {
			ix.db.Exec(`DELETE FROM indexed_sessions WHERE session_id=?`, id)
			ix.db.Exec(`DELETE FROM messages WHERE session_id=?`, id)
		}
	}
	return total, nil
}

type sessionFile struct {
	sessionID string
	scope     string
	path      string
}

// collectSessionFiles 遍历 <root>/<scope>/<id>.jsonl 与 <root>/<id>.jsonl。
func collectSessionFiles(root string) ([]sessionFile, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []sessionFile
	for _, e := range ents {
		if e.IsDir() {
			// 路径作用域子目录
			scope := e.Name()
			sub, err := os.ReadDir(filepath.Join(root, scope))
			if err != nil {
				continue
			}
			for _, f := range sub {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				out = append(out, sessionFile{
					sessionID: strings.TrimSuffix(f.Name(), ".jsonl"),
					scope:     scope,
					path:      filepath.Join(root, scope, f.Name()),
				})
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		out = append(out, sessionFile{
			sessionID: strings.TrimSuffix(e.Name(), ".jsonl"),
			path:      filepath.Join(root, e.Name()),
		})
	}
	return out, nil
}

// syncFile 从上次偏移继续索引一个会话文件。
func (ix *Index) syncFile(f sessionFile) (int, error) {
	var indexedBytes, msgCount int
	var lastTs string
	err := ix.db.QueryRow(`SELECT indexed_bytes, msg_count, last_ts FROM indexed_sessions WHERE session_id=?`, f.sessionID).
		Scan(&indexedBytes, &msgCount, &lastTs)
	exists := err == nil

	fi, err := os.Stat(f.path)
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if exists && size < int64(indexedBytes) {
		// 文件被截断/重写：重建该会话索引
		ix.db.Exec(`DELETE FROM messages WHERE session_id=?`, f.sessionID)
		ix.db.Exec(`DELETE FROM indexed_sessions WHERE session_id=?`, f.sessionID)
		indexedBytes, msgCount = 0, 0
		exists = false
	}
	if exists && size == int64(indexedBytes) {
		return 0, nil // 无新增
	}

	fh, err := os.Open(f.path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	if indexedBytes > 0 {
		if _, err := fh.Seek(int64(indexedBytes), 0); err != nil {
			return 0, err
		}
	}

	tx, err := ix.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	added := 0
	consumed := int64(indexedBytes)
	for sc.Scan() {
		line := sc.Bytes()
		consumed += int64(len(line)) + 1 // +1 换行
		text := strings.TrimSpace(string(line))
		if text == "" {
			continue
		}
		m, ok := indexableMessage(text)
		if !ok {
			continue
		}
		msgCount++
		if m.ts != "" {
			lastTs = m.ts
		}
		if _, err := tx.Exec(
			`INSERT INTO messages(session_id, scope, seq, role, tool, ts, content) VALUES(?,?,?,?,?,?,?)`,
			f.sessionID, f.scope, msgCount, m.role, m.tool, m.ts, m.content); err != nil {
			continue
		}
		added++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if exists {
		_, err = tx.Exec(`UPDATE indexed_sessions SET scope=?, path=?, indexed_bytes=?, msg_count=?, last_ts=? WHERE session_id=?`,
			f.scope, f.path, consumed, msgCount, lastTs, f.sessionID)
	} else {
		_, err = tx.Exec(`INSERT INTO indexed_sessions(session_id, scope, path, indexed_bytes, msg_count, last_ts) VALUES(?,?,?,?,?,?)`,
			f.sessionID, f.scope, f.path, consumed, msgCount, lastTs)
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

type doc struct {
	role    string
	tool    string
	ts      string
	content string
}

// indexableMessage 判断一行要不要进索引，并抽出可搜索正文。
//
// 只索引人读得懂的部分：
//   - user/assistant 普通消息与摘要 → 正文
//   - 工具调用（KindToolCall）→ "tool:name args"（便于找「哪次会话跑过 X」）
//   - 工具结果 / 压缩记录 / fork 记录 / subagent 标记 → 跳过（噪声大且体积大）
func indexableMessage(line string) (doc, bool) {
	// 元数据行（压缩/fork/subagent 快照）不进索引
	var env struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &env) == nil && env.Type != "" && env.Type != "message" {
		return doc{}, false
	}
	var m agent.Message
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return doc{}, false
	}
	d := doc{role: m.Role, ts: m.Timestamp}
	switch m.Kind {
	case agent.KindToolResult:
		return doc{}, false
	case agent.KindToolCall:
		content := strings.TrimSpace(m.ToolName + " " + m.ToolArgs)
		if content == "" {
			return doc{}, false
		}
		d.tool = m.ToolName
		d.content = "tool:" + content
		return d, true
	}
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return doc{}, false
	}
	// subagent 状态标记不服务检索
	if strings.Contains(content, "<!--subagent-state:") {
		return doc{}, false
	}
	d.content = content
	return d, true
}

// Search 关键词搜索（FTS5 MATCH）。返回按相关度排序的命中。
//
// 查询串按 FTS5 语法处理；为容错，先试短语匹配，失败再退化为逐词 AND。
func (ix *Index) Search(query string, limit int) ([]Hit, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	// trigram 分词只能命中 ≥3 字符的子串；含短词（1~2 字符，含中文单字）时
	// MATCH 恒不命中，退回 LIKE 全扫描（会话索引体量小，可接受）。
	if hasShortTerm(query) {
		return ix.searchLike(query, limit)
	}
	q := buildMatchQuery(query)
	if q == "" {
		return nil, fmt.Errorf("session_search: 查询词为空")
	}
	rows, err := ix.db.Query(`
SELECT session_id, scope, seq, role, tool,
       snippet(messages, 6, '《', '》', '…', 24)
FROM messages WHERE messages MATCH ?
ORDER BY rank LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("session_search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var seq int
		if err := rows.Scan(&h.SessionID, &h.Scope, &seq, &h.Role, &h.Tool, &h.Snippet); err != nil {
			continue
		}
		h.Seq = seq
		out = append(out, h)
	}
	return out, rows.Err()
}

// hasShortTerm 报告查询里是否含 <3 字符的词（trigram 无法索引）。
func hasShortTerm(query string) bool {
	for _, t := range strings.Fields(query) {
		if len([]rune(t)) < 3 {
			return true
		}
	}
	return false
}

// searchLike 短词退化路径：对 content 做 AND LIKE。
func (ix *Index) searchLike(query string, limit int) ([]Hit, error) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil, fmt.Errorf("session_search: 查询词为空")
	}
	where := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+1)
	for _, t := range terms {
		where = append(where, "content LIKE ?")
		args = append(args, "%"+t+"%")
	}
	args = append(args, limit)
	sqlStr := `SELECT session_id, scope, seq, role, tool, substr(content, 1, 200)
FROM messages WHERE ` + strings.Join(where, " AND ") + ` LIMIT ?`
	rows, err := ix.db.Query(sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("session_search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var seq int
		if err := rows.Scan(&h.SessionID, &h.Scope, &seq, &h.Role, &h.Tool, &h.Snippet); err != nil {
			continue
		}
		h.Seq = seq
		out = append(out, h)
	}
	return out, rows.Err()
}

// IndexedMessage 索引里的一条消息（供上下文回看）。
type IndexedMessage struct {
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Tool    string `json:"tool,omitempty"`
	Ts      string `json:"ts,omitempty"`
	Snippet string `json:"snippet"`
}

// SessionMessages 取某会话的消息：seq>0 时取 seq 前后 window 条；seq==0 时取最多 max 条。
func (ix *Index) SessionMessages(sessionID string, seq, window, max int) ([]IndexedMessage, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if max <= 0 {
		max = 100
	}
	var rows *sql.Rows
	var err error
	if seq > 0 {
		if window <= 0 {
			window = 3
		}
		rows, err = ix.db.Query(
			`SELECT seq, role, tool, ts, content FROM messages WHERE session_id=? AND seq BETWEEN ? AND ? ORDER BY seq`,
			sessionID, seq-window, seq+window)
	} else {
		rows, err = ix.db.Query(
			`SELECT seq, role, tool, ts, content FROM messages WHERE session_id=? ORDER BY seq LIMIT ?`,
			sessionID, max)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedMessage
	for rows.Next() {
		var m IndexedMessage
		if err := rows.Scan(&m.Seq, &m.Role, &m.Tool, &m.Ts, &m.Snippet); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// buildMatchQuery 把用户输入转成 FTS5 查询：每个词加引号（转义内部引号）后 AND。
// 中文整段作为一个「词」时，trigram 分词仍能子串命中。
func buildMatchQuery(query string) string {
	terms := strings.Fields(query)
	var quoted []string
	for _, t := range terms {
		t = strings.ReplaceAll(t, `"`, `""`)
		if strings.TrimSpace(t) == "" {
			continue
		}
		quoted = append(quoted, `"`+t+`"`)
	}
	return strings.Join(quoted, " AND ")
}

// Sessions 列出已索引的会话（按最近时间倒序）。
func (ix *Index) Sessions(limit int) ([]SessionInfo, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if limit <= 0 {
		limit = 30
	}
	rows, err := ix.db.Query(`SELECT session_id, scope, msg_count, last_ts FROM indexed_sessions ORDER BY last_ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.SessionID, &s.Scope, &s.Messages, &s.LastTs); err == nil {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastTs > out[j].LastTs })
	return out, rows.Err()
}
