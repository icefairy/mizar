package main

// 鼠标滚轮回归测试：内容区滚轮必须真的能滚，且滚动位置不能被随后的重绘拽回去。
//
// 事故：TextView 自带的 MouseHandler 处理滚轮时只改自己的 lineOffset，不通知
// 外部；userScrolledUp 因此保持 false，流式/看门狗每次 renderAllDirect 都会
// 「不跟随就 ScrollToEnd」把视图拉回底部 —— 用户感知为「滚轮完全不能用」。
// 修法：MouseCapture 里自己接管滚轮，统一走 scrollChat 维护 userScrolledUp。

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// newWheelTestApp 构造带 SimulationScreen 的模型并启动事件循环（滚轮走的是
// app 的 fireMouseActions → MouseCapture → flex，必须真跑事件循环才成立）。
func newWheelTestApp(t *testing.T) (*tuiModel, tcell.SimulationScreen, func()) {
	t.Helper()
	m := newScrollTestModel(200)
	sc := tcell.NewSimulationScreen("UTF-8")
	if err := sc.Init(); err != nil {
		t.Fatal(err)
	}
	sc.SetSize(100, 30)
	m.app.SetScreen(sc)
	m.app.SetRoot(m.flex, true)
	m.app.SetFocus(m.inputField)
	done := make(chan struct{})
	go func() { _ = m.app.Run(); close(done) }()
	time.Sleep(120 * time.Millisecond)
	return m, sc, func() { m.app.Stop(); <-done }
}

// TestWheelScrollsChatAndSurvivesRedraw 滚轮上滚后位置必须保留，
// 不能被一次全量重绘（流式/看门狗都会触发）拉回底部。
func TestWheelScrollsChatAndSurvivesRedraw(t *testing.T) {
	m, sc, stop := newWheelTestApp(t)
	defer stop()

	x, y, _, _ := m.textView.GetInnerRect()
	start, _ := m.textView.GetScrollOffset()

	for i := 0; i < 3; i++ {
		sc.PostEvent(tcell.NewEventMouse(x+1, y+1, tcell.WheelUp, tcell.ModNone))
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := m.textView.GetScrollOffset()
	if got >= start {
		t.Fatalf("滚轮上滚没有改变偏移：%d -> %d", start, got)
	}
	if !m.userScrolledUp {
		t.Fatal("滚轮上滚后 userScrolledUp 应为 true（暂停自动跟随）")
	}

	m.app.QueueUpdateDraw(func() { m.renderAllDirect() })
	time.Sleep(80 * time.Millisecond)
	if after, _ := m.textView.GetScrollOffset(); after != got {
		t.Fatalf("一次重绘后滚动位置被拽走：%d -> %d", got, after)
	}
}

// TestWheelDownRestoresFollow 滚轮下滚到底必须恢复自动跟随。
func TestWheelDownRestoresFollow(t *testing.T) {
	m, sc, stop := newWheelTestApp(t)
	defer stop()

	x, y, _, _ := m.textView.GetInnerRect()
	// 先上滚出去
	m.scrollChat(-1 << 20)
	m.app.ForceDraw()
	if !m.userScrolledUp {
		t.Fatal("前置：上滚后应处于暂停跟随")
	}
	// 一路滚到底（滚到恢复自动跟随为止；也应验证越过底部后夹紧不越界）
	for i := 0; i < 200 && m.userScrolledUp; i++ {
		sc.PostEvent(tcell.NewEventMouse(x+1, y+1, tcell.WheelDown, tcell.ModNone))
		time.Sleep(30 * time.Millisecond)
	}
	if m.userScrolledUp {
		t.Fatal("滚轮滚到底后应恢复自动跟随")
	}
	if row, total := m.textView.GetScrollOffset(); row+1 < total {
		t.Fatalf("滚到底后 offset = %d, total = %d", row, total)
	}
}

// TestWheelOutsideChatIgnored 指针不在聊天区时滚轮不应劫持（交回默认处理）。
func TestWheelOutsideChatIgnored(t *testing.T) {
	m, _, stop := newWheelTestApp(t)
	defer stop()

	before, _ := m.textView.GetScrollOffset()
	// 坐标放到屏幕最底部（输入框/状态栏区域）
	ev := tcell.NewEventMouse(1, 29, tcell.WheelUp, tcell.ModNone)
	_, action := m.app.GetMouseCapture()(ev, tview.MouseScrollUp)
	if action == tview.MouseScrollUp {
		t.Log("指针不在聊天区：未消费，符合预期")
	}
	if after, _ := m.textView.GetScrollOffset(); after != before {
		t.Fatalf("聊天区外滚轮改变了偏移：%d -> %d", before, after)
	}
}
