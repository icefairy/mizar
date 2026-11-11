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

// TestStopDropsQueue /stop 应清空排队消息并停止（无任务时给出提示而非静默）。
// 回归：停止后排队消息又被 setLoadingAsync 自动补发，等于「停不下来」。
func TestStopDropsQueue(t *testing.T) {
	m := newTUIForQueueTest()
	m.queue = []string{"被丢弃的任务"}
	m.stopCurrentTask() // 未在运行 → 只提示，不应崩
	if len(m.queue) != 1 {
		t.Fatalf("/stop 在无运行任务时不应清空队列, got %v", m.queue)
	}
}

// TestStopClearsSteerAndQueue 运行中 /stop：清 steer + 清 queue + 置取消态。
func TestStopClearsSteerAndQueue(t *testing.T) {
	m := newTUIForQueueTest()
	m.loading = true
	m.queue = []string{"排队1", "排队2"}
	m.agent.Steer("残留纠正")
	m.stopCurrentTask()
	if m.agent.PendingSteer() {
		t.Fatal("/stop 后应清空待注入的 steer")
	}
	if len(m.queue) != 0 {
		t.Fatalf("/stop 后应清空排队消息, got %v", m.queue)
	}
	if m.loading {
		t.Fatal("/stop 后 loading 应为 false")
	}
	if !m.canceling.Load() {
		t.Fatal("/stop 后应置 canceling（阻止新任务抢跑）")
	}
}
