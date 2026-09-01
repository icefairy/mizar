// pi 式 todo 工具：动作式 API（list/add/toggle/clear）+ 状态随会话持久化 + 回放重建。
//
// 设计来源：pi 的 examples/extensions/todo.ts（状态存 tool result details，
// 会话恢复/branch 时按历史顺序回放重建，状态自动正确）。
//
// mizar 的对齐实现：
//   - 状态内嵌在工具返回文本的标记段 <!--todo-state:{json}-->（TUI 渲染时由
//     StripTodoMarker 过滤，不产生额外噪音；LLM 读到的回复文本很短）；
//   - 每次调用 tool_result 消息落盘，会话恢复时 RebuildTodos 按顺序回放，
//     恢复出当前清单（与 pi 的 reconstructState 语义一致）；
//   - /todos 命令复用同一份状态。
package builtins

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// PiTodo 单个待办项。
type PiTodo struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// piTodoState 全局待办状态（与现有 todoReg 相同生命周期：单进程单会话）。
type piTodoState struct {
	mu    sync.Mutex
	todos []PiTodo
	next  int
}

var todoS = &piTodoState{next: 1}

func (s *piTodoState) snapshot() piState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return piState{Todos: append([]PiTodo(nil), s.todos...), Next: s.next}
}

// ============================================================================
// 状态序列化（内嵌在工具返回文本中，随 tool_result 持久化）
// ============================================================================

// todoStatePrefix / todoStateSuffix 包裹状态 JSON。
const (
	todoStatePrefix = "\n<!--todo-state:"
	todoStateSuffix = "-->"
)

// encodeTodoStatePre 返回 人类可读文本 + 状态标记（调用方必须已持有 todoS.mu）。
func encodeTodoState(text string) string {
	b, err := json.Marshal(piState{Todos: todoS.todos, Next: todoS.next})
	if err != nil {
		return text
	}
	return text + todoStatePrefix + string(b) + todoStateSuffix
}

// piState 持久化载荷。
type piState struct {
	Todos []PiTodo `json:"todos"`
	Next  int      `json:"next"`
}

// extractTodoState 从工具返回文本中提取状态（不含标记）。
func extractTodoState(text string) (piState, bool) {
	start := strings.Index(text, "<!--todo-state:")
	if start < 0 {
		return piState{}, false
	}
	start += len("<!--todo-state:")
	end := strings.Index(text[start:], "-->")
	if end < 0 {
		return piState{}, false
	}
	var s piState
	if err := json.Unmarshal([]byte(text[start:start+end]), &s); err != nil {
		return piState{}, false
	}
	return s, true
}

// StripTodoMarker 移除工具返回文本中的状态标记（TUI 渲染用）。
func StripTodoMarker(text string) string {
	if !strings.Contains(text, "<!--todo-state:") {
		return text
	}
	var sb strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "<!--todo-state:")
		if start < 0 {
			sb.WriteString(rest)
			break
		}
		sb.WriteString(rest[:start])
		after := rest[start+len("<!--todo-state:"):]
		end := strings.Index(after, "-->")
		if end < 0 {
			break
		}
		rest = after[end+len("-->"):]
	}
	return strings.TrimSpace(sb.String())
}

// ============================================================================
// 落盘/回放：状态内嵌在 tool_result 的 Output 字段，随会话 JSONL 持久化
// ============================================================================

// todoResultPrefix 是 agent.ToolResult 消息的 Content 前缀（见 loop.go）。
const todoResultPrefix = "工具结果: "

// SetTodosFromHistory 从会话历史消息重建 todo 状态（pi：reconstructState）。
// 会话恢复（a.Initial 装载）后、首次工具调用前调用一次：
// 扫描所有 kind=tool_result 且 toolName=todo 的消息，按顺序应用到最新一条的状态。
func SetTodosFromHistory(messages []agent.Message) {
	state, ok := lastTodoStateFromMessages(messages)
	if !ok {
		return
	}
	todoS.mu.Lock()
	defer todoS.mu.Unlock()
	todoS.todos = append([]PiTodo(nil), state.Todos...)
	if state.Next > 1 {
		todoS.next = state.Next
	}
}

