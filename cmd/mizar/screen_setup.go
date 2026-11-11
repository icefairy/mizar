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
	m.scr = wrapped
	m.app.SetScreen(wrapped)
	// 鼠标上报：跟随运行期开关（/mouse、MIZAR_MOUSE、config.mouse，见 platform_input.go）。
	if MouseEnabled() {
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
		// 终端缩放（含首帧）：状态栏行数要按新宽度重算，否则窗口变窄后
		// 第二行仍按旧宽度只占 1 行，内容被截断。tview 在 EventResize 后会重画，
		// 这里借重画时机做一次便宜的比较（宽度没变则立即返回）。
		if w, _ := screen.Size(); w > 0 {
			if w != m.statusWidth && m.statusRaw != "" {
				m.syncStatusBarHeight(m.statusRaw)
			}
			if w != m.projWidth && m.projRaw != "" {
				m.syncBarHeight(m.projBar, m.projRaw, &m.projRows, &m.projWidth, &m.projRaw)
			}
		}
		return m.pasteBurstActive()
	})
	return nil
}
