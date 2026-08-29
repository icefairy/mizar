// Package builtins 新增 todo_write 工具（复刻 deepseek-harness tool-todo）。
//
// todo_write 是 session-owned 的结构化任务清单工具：每条记录 content + status
// （pending/in_progress/completed）。每次调用替换整个清单（last-write-wins）。
package builtins

import (
	"encoding/json"

	"mizar/internal/plugins"
	"fmt"
	"strings"
	"sync"
)

// TodoItem 一条清单条目。
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

const (
	StatusPending   = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted = "completed"
)

// Registry 进程内 todo 清单（单 session 级，Reset 清空）。
// 由 main.go 注入 Agent，tool_todo_write 通过全局指针访问；
// 为保持简单不引入 session-scoped 隔离——mizar 当前场景下单 binary 单 session 足够。
type Registry struct {
	mu       sync.Mutex
	items    []TodoItem
	staleSeq int // 变更计数器，重置时递增
}

var todoReg = &Registry{}

// Reset 新任务开始时调用（对齐 dsh turn/start 清除投影）。
func ResetTodo() {
	todoReg.mu.Lock()
	defer todoReg.mu.Unlock()
	todoReg.items = nil
	todoReg.staleSeq++
}

// List 返回当前清单（线程安全）。
func ListTodos() []TodoItem {
	todoReg.mu.Lock()
	defer todoReg.mu.Unlock()
	out := make([]TodoItem, len(todoReg.items))
	copy(out, todoReg.items)
	return out
}

// Render 将清单渲染成人类可读 checklist 文本（供 TUI 展示或作为工具返回值）。
func RenderTodos() string {
	items := ListTodos()
	if len(items) == 0 {
		return "（暂无待办）"
	}
	var sb strings.Builder
	for i, it := range items {
		var icon string
		switch it.Status {
		case StatusInProgress:
			icon = "▶"
		case StatusCompleted:
			icon = "✓"
		default:
			icon = "○"
		}
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, icon, it.Content)
	}
	return sb.String()
}

// toolTodoWrite 返回 todo_write 工具。
func toolTodoWrite() plugins.Tool {
	return plugins.Tool{
		Name:        "todo_write",
		Description: "Record and update a structured task list for the current work. Send the ENTIRE list every call — it REPLACES the previous list. Use statuses: pending (not started), in_progress (being worked on now), completed (finished). Keep AT MOST ONE item in_progress at a time. Skip the list for trivial single-step tasks.",
		Run: func(args string) (string, error) {
			var raw []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			}
			if err := json.Unmarshal([]byte(args), &raw); err != nil || len(raw) == 0 {
				return "", fmt.Errorf("todo_write: args must be a non-empty JSON array of {content, status}")
			}
			seen := map[string]bool{}
			var items []TodoItem
			active := 0
			for _, it := range raw {
				c := strings.TrimSpace(it.Content)
				if c == "" {
					return "", fmt.Errorf("todo_write: content must be non-empty")
				}
				if seen[c] {
					return "", fmt.Errorf("todo_write: duplicate content %q", c)
				}
				seen[c] = true
				switch it.Status {
				case StatusPending, StatusInProgress, StatusCompleted:
				default:
					return "", fmt.Errorf("todo_write: invalid status %q (expected pending/in_progress/completed)", it.Status)
				}
				if it.Status == StatusInProgress {
					active++
				}
				items = append(items, TodoItem{Content: c, Status: it.Status})
			}
			if active > 1 {
				return "", fmt.Errorf("todo_write: at most one item may be in_progress (got %d)", active)
			}
			todoReg.mu.Lock()
			todoReg.items = items
			todoReg.staleSeq++
			todoReg.mu.Unlock()
			return "✓ 已更新待办清单。\n" + renderItems(items), nil
		},
	}
}

func renderItems(items []TodoItem) string {
	var sb strings.Builder
	for i, it := range items {
		var icon string
		switch it.Status {
		case StatusInProgress:
			icon = "▶"
		case StatusCompleted:
			icon = "✓"
		default:
			icon = "○"
		}
		fmt.Fprintf(&sb, "%d. %s %s\n", i+1, icon, it.Content)
	}
	return sb.String()
}
