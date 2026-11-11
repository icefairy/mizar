package main

// 鼠标拖选复制（对齐「别的程序」的直觉：不按 Shift，直接拖动选中再松开即复制）。
//
// 为什么必须自己实现：一旦开启鼠标上报（EnableMouse），终端就把鼠标事件全部交给
// 程序，终端自己不再做文本选择——所以此前只能「Shift+拖拽」把事件让回终端。
// 这里接管左键按下/移动/抬起，自己维护选区、自己反色高亮、松开时取文本入剪贴板。
//
// 实现要点（都是踩过的坑）：
//   - 选区用【屏幕坐标】而不是聊天行号/字符偏移：tview 的折行是 uniseg 单词边界 +
//     CJK 双宽，自己再算一份必然对不上（差一个宽字符就少选一个字）。直接按屏幕
//     单元格取内容与实际显示完全一致。
//   - 高亮用【AfterDrawFunc 反色】而不是重排 TextView：流式输出每 60ms 全量重绘，
//     把选中态塞进渲染文本会被反复冲掉；画完再统一反色最省事，也不干扰滚动逻辑。
//   - Shift+拖拽不接管：终端自己在鼠标上报模式下用 Shift 作为逃生通道，
//     即使事件仍传到这里也直接放行，保证「终端原生选择」这条退路永远可用。
//   - 只读屏幕缓冲区、不修改任何聊天数据结构：拖选期间不会触发重排/滚动，
//     用户不会看到聊天区抖动。

import (
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// chatSelection 拖选状态（屏幕坐标；ax/ay 为锚点，cx/cy 为当前拖动点）。
// scrollRow 记录按下时的聊天区滚动行：拖动期间若有新内容（流式/bash 输出）
// 让内容上移，选区要跟着平移，否则复制到的是「同一屏幕位置的新文字」。
type chatSelection struct {
	active    bool
	ax, ay    int
	cx, cy    int
	scrollRow int
}

// beginSelection 以 (x,y) 为锚点开始新的拖选（同时清掉旧选区）。
func (m *tuiModel) beginSelection(x, y int) {
	row := 0
	if m.textView != nil {
		row, _ = m.textView.GetScrollOffset()
	}
	m.sel = chatSelection{active: true, ax: x, ay: y, cx: x, cy: y, scrollRow: row}
	m.selMoved = false
}

// updateSelection 更新拖动终点；返回 true 表示位置确实变化（需要重画）。
func (m *tuiModel) updateSelection(x, y int) bool {
	if !m.sel.active {
		return false
	}
	if m.sel.cx == x && m.sel.cy == y {
		return false
	}
	m.sel.cx, m.sel.cy = x, y
	m.selMoved = true
	return true
}

// clearSelection 清除选区。
func (m *tuiModel) clearSelection() bool {
	if !m.sel.active {
		return false
	}
	m.sel = chatSelection{}
	m.selMoved = false
	return true
}

// hasSelection 当前是否有可见选区。
func (m *tuiModel) hasSelection() bool {
	return m.sel.active
}

// selectionRect 返回规范化（左上/右下）并裁剪到聊天区内的选区矩形。
// 裁剪是必须的：拖动时指针会跑到输入框/状态栏上，不裁剪就会把状态栏文字也复制进去。
func (m *tuiModel) selectionRect() (x0, y0, x1, y1 int, ok bool) {
	if m.textView == nil || !m.sel.active {
		return 0, 0, 0, 0, false
	}
	rx, ry, rw, rh := m.textView.GetInnerRect()
	if rw <= 0 || rh <= 0 {
		return 0, 0, 0, 0, false
	}
	clamp := func(x, y int) (int, int) {
		if x < rx {
			x = rx
		}
		if x > rx+rw-1 {
			x = rx + rw - 1
		}
		if y < ry {
			y = ry
		}
		if y > ry+rh-1 {
			y = ry + rh - 1
		}
		return x, y
	}
	// 内容滚动补偿：内容上移 delta 行时，同一段文字在屏幕上的 y 也上移 delta。
	delta := 0
	if m.textView != nil {
		if row, _ := m.textView.GetScrollOffset(); row != m.sel.scrollRow {
			delta = m.sel.scrollRow - row
		}
	}
	ax, ay := clamp(m.sel.ax, m.sel.ay+delta)
	cx, cy := clamp(m.sel.cx, m.sel.cy+delta)
	if ay > cy || (ay == cy && ax > cx) { // 反向拖（从右下往左上）
		ax, ay, cx, cy = cx, cy, ax, ay
	}
	return ax, ay, cx, cy, true
}

// drawSelectionHighlight 在选区上叠加反色（供 AfterDrawFunc 调用，每帧重画）。
func (m *tuiModel) drawSelectionHighlight(screen tcell.Screen) {
	if screen == nil || !m.sel.active {
		return
	}
	x0, y0, x1, y1, ok := m.selectionRect()
	if !ok {
		return
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			r, comb, style, w := screen.GetContent(x, y)
			if r == 0 || w <= 0 {
				continue
			}
			screen.SetContent(x, y, r, comb, style.Reverse(true))
			if w > 1 {
				x++ // 宽字符占两格，跳过延续格
			}
		}
	}
}

