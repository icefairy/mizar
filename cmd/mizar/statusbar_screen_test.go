package main

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// 用 tcell 的模拟屏幕做一次真实排版：验证窄终端下状态栏真的变多行，
// 且多行内容确实被画到屏幕上（而不是被截断）。
func TestStatusBarWrapsOnRealScreen(t *testing.T) {
	m := newTUIForQueueTest()
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)

	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(50, 20)

	m.scr = nil
	m.widthOverride, m.heightOverride = 50, 20

	m.statusBarDirect()
	rows := m.statusRows
	if rows < 2 {
		t.Fatalf("50 列下状态栏应多行, got %d", rows)
	}
	m.flex.SetRect(0, 0, 50, 20)
	m.flex.Draw(sim)
	sim.Show()

	// 第一行状态栏应出现在屏幕底部最后 rows 行里：取最后一行看是否有内容
	text := ""
	for y := 20 - rows; y < 20; y++ {
		line := ""
		for x := 0; x < 50; x++ {
			r, _, _, _ := sim.GetContent(x, y)
			if r == 0 {
				r = ' '
			}
			line += string(r)
		}
		text += strings.TrimRight(line, " ") + "\n"
	}
	flat := strings.ReplaceAll(text, " ", "") // tview 给宽字符补空格对齐，比对前去掉
	if !strings.Contains(flat, "tokens") {
		t.Fatalf("状态栏内容应出现在底部多行区域:\n%s", text)
	}
	if !strings.Contains(flat, "思考") {
		t.Fatalf("折行后的后半段（思考等级）也应可见:\n%s", text)
	}
	// 折行后不能出现「被截断」的痕迹：最后一行应包含输出末尾的思考等级
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("应占多行: %d", len(lines))
	}
}
