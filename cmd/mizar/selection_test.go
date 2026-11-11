package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// 拖选复制必须与「终端原生拖选」结果一致：按屏幕矩形取字、行尾空格去掉、
// 多行用换行连接、尾部空行不追加。
func TestSelectionTextFromScreen(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(20, 5)

	rows := []string{"hello world", "第二行中文", "mixed 混合 OK", "", "last"}
	for y, r := range rows {
		x := 0
		for _, ch := range r {
			sim.SetContent(x, y, ch, nil, tcell.StyleDefault)
			if ch > 0x2E80 {
				x += 2 // 宽字符占两格
			} else {
				x++
			}
		}
	}
	for y := 0; y < 5; y++ {
		for x := 0; x < 20; x++ {
			if r, _, _, _ := sim.GetContent(x, y); r == 0 {
				sim.SetContent(x, y, ' ', nil, tcell.StyleDefault)
			}
		}
	}

	// 全选前 3 行第 0..9 列
	got := selectionText(sim, 0, 0, 9, 2)
	want := "hello worl\n第二行中文\nmixed 混合"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	// 拖到超出内容末尾（第 3 行空白）：尾部空行不得追加
	got = selectionText(sim, 0, 4, 19, 4)
	if got != "last" {
		t.Fatalf("尾部空行应去掉, got %q", got)
	}
}

// 选区矩形要裁剪到聊天区内，并且反向拖动（右下→左上）要能正确归一化。
func TestSelectionRectNormalizeAndClamp(t *testing.T) {
	m := newTUIForQueueTest()
	m.widthOverride, m.heightOverride = 60, 20
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)
	m.flex.SetRect(0, 0, 60, 20)

	rx, ry, rw, rh := m.textView.GetInnerRect()
	if rw <= 0 || rh <= 0 {
		t.Fatalf("聊天区矩形无效: %d,%d,%d,%d", rx, ry, rw, rh)
	}

	// 反向拖：从右下往左上
	m.beginSelection(rx+rw-1, ry+rh-1)
	m.updateSelection(rx, ry)
	x0, y0, x1, y1, ok := m.selectionRect()
	if !ok || x0 != rx || y0 != ry || x1 != rx+rw-1 || y1 != ry+rh-1 {
		t.Fatalf("反向拖应归一化: %d,%d,%d,%d", x0, y0, x1, y1)
	}

	// 拖到输入框/状态栏上：必须被裁剪回聊天区
	m.beginSelection(rx, ry)
	m.updateSelection(0, 19)
	_, _, _, y1, ok = m.selectionRect()
	if !ok || y1 > ry+rh-1 {
		t.Fatalf("选区应裁剪到聊天区内: y1=%d 上限=%d", y1, ry+rh-1)
	}
}

// 纯点击（未移动）不复制，只清除旧选区。
func TestClickWithoutDragDoesNotCopy(t *testing.T) {
	m := newTUIForQueueTest()
	m.clip = newClipboard() // newTUIForQueueTest 不建剪贴板
	m.flex.SetRect(0, 0, 60, 20)
	m.beginSelection(2, 3)
	if !m.hasSelection() {
		t.Fatal("按下后应有选区")
	}
	m.finishSelection() // 没有 updateSelection，selMoved=false
	if m.hasSelection() {
		t.Fatal("纯点击后选区应清除")
	}
	if m.clip.peek() != "" {
		t.Fatalf("纯点击不应复制: %q", m.clip.peek())
	}
}

