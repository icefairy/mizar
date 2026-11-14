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
	"sort"
	"strconv"
	"strings"
	"sync"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// PiTodo 单个待办项。
type PiTodo struct {
	ID        int    `json:"id"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	BlockedBy []int  `json:"blocked_by,omitempty"` // 前置任务 id：全部完成后本项才算就绪
}

// piTodoState 全局待办状态（单进程单会话生命周期）。
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

// ============================================================================
// 依赖（blocked_by）：就绪判定 / 归一化 / 自动解锁
// ============================================================================

// todoBlocking 返回 t 尚未完成的前置 id（就绪时为空）。
// 前置不存在视为已消解（被 clear 掉的依赖不再阻塞）。Done 的 todo 不算等待。
func todoBlocking(t PiTodo, todos []PiTodo) (waiting []int) {
	if t.Done {
		return nil
	}
	for _, bid := range t.BlockedBy {
		found := false
		for _, o := range todos {
			if o.ID == bid {
				found = true
				if !o.Done {
					waiting = append(waiting, bid)
				}
				break
			}
		}
		_ = found // 前置缺失按已消解处理
	}
	return waiting
}

// normalizeBlockedBy 校验 add 的 blocked_by：剔除 0/负数/自身/重复，
// 引用不存在的 id 记入 unknown（调用方报错拒收）。
func normalizeBlockedBy(ids []int, selfID int, todos []PiTodo) (valid, unknown []int) {
	seen := map[int]bool{}
	for _, id := range ids {
		if id <= 0 || id == selfID || seen[id] {
			continue
		}
		found := false
		for _, t := range todos {
			if t.ID == id {
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, id)
			continue
		}
		seen[id] = true
		valid = append(valid, id)
	}
	sort.Ints(valid)
	sort.Ints(unknown)
	return valid, unknown
}

// newlyUnlocked 返回此前被 completedID 阻塞、现在前置全部完成的后继 id。
// 在 toggle 完成动作、状态已翻转之后调用。
func newlyUnlocked(completedID int, todos []PiTodo) (unlocked []int) {
	for _, t := range todos {
		if t.Done || !containsInt(t.BlockedBy, completedID) {
			continue
		}
		if len(todoBlocking(t, todos)) == 0 {
			unlocked = append(unlocked, t.ID)
		}
	}
	sort.Ints(unlocked)
	return unlocked
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// idsLabel 把 id 切片渲染成 "#1 #2" 形式（%v 会打印成 "[1 2]"，不像 todo id）。
func idsLabel(ids []int) string {
	parts := make([]string, len(ids))
	for i, v := range ids {
		parts[i] = "#" + strconv.Itoa(v)
	}
	return strings.Join(parts, " ")
}

// PiTodoRender 渲染待办清单为人类可读文本（TUI /todos 用）。
func RenderPiTodos() string {
	todos := PiTodoList()
	if len(todos) == 0 {
		return "（暂无待办）"
	}
	lines := make([]string, 0, len(todos))
	for _, t := range todos {
		lines = append(lines, formatTodoLine(t, todos))
	}
	return strings.Join(lines, "\n")
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
		Description: "Manage a persistent todo list across the session, with dependencies. " +
			"Actions: list, add (text, optional blocked_by: [ids] to order after existing todos), toggle (id), clear. " +
			"Args example: {\"action\":\"add\",\"text\":\"run tests\",\"blocked_by\":[1]} or {\"action\":\"toggle\",\"id\":1} or {\"action\":\"list\"} or {\"action\":\"clear\"}. " +
			"Completing a blocker automatically reports unlocked dependents; blocked todos show（等待 #n）in list. " +
			"Before delegate_task: record serial dependencies here, then fan out only the independent tasks. " +
			"State survives session restarts and is shown by the /todos command.",
		Run: func(args string) (string, error) {
			var p struct {
				Action    string `json:"action"`
				Text      string `json:"text"`
				ID        int    `json:"id"`
				BlockedBy []int  `json:"blocked_by"`
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
					lines = append(lines, formatTodoLine(t, todoS.todos))
				}
				return encodeTodoState(strings.Join(lines, "\n")), nil

			case "add":
				if strings.TrimSpace(p.Text) == "" {
					return "Error: text required for add", nil
				}
				blocked, unknown := normalizeBlockedBy(p.BlockedBy, todoS.next, todoS.todos)
				if len(unknown) > 0 {
					return encodeTodoState(fmt.Sprintf("Error: blocked_by references unknown todo(s) %s (use list to see ids)", idsLabel(unknown))), nil
				}
				t := PiTodo{ID: todoS.next, Text: p.Text, BlockedBy: blocked}
				todoS.next++
				todoS.todos = append(todoS.todos, t)
				msg := fmt.Sprintf("Added todo #%d: %s", t.ID, t.Text)
				if len(blocked) > 0 {
					msg += fmt.Sprintf("（等待 %s 完成后开始）", idsLabel(blocked))
				}
				return encodeTodoState(msg), nil

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
				if !todoS.todos[idx].Done {
					return encodeTodoState(fmt.Sprintf("Todo #%d re-opened", p.ID)), nil
				}
				// 完成路径：提示仍在阻塞中的前置（模型跳序时给个提醒），
				// 并自动解锁以此项为前置、且其余前置也已完成的后继。
				msg := fmt.Sprintf("Todo #%d completed", p.ID)
				if wait := todoBlocking(todoS.todos[idx], todoS.todos); len(wait) > 0 {
					msg += fmt.Sprintf(" ⚠ 其前置 %s 尚未完成，请核对顺序", idsLabel(wait))
				}
				if unlocked := newlyUnlocked(p.ID, todoS.todos); len(unlocked) > 0 {
					msg += fmt.Sprintf("；已解锁 %s", idsLabel(unlocked))
				}
				return encodeTodoState(msg), nil

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

// formatTodoLine 渲染单个 todo 行：完成标记 + 序号 + 文本 + 阻塞提示。
func formatTodoLine(t PiTodo, todos []PiTodo) string {
	mark := "[ ]"
	if t.Done {
		mark = "[x]"
	}
	line := fmt.Sprintf("%s #%d: %s", mark, t.ID, t.Text)
	if wait := todoBlocking(t, todos); len(wait) > 0 {
		line += fmt.Sprintf("（等待 %s）", idsLabel(wait))
	}
	return line
}