// lastTodoStateFromMessages 返回历史中最后一个 todo 工具结果的状态。
func lastTodoStateFromMessages(messages []agent.Message) (piState, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != agent.RoleUser || m.Kind != agent.KindToolResult {
			continue
		}
		content := m.Content
		if len(content) < len(todoResultPrefix) || content[:len(todoResultPrefix)] != todoResultPrefix {
			continue
		}
		inner := content[len(todoResultPrefix):]
		var tr struct {
			ToolName string `json:"tool_name"`
			Output   string `json:"output"`
		}
		if err := json.Unmarshal([]byte(inner), &tr); err != nil {
			continue
		}
		if tr.ToolName != "todo" {
			continue
		}
		if s, ok := extractTodoState(tr.Output); ok {
			return s, true
		}
	}
	return piState{}, false
}

// PiTodoList 返回当前待办（供 /todos 命令与测试）。
func PiTodoList() []PiTodo {
	return todoS.snapshot().Todos
}

// PiTodoRender 渲染待办清单为人类可读文本（TUI /todos 用）。
func RenderPiTodos() string {
	todos := PiTodoList()
	if len(todos) == 0 {
		return "（暂无待办）"
	}
	var sb strings.Builder
	for _, t := range todos {
		mark := "[ ]"
		if t.Done {
			mark = "[x]"
		}
		fmt.Fprintf(&sb, "%s #%d: %s\n", mark, t.ID, t.Text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ============================================================================
// 工具定义
// ============================================================================

// toolTodo 返回 todo 工具（pi 式动作 API）。
func toolTodo() plugins.Tool {
	return todoTool()
}

// TodoToolForTest 供测试/命令层获取 todo 工具实例。
func TodoToolForTest() plugins.Tool {
	return todoTool()
}

func todoTool() plugins.Tool {
	return plugins.Tool{
		Name: "todo",
		Description: "Manage a persistent todo list across the session. " +
			"Actions: list, add (requires text), toggle (requires id), clear. " +
			"Args example: {\"action\":\"add\",\"text\":\"write tests\"} or {\"action\":\"toggle\",\"id\":1} or {\"action\":\"list\"} or {\"action\":\"clear\"}. " +
			"State survives session restarts and is shown by the /todos command.",
		Run: func(args string) (string, error) {
			var p struct {
				Action string `json:"action"`
				Text   string `json:"text"`
				ID     int    `json:"id"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("todo: args must be JSON: {\"action\":\"list|add|toggle|clear\", ...}")
			}
			todoS.mu.Lock()
			defer todoS.mu.Unlock()

			switch p.Action {
			case "list":
				if len(todoS.todos) == 0 {
					return "No todos", nil
				}
				lines := make([]string, 0, len(todoS.todos))
				for _, t := range todoS.todos {
					mark := "[ ]"
					if t.Done {
						mark = "[x]"
					}
					lines = append(lines, fmt.Sprintf("%s #%d: %s", mark, t.ID, t.Text))
				}
				return encodeTodoState(strings.Join(lines, "\n")), nil

			case "add":
				if strings.TrimSpace(p.Text) == "" {
					return "Error: text required for add", nil
				}
				t := PiTodo{ID: todoS.next, Text: p.Text}
				todoS.next++
				todoS.todos = append(todoS.todos, t)
				return encodeTodoState(fmt.Sprintf("Added todo #%d: %s", t.ID, t.Text)), nil

			case "toggle":
				idx := -1
				for i, t := range todoS.todos {
					if t.ID == p.ID {
						idx = i
						break
					}
				}
				if idx < 0 {
					return encodeTodoState(fmt.Sprintf("Todo #%d not found (use list to see ids)", p.ID)), nil
				}
				todoS.todos[idx].Done = !todoS.todos[idx].Done
				if todoS.todos[idx].Done {
					return encodeTodoState(fmt.Sprintf("Todo #%d completed", p.ID)), nil
				}
				return encodeTodoState(fmt.Sprintf("Todo #%d re-opened", p.ID)), nil

			case "clear":
				n := len(todoS.todos)
				todoS.todos = nil
				todoS.next = 1
				if n == 0 {
					return "No todos to clear", nil
				}
				return encodeTodoState(fmt.Sprintf("Cleared %d todos", n)), nil

			default:
				return "Unknown action: use list/add/toggle/clear", nil
			}
		},
	}
}