// 拖选后松开：内容必须进剪贴板。
func TestDragSelectionCopies(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(40, 10)
	for x := 0; x < 40; x++ {
		for y := 0; y < 10; y++ {
			sim.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	putRunes(sim, 0, 2, "复制我abc")
	sim.Show()

	m := newTUIForQueueTest()
	m.clip = newClipboard()
	m.mcpStats = nil
	m.scr = &pasteSkipScreen{Screen: sim}
	m.textView.SetRect(0, 0, 40, 10) // 聊天区铺满整屏，便于坐标断言
	m.beginSelection(0, 2)
	m.updateSelection(8, 2)
	m.finishSelection()

	if got := m.clip.peek(); !strings.Contains(got, "复制我abc") {
		t.Fatalf("剪贴板内容不对: %q", got)
	}
	if m.hasSelection() {
		t.Fatal("复制后选区应清除")
	}
}

// 双击选中整行。
func TestDoubleClickSelectsWholeLine(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(20, 6)
	for x := 0; x < 20; x++ {
		for y := 0; y < 6; y++ {
			sim.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	putRunes(sim, 0, 1, "一行文本")
	sim.Show()

	m := newTUIForQueueTest()
	m.clip = newClipboard()
	m.scr = &pasteSkipScreen{Screen: sim}
	m.textView.SetRect(0, 0, 20, 6)
	m.handleLeftMouse(tview.MouseLeftDoubleClick, tcell.NewEventMouse(2, 1, tcell.ButtonPrimary, tcell.ModNone))

	if got := m.clip.peek(); !strings.Contains(got, "一行文本") {
		t.Fatalf("双击应整行复制: %q", got)
	}
}

// 复制提示文案：单行说字符数，多行说行数+字符数。
func TestCopyNoticeText(t *testing.T) {
	if got := copyNoticeText(1, 12); !strings.Contains(got, "12 个字符") || strings.Contains(got, "行") {
		t.Fatalf("单行提示: %q", got)
	}
	got := copyNoticeText(3, 40)
	if !strings.Contains(got, "3 行") || !strings.Contains(got, "40 个字符") {
		t.Fatalf("多行提示: %q", got)
	}
}

// putRunes 按显示宽度写入一串 rune（CJK 占两格），供屏幕断言用。
func putRunes(sim tcell.SimulationScreen, x, y int, text string) {
	for _, ch := range text {
		sim.SetContent(x, y, ch, nil, tcell.StyleDefault)
		if ch > 0x2E80 { // 粗略判定宽字符
			x += 2
		} else {
			x++
		}
	}
}

// 拖到聊天区边界外要自动滚动（一次选长段落不用松手再滚轮）。
func TestAutoScrollWhileDragging(t *testing.T) {
	m := newTUIForQueueTest()
	m.lines = nil
	for i := 0; i < 100; i++ {
		m.lines = append(m.lines, chatLine{role: "bot", content: strings.Repeat("行 ", 20), ts: now()})
	}
	m.flex.SetRect(0, 0, 60, 20)
	m.textView.SetRect(0, 0, 60, 10)
	m.textView.ScrollTo(50, 0)
	before, _ := m.textView.GetScrollOffset()

	m.beginSelection(2, 0) // 在聊天区顶部按下
	if !m.autoScrollWhileDragging(-1) {
		t.Fatal("拖出上边界应触发滚动")
	}
	after, _ := m.textView.GetScrollOffset()
	if after >= before {
		t.Fatalf("向上应滚到更早的行：before=%d after=%d", before, after)
	}
	if !m.userScrolledUp {
		t.Fatal("向上滚动后应标记用户已上滚（防止被自动拉回底部）")
	}
}

// Shift+拖拽不接管：保证终端原生选择这条退路始终可用。
func TestShiftDragNotCaptured(t *testing.T) {
	m := newTUIForQueueTest()
	m.textView.SetRect(0, 0, 60, 10)

	ev := tcell.NewEventMouse(2, 2, tcell.ButtonPrimary, tcell.ModShift)
	pass, action := m.mouseCapture(ev, tview.MouseLeftDown)
	if pass == nil {
		t.Fatal("Shift+左键应放行给终端原生选择")
	}
	if action != tview.MouseLeftDown {
		t.Fatalf("应保持原 action，got %v", action)
	}
	if m.hasSelection() {
		t.Fatal("Shift+拖拽不应进入自建选区")
	}

	// 不带 Shift：自己接管，进入拖选
	ev2 := tcell.NewEventMouse(2, 2, tcell.ButtonPrimary, tcell.ModNone)
	pass2, _ := m.mouseCapture(ev2, tview.MouseLeftDown)
	if pass2 != nil {
		t.Fatal("普通左键应被接管")
	}
	if !m.hasSelection() {
		t.Fatal("普通左键按下应开始拖选")
	}
}

// 选区反色必须作用在已画好的屏幕上（AfterDrawFunc 叠加），且宽字符只染首格。
func TestDrawSelectionHighlight(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(20, 5)
	for x := 0; x < 20; x++ {
		for y := 0; y < 5; y++ {
			sim.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	putRunes(sim, 0, 1, "中文ab")
	sim.Show()

	m := newTUIForQueueTest()
	m.scr = &pasteSkipScreen{Screen: sim}
	m.textView.SetRect(0, 0, 20, 5)
	m.beginSelection(0, 1)
	m.updateSelection(5, 1)
	m.drawSelectionHighlight(sim)

	_, _, style, _ := sim.GetContent(0, 1)
	if style != style.Reverse(true) {
		t.Fatalf("选区应反色，got %v", style)
	}
	_, _, outStyle, _ := sim.GetContent(10, 1)
	if outStyle != tcell.StyleDefault {
		t.Fatal("选区外的单元格不应被改动")
	}
}

// banner 必须说明新的复制方式（用户不会去读源码）。
func TestBannerMentionsDragCopy(t *testing.T) {
	b := banner()
	if !strings.Contains(b, "拖选") {
		t.Fatalf("banner 应说明鼠标拖选复制:\n%s", b)
	}
}

// 端到端：按下→拖动→松开 三个鼠标事件走完，内容进剪贴板。
//
// 关键回归点：这条路径曾在 MouseLeftUp 里死锁——早期用 app.QueueUpdateDraw 请求
// 重画，而 mouseCapture 本身就在 tview 事件循环内执行，投递后会等事件循环消费，
// 自己卡自己。现在要求整条路径「不阻塞」。
func TestMouseDragEndToEndNotBlocking(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(40, 8)
	for x := 0; x < 40; x++ {
		for y := 0; y < 8; y++ {
			sim.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}
	putRunes(sim, 0, 3, "abcdef")
	sim.Show()

	m := newTUIForQueueTest()
	m.clip = newClipboard()
	m.scr = &pasteSkipScreen{Screen: sim}
	m.textView.SetRect(0, 0, 40, 8)

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.mouseCapture(tcell.NewEventMouse(0, 3, tcell.ButtonPrimary, tcell.ModNone), tview.MouseLeftDown)
		m.mouseCapture(tcell.NewEventMouse(3, 3, tcell.ButtonPrimary, tcell.ModNone), tview.MouseMove)
		m.mouseCapture(tcell.NewEventMouse(3, 3, 0, tcell.ModNone), tview.MouseLeftUp)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("拖选流程阻塞（MouseCapture 内不要用 QueueUpdateDraw）")
	}
	if got := m.clip.peek(); got != "abcd" {
		t.Fatalf("复制内容不对: %q", got)
	}
	if m.hasSelection() {
		t.Fatal("松开后选区应清除")
	}
}

// 点击聊天区外应取消选区（而不是开始新选区）。
func TestClickOutsideChatClearsSelection(t *testing.T) {
	m := newTUIForQueueTest()
	m.clip = newClipboard()
	m.textView.SetRect(0, 0, 60, 10)
	m.beginSelection(2, 2)
	if !m.hasSelection() {
		t.Fatal("应有选区")
	}
	// (5, 50) 明显在聊天区之外
	m.mouseCapture(tcell.NewEventMouse(5, 50, tcell.ButtonPrimary, tcell.ModNone), tview.MouseLeftDown)
	if m.hasSelection() {
		t.Fatal("点击聊天区外应取消选区")
	}
}