// selectionText 按屏幕矩形取文本（等价于终端拖选复制）。
//
// 规则与终端一致：逐行取字符、行尾空格去掉、空行保留为换行，末行多余空行不追加。
func selectionText(screen tcell.Screen, x0, y0, x1, y1 int) string {
	if screen == nil || x1 < x0 || y1 < y0 {
		return ""
	}
	var rows []string
	for y := y0; y <= y1; y++ {
		var sb strings.Builder
		for x := x0; x <= x1; x++ {
			r, comb, _, w := screen.GetContent(x, y)
			if r == 0 || w <= 0 {
				continue
			}
			sb.WriteRune(r)
			if len(comb) > 0 {
				sb.WriteString(string(comb))
			}
			if w > 1 {
				x++ // 宽字符延续格不再取
			}
		}
		rows = append(rows, strings.TrimRight(sb.String(), " "))
	}
	// 去掉尾部空行（拖到最后一行之后的空白区域时常见）
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n")
}

// finishSelection 结束拖选：有内容则复制到剪贴板并提示，然后清除选区。
// 纯点击（没移动）不复制，只清除旧选区——与常见 GUI 行为一致。
func (m *tuiModel) finishSelection() {
	if !m.sel.active {
		return
	}
	if !m.selMoved {
		m.clearSelection()
		return
	}
	x0, y0, x1, y1, ok := m.selectionRect()
	if !ok {
		m.clearSelection()
		return
	}
	text := selectionText(m.screen(), x0, y0, x1, y1)
	m.clearSelection()
	if strings.TrimSpace(text) == "" {
		return
	}
	m.clip.set(text)
	lines := strings.Count(text, "\n") + 1
	chars := len([]rune(text))
	m.addChatLine(chatLine{
		role:    "err",
		content: copyNoticeText(lines, chars),
		ts:      time.Now(),
	})
}

// copyNoticeText 组装复制成功提示（单独成函数便于单测口径一致）。
func copyNoticeText(lines, chars int) string {
	if lines <= 1 {
		return "✓ 已复制 " + strconv.Itoa(chars) + " 个字符到剪贴板（Alt+P 或右键可在输入框粘贴）"
	}
	return "✓ 已复制 " + strconv.Itoa(lines) + " 行 / " + strconv.Itoa(chars) + " 个字符到剪贴板（Alt+P 或右键可粘贴）"
}

// screen 返回当前屏幕（可能为 nil：单测/未装配屏幕时）。
func (m *tuiModel) screen() tcell.Screen {
	if m.scr == nil {
		return nil
	}
	return m.scr
}

// handleLeftMouse 处理左键按下/抬起/单击/双击。
//
// 双击 = 选整行（常见 GUI 直觉）；单击在按下时先清旧选区，抬起时若无拖动则视为
// 「点空白取消选区」，不复制任何东西。
func (m *tuiModel) handleLeftMouse(action tview.MouseAction, event *tcell.EventMouse) {
	if event == nil {
		return
	}
	x, y := event.Position()
	inside := m.inChatArea(x, y)
	switch action {
	case tview.MouseLeftDown:
		if !inside {
			m.clearSelection() // 点击聊天区外＝取消选区（重画由 tview 负责）
			return
		}
		m.beginSelection(x, y)
	case tview.MouseLeftDoubleClick:
		if !inside {
			return
		}
		m.selectWholeLine(y)
		m.finishSelection()
	case tview.MouseLeftUp:
		if m.hasSelection() {
			m.finishSelection()
		}
	}
}

// inChatArea 判断屏幕坐标是否落在聊天区内。
func (m *tuiModel) inChatArea(x, y int) bool {
	if m.textView == nil {
		return false
	}
	rx, ry, rw, rh := m.textView.GetInnerRect()
	return x >= rx && x < rx+rw && y >= ry && y < ry+rh
}

// selectWholeLine 双击整行选择：以聊天区左边界到右边界为选区（单行）。
func (m *tuiModel) selectWholeLine(y int) {
	if m.textView == nil {
		return
	}
	rx, ry, rw, rh := m.textView.GetInnerRect()
	if rw <= 0 || y < ry || y >= ry+rh {
		return
	}
	row := 0
	if m.textView != nil {
		row, _ = m.textView.GetScrollOffset()
	}
	m.sel = chatSelection{active: true, ax: rx, ay: y, cx: rx + rw - 1, cy: y, scrollRow: row}
	m.selMoved = true
}

// redrawSelection 无需显式重画：这些调用都发生在 mouseCapture/InputCapture 里，
// 而它们是**在 tview 事件循环内**执行的——此时 QueueUpdateDraw 会往自己的
// updates channel 投递并等待事件循环消费，事件循环正卡在本函数里，直接死锁。
// 正确做法是不用管：只要 mouseCapture 返回 consumed=true，tview 会自己 draw，
// AfterDrawFunc 再叠加选区反色。
func (m *tuiModel) redrawSelection() {}

// escClearsSelection 供 InputCapture 使用：有选区时 Esc 只清选区，不取消任务。
// 返回 true 表示事件已被消费。
func (m *tuiModel) escClearsSelection() bool {
	if !m.hasSelection() {
		return false
	}
	m.clearSelection()
	return true
}

// autoScrollWhileDragging 拖动到聊天区上/下边界外时自动滚动，便于一次选长段落。
// 返回 true 表示发生了滚动（调用方需重画）。
func (m *tuiModel) autoScrollWhileDragging(y int) bool {
	if m.textView == nil || !m.sel.active {
		return false
	}
	_, ry, _, rh := m.textView.GetInnerRect()
	if rh <= 0 {
		return false
	}
	switch {
	case y < ry:
		m.scrollChat(-1)
		return true
	case y >= ry+rh:
		m.scrollChat(1)
		return true
	}
	return false
}
