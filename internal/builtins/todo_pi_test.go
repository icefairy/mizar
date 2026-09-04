package builtins

import (
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// resetTodoState 重置 pi 式 todo 全局态（测试隔离）。
func resetTodoState() {
	todoS.mu.Lock()
	defer todoS.mu.Unlock()
	todoS.todos = nil
	todoS.next = 1
}

// TestTodoAddList 动作式 API：add → list 往返。
func TestTodoAddList(t *testing.T) {
	resetTodoState()
	tool := toolTodo()

	out, err := tool.Run(`{"action":"add","text":"写文档"}`)
	if err != nil {
		t.Fatal(err)
	}
	res, ok := extractTodoState(out)
	if !ok {
		t.Fatalf("add result missing state marker: %q", out)
	}
	if len(res.Todos) != 1 || res.Todos[0].ID != 1 || res.Todos[0].Text != "写文档" {
		t.Fatalf("unexpected state: %+v", res)
	}

	out, err = tool.Run(`{"action":"add","text":"跑测试"}`)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = extractTodoState(out)
	if len(res.Todos) != 2 || res.Todos[1].ID != 2 {
		t.Fatalf("second add failed: %+v", res)
	}

	out, _ = tool.Run(`{"action":"list"}`)
	if !strings.Contains(out, "#1: 写文档") || !strings.Contains(out, "#2: 跑测试") {
		t.Fatalf("list output wrong: %q", out)
	}
}

// TestTodoToggle 切换完成状态。
func TestTodoToggle(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	tool.Run(`{"action":"add","text":"任务A"}`)
	out, _ := tool.Run(`{"action":"toggle","id":1}`)
	if !strings.Contains(out, "completed") {
		t.Fatalf("toggle to done: %q", out)
	}
	res, _ := extractTodoState(out)
	if !res.Todos[0].Done {
		t.Fatal("todo 1 should be done")
	}
	out, _ = tool.Run(`{"action":"toggle","id":1}`)
	if !strings.Contains(out, "re-opened") {
		t.Fatalf("toggle back: %q", out)
	}
	// 不存在的 id
	out, _ = tool.Run(`{"action":"toggle","id":99}`)
	if !strings.Contains(out, "not found") {
		t.Fatalf("toggle missing id: %q", out)
	}
}

// TestTodoClear 清空。
func TestTodoClear(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	tool.Run(`{"action":"add","text":"x"}`)
	out, _ := tool.Run(`{"action":"clear"}`)
	if !strings.Contains(out, "Cleared 1") {
		t.Fatalf("clear: %q", out)
	}
	res, _ := extractTodoState(out)
	if len(res.Todos) != 0 {
		t.Fatal("state should be empty after clear")
	}
}

// TestTodoBadInput 非法参数。
func TestTodoBadInput(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	_, err := tool.Run(`not-json`)
	if err == nil {
		t.Fatal("bad args should error")
	}
	out, _ := tool.Run(`{"action":"nope"}`)
	if !strings.Contains(out, "Unknown action") {
		t.Fatalf("unknown action: %q", out)
	}
	out, _ = tool.Run(`{"action":"add","text":""}`)
	if !strings.Contains(out, "text required") {
		t.Fatalf("empty text add: %q", out)
	}
}

// TestTodoMarkerRoundTrip 状态标记 → 文本 → 提取 无损往返。
func TestTodoMarkerRoundTrip(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	out, _ := tool.Run(`{"action":"add","text":"任务"}`)
	// 标记剥离后是纯读文本
	stripped := StripTodoMarker(out)
	if strings.Contains(stripped, "todo-state") {
		t.Fatalf("marker not stripped: %q", stripped)
	}
	if !strings.Contains(stripped, "Added todo") {
		t.Fatalf("readable text lost: %q", stripped)
	}
}

// TestTodoSetFromHistory 会话恢复回放：模拟落盘历史 → SetTodosFromHistory 重建。
func TestTodoSetFromHistory(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	// 演绎两次 add + 一次 toggle（模拟会话中发生）
	out1, _ := tool.Run(`{"action":"add","text":"任务1"}`)
	_ = out1
	out2, _ := tool.Run(`{"action":"add","text":"任务2"}`)
	_ = out2
	out3, _ := tool.Run(`{"action":"toggle","id":1}`)
	_ = out3

	// 将三次工具结果的 tool_result 消息构建为会话历史（与 loop.go 落盘格式一致）
	var msgs []agent.Message
	for _, te := range []struct{ tool, args, out string }{
		{"todo", `{"action":"add","text":"任务1"}`, out1},
		{"todo", `{"action":"add","text":"任务2"}`, out2},
		{"todo", `{"action":"toggle","id":1}`, out3},
	} {
		msgs = append(msgs, agent.ToolExchangeMessages(te.tool, te.args, "", te.out, nil)...)
	}

	// 清空内存态，模拟新进程/新会话开始
	resetTodoState()

	// 回放恢复
	SetTodosFromHistory(msgs)
	todos := PiTodoList()
	if len(todos) != 2 {
		t.Fatalf("rebuild todos = %d, want 2", len(todos))
	}
	if !todos[0].Done {
		t.Fatal("todo 1 should be done after replay")
	}
	if todos[1].Text != "任务2" {
		t.Fatalf("todo 2 text = %q", todos[1].Text)
	}
	// next id 应恢复为 3（下次 add 用 3）
	if todoS.next != 3 {
		t.Fatalf("next = %d, want 3", todoS.next)
	}
	// 恢复后仍可继续 add
	tool.Run(`{"action":"add","text":"任务3"}`)
	res, _ := extractTodoState(mustRun(t, tool, `{"action":"list"}`))
	if len(res.Todos) != 3 || res.Todos[2].ID != 3 {
		t.Fatalf("continue after replay: %+v", res)
	}
}

// mustRun 执行工具并返回输出（测试辅 turbine）。
func mustRun(t *testing.T, tool plugins.Tool, args string) string {
	t.Helper()
	out, err := tool.Run(args)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStripTodoMarker 多标记剥离（re-open 场景）。
func TestStripTodoMarker(t *testing.T) {
	in := "结果\n<!--todo-state:{\"a\":1}-->\n<!--todo-state:{\"b\":2}-->"
	out := StripTodoMarker(in)
	if strings.Contains(out, "todo-state") {
		t.Fatalf("marker leaked: %q", out)
	}
	if !strings.Contains(out, "结果") {
		t.Fatalf("content lost: %q", out)
	}
}
