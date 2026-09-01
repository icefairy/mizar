package main

import (
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/builtins"
)

// TestTodosCommandWithTool todo 工具与 /todos 命令联动：
// 工具 add 后 /todos 应显示；toggle 后 /todos 应反映完成状态。
func TestTodosCommandWithTool(t *testing.T) {
	// 构造最小 agent（Commands 注册即可用）
	a := &agent.Agent{}
	a.Commands = agent.NewCommandRegistry()
	a.Commands.Register(agent.Command{
		Name: "todos",
		Run:  func(args string) (string, error) { return builtins.RenderPiTodos(), nil },
	})
	// add → /todos 显示
	builtins.TodoToolForTest().Run(`{"action":"add","text":"集成任务"}`)

	if handled, out, err := a.Commands.Dispatch("/todos"); !handled || err != nil || !strings.Contains(out, "集成任务") {
		t.Fatalf("/todos after add: handled=%v out=%q err=%v", handled, out, err)
	}
	// toggle → 完成标记
	builtins.TodoToolForTest().Run(`{"action":"toggle","id":1}`)
	if _, out, _ := a.Commands.Dispatch("/todos"); !strings.Contains(out, "[x]") {
		t.Fatalf("/todos after toggle: %q", out)
	}
}
