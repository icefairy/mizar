package main

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// =============================================================================
// 平台输入适配（Windows Terminal / ConPTY 相关）
//
// 背景：Windows 上 tcell 走 ConsoleScreen（ReadConsoleInputW），鼠标上报需要
// conhost 向宿主终端发 DECSET 鼠标模式请求。但 ConPTY 会无条件开启
// win32-input-mode，宿主终端（Windows Terminal）便用 win32 序列编码鼠标事件，
// ConPTY 解析时存在已知的双重编码缺陷（microsoft/terminal#15083、#16343），
// 鼠标移动/滚轮被还原成普通字符流灌进控制台输入缓冲。
//
// 后果：tcell 把它们当 KeyRune 交给 tview，输入框出现 "[<35;66;61M..." 之类乱码；
// 且序列以 ESC 开头，会被误判成用户按下 ESC → 无辜触发"任务已取消"。
//
// 对策（方案 A）：Windows 上默认不启用鼠标上报，整条链路断开。复制粘贴改用
// 终端原生能力（拖选 + Ctrl+Shift+C/V、右键菜单），键盘侧仍可用 Alt+L / Alt+C。
// 需要鼠标的用户可设 MIZAR_MOUSE=1 强行开启。
// =============================================================================

// shouldEnableMouseFor 判断是否启用鼠标上报（getenv 便于测试注入）。
func shouldEnableMouseFor(goos string, getenv func(string) string) bool {
	switch getenv("MIZAR_MOUSE") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	// Windows 默认关闭：ConPTY 下鼠标事件会双重编码成字符泄漏（见文件头说明）
	return goos != "windows"
}

// shouldEnableMouse 生产环境入口。
func shouldEnableMouse() bool {
	return shouldEnableMouseFor(runtime.GOOS, os.Getenv)
}

// pasteGuardEnabledFor 是否启用粘贴换行防护（getenv 便于测试注入）。
func pasteGuardEnabledFor(getenv func(string) string) bool {
	switch getenv("MIZAR_PASTE_GUARD") {
	case "0", "false", "off":
		return false
	}
	return true
}

// pasteNewlineWindow 粘贴突发的判定窗口：与上一个字符事件间隔小于该值，
// 则认为 Enter 来自粘贴的换行符而非用户主动提交。
//
// 依据：tview 的 EnablePaste 只在 Unix terminfo screen 下有效（tcell 在
// Windows 上 EnablePaste 是空实现），粘贴多行文本时换行会变成裸 Enter，
// 导致半截内容被提交。人手打字两次按键间隔通常 ≥30ms，粘贴则是毫秒级。
const pasteNewlineWindow = 8 * time.Millisecond

// isPasteNewline 判断 now 时刻到达的 Enter 是否应视为粘贴换行。
func isPasteNewline(now, last time.Time) bool {
	if last.IsZero() {
		return false
	}
	d := now.Sub(last)
	// d < 0：时钟回拨等异常，不当作粘贴，避免吞掉用户提交
	return d >= 0 && d < pasteNewlineWindow
}

// normalizePastedNewlines 统一换行符，避免 \r 残留进输入框。
func normalizePastedNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
