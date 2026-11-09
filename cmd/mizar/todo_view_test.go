package main

import (
	"strings"
	"testing"

	"github.com/rivo/tview"

	"mizar/internal/agent"
	"mizar/internal/builtins"
)

// newTUIForTodoTest 构造带 todoView 的 TUI（布局与实盘一致）。
func newTUIForTodoTest() *tuiModel {
	a := &agent.Agent{}
	m := &tuiModel{
		agent:     a,
		lines:     []chatLine{{role: "system", content: banner()}},
		userColor: "white",
		aiColor:   "green",
	}
	m.textView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	m.queueView = tview.NewTextView().SetDynamicColors(true)
	m.todoView = tview.NewTextView().SetDynamicColors(true)
	m.autoComplete = tview.NewTextView().SetDynamicColors(true)
	m.inputField = tview.NewTextArea()
	m.statusBar = tview.NewTextView().SetDynamicColors(true)
	m.app = tview.NewApplication()
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.queueView, 0, 0, false).
		AddItem(m.todoView, 0, 0, false).
		AddItem(m.autoComplete, 1, 0, false).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)
	return m
}

// TestTodoViewVisible 有待办时 todoView 显示在输入框上方且高度 > 0。
func TestTodoViewVisible(t *testing.T) {
	// 清空全局 todo 状态，避免其他测试残留
	builtins.TodoToolForTest().Run(`{"action":"clear"}`)
	tool := builtins.TodoToolForTest()
	tool.Run(`{"action":"add","text":"写测试"}`)
	tool.Run(`{"action":"add","text":"跑构建"}`)

	m := newTUIForTodoTest()
	m.renderTodos()
	if !m.todoVisible {
		t.Fatal("todoView should be visible with pending todos")
	}
	txt := m.todoView.GetText(true)
	if !strings.Contains(txt, "待办") || !strings.Contains(txt, "写测试") {
		t.Fatalf("todoView text missing header/item: %q", txt)
	}
	// flex 应仍包含 6 个项（ResizeItem 只改尺寸不增删）
	if m.flex.GetItemCount() != 6 {
		t.Fatalf("flex item count = %d, want 6", m.flex.GetItemCount())
	}
}

// TestTodoViewHiddenWhenAllDone 全部完成时 todoView 收回 0 高度隐藏。
func TestTodoViewHiddenWhenAllDone(t *testing.T) {
	builtins.TodoToolForTest().Run(`{"action":"clear"}`)
	tool := builtins.TodoToolForTest()
	tool.Run(`{"action":"add","text":"只此一项"}`)
	tool.Run(`{"action":"toggle","id":1}`)

	m := newTUIForTodoTest()
	m.renderTodos()
	if m.todoVisible {
		t.Fatal("todoView should be hidden when all todos done")
	}
	if m.todoView.GetText(true) != "" {
		t.Fatalf("todoView text should be empty, got %q", m.todoView.GetText(true))
	}
	builtins.TodoToolForTest().Run(`{"action":"clear"}`)
}
