package main

// 屏幕装配：把 tview 的 Application 与「粘贴攒批期间抑制屏幕输出」的薄包装接起来。
//
// 为什么要自己做：tview 的 Application.SetScreen() 在 Run() 之前调用时只做
// screen.Init()，而 EnableMouse/EnablePaste 这些步骤在 Run() 里仅在「screen == nil」
// 分支执行——一旦我们在 Run() 之前装好 screen，那些初始化就被跳过了。
// 这里按 Run() 的同等语义补齐，保证行为不变。

import (
	"github.com/gdamore/tcell/v2"
)

// initScreen 创建真实屏幕（Windows 上为 ConsoleScreen），套上 pasteSkipScreen
// 后交给 Application，并补齐 Run() 会做的初始化。
//
// 失败时返回错误交给调用方走默认路径（Run() 会自己 NewScreen 并报出同样的错）。
func (m *tuiModel) initScreen() error {
	scr, err := tcell.NewScreen()
	if err != nil || scr == nil {
		return err
	}
	// SetScreen（a.screen == nil 时）会替我们调用 screen.Init()。
	wrapped := &pasteSkipScreen{Screen: scr, skip: m.pasteBurstActive}
	m.app.SetScreen(wrapped)
	// 鼠标上报：Windows 默认关闭（见 platform_input.go），其余平台开启。
	if shouldEnableMouse() {
		wrapped.EnableMouse()
	} else {
		wrapped.DisableMouse()
	}
	// bracketed paste：Unix terminfo 下生效；Windows 是空实现（由 paste_burst.go 兜底）。
	wrapped.EnablePaste()

	// 攒批期间连「重画所有可见控件」也跳过（见 paste_burst.go）：tview 对每个被
	// 吞掉的按键事件都会走一次 a.draw() → root.Draw()。TextView 每次 Draw 都要
	// 重新排版全部可见行，粘贴几百字就是几百次重排。跳过绘制后每键只剩一次
	// 空 Clear（内存层面，微秒级），配合 Show 抑制，攒批几乎零成本。
	m.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		return m.pasteBurstActive()
	})
	return nil
}
