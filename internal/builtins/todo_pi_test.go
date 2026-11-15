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

// ============================================================================
// blocked_by 依赖：C-2（fan-out 的串行依赖承载）
// ============================================================================

// add 带 blocked_by → list 显示等待标记 → toggle 前置自动解锁后继。
func TestTodoBlockedBy(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	tool.Run(`{"action":"add","text":"先构建"}`)
	out := mustRun(t, tool, `{"action":"add","text":"再跑测试","blocked_by":[1]}`)
	if !strings.Contains(out, "等待 #1") {
		t.Fatalf("add 应提示依赖：out=%q", out)
	}
	res, _ := extractTodoState(out)
	if len(res.Todos[1].BlockedBy) != 1 || res.Todos[1].BlockedBy[0] != 1 {
		t.Fatalf("blocked_by 没落进状态: %+v", res.Todos[1])
	}
	// list：未完成前置显示等待标记
	out = mustRun(t, tool, `{"action":"list"}`)
	if !strings.Contains(out, "（等待 #1）") {
		t.Fatalf("list 应显示等待标记: %q", out)
	}
	// toggle 前置 → 自动解锁后继
	out = mustRun(t, tool, `{"action":"toggle","id":1}`)
	if !strings.Contains(out, "completed") || !strings.Contains(out, "已解锁 #2") {
		t.Fatalf("toggle 前置应自动解锁后继: %q", out)
	}
	// 后继就绪后 list 不再有等待标记
	out = mustRun(t, tool, `{"action":"list"}`)
	if strings.Contains(out, "（等待") {
		t.Fatalf("解锁后 list 不应再有等待标记: %q", out)
	}
	// re-open 前置 → 后继恢复等待（就绪状态是派生的）
	mustRun(t, tool, `{"action":"toggle","id":1}`)
	out = mustRun(t, tool, `{"action":"list"}`)
	if !strings.Contains(out, "（等待 #1）") {
		t.Fatalf("re-open 前置后后继应恢复等待: %q", out)
	}
}

// blocked_by 引用不存在的 id 应拒收（模型编错序号时给明确反馈）。
func TestTodoBlockedByUnknownRef(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	mustRun(t, tool, `{"action":"add","text":"A"}`)
	out := mustRun(t, tool, `{"action":"add","text":"B","blocked_by":[99]}`)
	if !strings.Contains(out, "unknown todo") {
		t.Fatalf("未知前置应报错: %q", out)
	}
	res, _ := extractTodoState(out)
	if len(res.Todos) != 1 {
		t.Fatal("被拒收的 todo 不应进清单")
	}
	// 重复/自身引用被剔除，不报错
	out = mustRun(t, tool, `{"action":"add","text":"C","blocked_by":[1,1,2]}`)
	res, _ = extractTodoState(out)
	if len(res.Todos) != 2 || len(res.Todos[1].BlockedBy) != 1 {
		t.Fatalf("重复引用应去重: %+v", res)
	}
}

// 跳序完成：前置未完成时 toggle 给提醒但不阻止；多链解锁只报就绪的后继。
func TestTodoToggleSkipOrderWarns(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	mustRun(t, tool, `{"action":"add","text":"A"}`)
	mustRun(t, tool, `{"action":"add","text":"B","blocked_by":[1]}`)
	mustRun(t, tool, `{"action":"add","text":"C","blocked_by":[1]}`)
	out := mustRun(t, tool, `{"action":"toggle","id":1}`)
	if !strings.Contains(out, "已解锁 #2 #3") {
		t.Fatalf("两个后继应同时解锁: %q", out)
	}
	// 没有依赖的完成不该出现解锁提示
	resetTodoState()
	mustRun(t, tool, `{"action":"add","text":"独立任务"}`)
	out = mustRun(t, tool, `{"action":"toggle","id":1}`)
	if strings.Contains(out, "已解锁") || strings.Contains(out, "前置") {
		t.Fatalf("无依赖完成不应有解锁/前置提示: %q", out)
	}
}

// blocked_by 随状态 JSON 往返，会话回放后依赖关系保留。
func TestTodoBlockedByReplay(t *testing.T) {
	resetTodoState()
	tool := toolTodo()
	out1 := mustRun(t, tool, `{"action":"add","text":"A"}`)
	out2 := mustRun(t, tool, `{"action":"add","text":"B","blocked_by":[1]}`)
	var msgs []agent.Message
	for _, e := range []struct{ out string }{{out1}, {out2}} {
		msgs = append(msgs, agent.ToolExchangeMessages("todo", "", "", e.out, nil)...)
	}
	resetTodoState()
	SetTodosFromHistory(msgs)
	todos := PiTodoList()
	if len(todos) != 2 || len(todos[1].BlockedBy) != 1 || todos[1].BlockedBy[0] != 1 {
		t.Fatalf("回放后依赖丢失: %+v", todos)
	}
}

func TestRenderForInjectionPendingOnly(t *testing.T) {
	todoS.mu.Lock()
	todoS.todos = []PiTodo{
		{ID: 1, Text: "已完成的事", Done: true},
		{ID: 2, Text: "未完成 A"},
		{ID: 3, Text: "未完成 B", BlockedBy: []int{2}},
	}
	todoS.next = 4
	todoS.mu.Unlock()
	defer func() {
		todoS.mu.Lock()
		todoS.todos = nil
		todoS.next = 1
		todoS.mu.Unlock()
	}()

	out := RenderForInjection()
	if out == "" {
		t.Fatal("有未完成项时应返回注入段")
	}
	if strings.Contains(out, "已完成的事") {
		t.Errorf("完成项不应注入（防重做）:\n%s", out)
	}
	if !strings.Contains(out, "未完成 A") || !strings.Contains(out, "未完成 B") {
		t.Errorf("未完成项应注入:\n%s", out)
	}
	if !strings.Contains(out, "等待 #2") {
		t.Errorf("阻塞关系应渲染:\n%s", out)
	}
	if !strings.Contains(out, "压缩后重注入") {
		t.Errorf("应点明来源:\n%s", out)
	}
}

func TestRenderForInjectionEmpty(t *testing.T) {
	todoS.mu.Lock()
	todoS.todos = []PiTodo{{ID: 1, Text: "done", Done: true}}
	todoS.mu.Unlock()
	defer func() {
		todoS.mu.Lock()
		todoS.todos = nil
		todoS.mu.Unlock()
	}()
	if out := RenderForInjection(); out != "" {
		t.Errorf("全部完成时应返回空（无需注入），got %q", out)
	}
}

func TestRenderForInjectionNoTodos(t *testing.T) {
	todoS.mu.Lock()
	todoS.todos = nil
	todoS.mu.Unlock()
	if out := RenderForInjection(); out != "" {
		t.Errorf("无待办应返回空，got %q", out)
	}
}
