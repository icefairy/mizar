package main

import (
	"strings"
	"testing"

	"github.com/rivo/tview"

	"mizar/internal/agent"
)

// 构造一个未跑 Run 的 tuiModel（只需 flex/textView/queueView/inputField/statusBar/app）
func newTUIForQueueTest() *tuiModel {
	a := &agent.Agent{}
	m := &tuiModel{
		agent:     a,
		lines:     []chatLine{{role: "system", content: banner()}},
		userColor: "white",
		aiColor:   "green",
	}
	m.textView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWordWrap(true)
	m.queueView = tview.NewTextView().SetDynamicColors(true)
	m.autoComplete = tview.NewTextView().SetDynamicColors(true)
	m.inputField = tview.NewTextArea()
	m.statusBar = tview.NewTextView().SetDynamicColors(true)
	m.app = tview.NewApplication()
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.queueView, 0, 0, false).
		AddItem(m.autoComplete, 1, 0, false).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)
	return m
}

// TestQueueRenderVisible 排队后 queueView 应有可见高度且文本含标题。
// 回归：旧代码 AddItem(queueView, 0, 0) 恒为 0 高度，排队消息永远不可见。
func TestQueueRenderVisible(t *testing.T) {
	m := newTUIForQueueTest()
	m.queue = []string{"任务一", "任务二"}
	m.renderQueue()
	if !m.queueVisible {
		t.Fatal("queue should be visible after renderQueue")
	}
	// queueView 应仍注册在 flex 中（ResizeItem 只改尺寸不增删项）
	if m.flex.GetItemCount() != 5 {
		t.Fatalf("flex item count = %d, want 5", m.flex.GetItemCount())
	}
	txt := m.queueView.GetText(true)
	if !strings.Contains(txt, "排队") || !strings.Contains(txt, "任务一") {
		t.Fatalf("queueView text missing header/item: %q", txt)
	}
}

// TestQueueRenderEmpty 队列空时：queueView 收回 0 高度隐藏。
func TestQueueRenderEmpty(t *testing.T) {
	m := newTUIForQueueTest()
	m.queue = nil
	m.renderQueue()
	if m.queueVisible {
		t.Fatal("queue should be hidden when empty")
	}
}

// TestQueueRenderThenClear 先排队后清空：状态正确切换（可见→隐藏）。
func TestQueueRenderThenClear(t *testing.T) {
	m := newTUIForQueueTest()
	m.queue = []string{"任务一"}
	m.renderQueue()
	if !m.queueVisible {
		t.Fatal("queue should be visible")
	}
	m.queue = nil
	m.renderQueue()
	if m.queueVisible {
		t.Fatal("queue should be hidden after clear")
	}
}
