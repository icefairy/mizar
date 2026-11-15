// 会话全文搜索工具（H-1）：在全部历史会话里按内容检索。
//
// 与 hermes session_search 对齐的四种模式（用参数区分）：
//   - 搜索：{query}                  → DISCOVERY，FTS5 相关度排序
//   - 上下文：{session_id, seq}       → SCROLL，取该会话 seq 附近的窗口
//   - 整段：{session_id}              → READ，返回该会话全部可索引消息
//   - 列表：{}                        → BROWSE，列出已索引会话
package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
	"mizar/internal/sessionsearch"
)

// SessionSearchTool 返回 session_search 工具。
// idx 由调用方持有（进程级单例）；sessionsRoot 用于首次/惰性同步。
func SessionSearchTool(idx *sessionsearch.Index, sessionsRoot string) plugins.Tool {
	if idx == nil {
		return plugins.Tool{}
	}
	return plugins.Tool{
		Name: "session_search",
		Description: "在全部历史会话里检索（跨会话回溯）。何时用：想问「之前是不是聊过 X / 上次怎么解决的」时。" +
			"参数: {query?: string 关键词(≥3字符走FTS5,否则LIKE)} ｜ {session_id?: string, seq?: int 取上下文} ｜ 空 {} 列出已索引会话。",
		Run: func(args string) (string, error) {
			var p struct {
				Query     string `json:"query"`
				SessionID string `json:"session_id"`
				Seq       int    `json:"seq"`
				Limit     int    `json:"limit"`
			}
			_ = json.Unmarshal([]byte(args), &p)

			// 惰性同步：每次调用前增量同步一次（只索引新增行，开销小）
			if _, err := idx.Sync(sessionsRoot); err != nil {
				// 同步失败不阻断检索（可能索引里已有旧数据）
				_ = err
			}

			switch {
			case strings.TrimSpace(p.Query) != "":
				hits, err := idx.Search(p.Query, p.Limit)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return fmt.Sprintf("未找到与「%s」相关的历史消息。", p.Query), nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "找到 %d 条命中（会话 %s ｜ 消息序号 ｜ 片段）：\n", len(hits), "`session_search {session_id,seq}` 可看上下文")
				for _, h := range hits {
					role := h.Role
					if h.Tool != "" {
						role = "tool:" + h.Tool
					}
					fmt.Fprintf(&sb, "• [%s#%d] (%s) %s\n", shortID(h.SessionID), h.Seq, role, strings.TrimSpace(h.Snippet))
				}
				return strings.TrimSpace(sb.String()), nil

			case strings.TrimSpace(p.SessionID) != "":
				return readSessionContext(idx, p.SessionID, p.Seq)

			default:
				list, err := idx.Sessions(p.Limit)
				if err != nil {
					return "", err
				}
				if len(list) == 0 {
					return "（尚无已索引会话；先聊几句）", nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "已索引 %d 个会话：\n", len(list))
				for _, s := range list {
					scope := s.Scope
					if scope == "" {
						scope = "(平铺)"
					}
					fmt.Fprintf(&sb, "• %s  %s  %d 条  %s\n", shortID(s.SessionID), scope, s.Messages, s.LastTs)
				}
				return strings.TrimSpace(sb.String()), nil
			}
		},
	}
}

// readSessionContext 返回某会话 seq 附近的窗口（seq=0 时取该会话全部可索引消息，最多 100 条）。
func readSessionContext(idx *sessionsearch.Index, sessionID string, seq int) (string, error) {
	msgs, err := idx.SessionMessages(sessionID, seq, 3, 100)
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return fmt.Sprintf("会话 %s 无索引消息（或未同步）。", shortID(sessionID)), nil
	}
	var sb strings.Builder
	if seq > 0 {
		fmt.Fprintf(&sb, "会话 %s 第 %d 条附近：\n", shortID(sessionID), seq)
	} else {
		fmt.Fprintf(&sb, "会话 %s（%d 条）：\n", shortID(sessionID), len(msgs))
	}
	for _, m := range msgs {
		role := m.Role
		if m.Tool != "" {
			role = "tool:" + m.Tool
		}
		content := m.Snippet
		if len([]rune(content)) > 400 {
			content = string([]rune(content)[:400]) + "…"
		}
		fmt.Fprintf(&sb, "[%d] (%s) %s\n", m.Seq, role, content)
	}
	return strings.TrimSpace(sb.String()), nil
}

// shortID 截断会话 id（展示用）。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// EnsureSessionsDir 确保会话目录存在（Open 索引前调用）。
func EnsureSessionsDir(root string) string {
	if strings.HasPrefix(root, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
		}
	}
	return root
}
