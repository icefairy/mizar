package main

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// =============================================================================
// 平台输入适配（Windows Terminal / ConPTY 相关）
//
// 历史：旧版 tcell（v2.8.x）在 Windows 上走 ConsoleScreen + ReadConsoleInputW，
// 而 ConPTY 会无条件开启 win32-input-mode，宿主终端（Windows Terminal）遂把鼠标
// 事件编码成 win32 序列；老 ConPTY 解析存在双重编码缺陷（microsoft/terminal#15083、
// #16343），鼠标事件被还原成字符流灌进输入缓冲：输入框出现 "[<35;66;61M..." 乱码，
// 且序列以 ESC 开头 → 被误判成「取消任务」。因此当时 Windows 默认关闭鼠标上报。
//
// 现在（tcell ≥ v2.13）Windows 改用 terminfo 屏 + win32-input-mode：输入是 VT
// 字节流，X10/SGR 鼠标序列（CSI < … M）由 tcell 的 inputProcessor 正确还原为
// EventMouse；Windows Terminal 也会把鼠标作为标准 VT 序列回传。因此恢复默认开启
// 鼠标（滚轮/拖选/右键粘贴都能用），与 pi 等其它 agent 的体验一致。
//
// 保留的兜底（万一碰到会泄漏字符的老终端）：
//   - esc_guard.go 吞掉泄漏的鼠标序列，滚轮绝不误杀任务；
//   - esc_seq.go 把泄漏序列整段吞掉；
//   - Windows 的 Esc 取消默认两段式确认。
//
// 关闭鼠标（旧终端乱码 / 只想用键盘）：
//   - 运行期：/mouse off（立即生效并持久化到 config.json）
//   - 启动时：环境变量 MIZAR_MOUSE=0，或 config.json 的 "mouse": false
// =============================================================================

// shouldEnableMouseFor 判断默认是否启用鼠标上报（getenv 便于测试注入）。
// 现在各平台默认开启；显式环境变量优先。
func shouldEnableMouseFor(_ string, getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("MIZAR_MOUSE"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// shouldEnableMouse 生产环境默认值（只看环境变量与平台默认，不含 /mouse 运行期选择）。
func shouldEnableMouse() bool {
	return shouldEnableMouseFor(runtime.GOOS, os.Getenv)
}

// ---------------------------------------------------------------------------
// 运行期鼠标开关（/mouse 命令）
// ---------------------------------------------------------------------------

var (
	mouseMu       sync.Mutex
	mouseOn       = shouldEnableMouse()
	tuiSetMouseFn func(bool) error // TUI 注入：真正调用 Application.EnableMouse
)

// MouseEnabled 当前是否启用鼠标上报。
func MouseEnabled() bool {
	mouseMu.Lock()
	defer mouseMu.Unlock()
	return mouseOn
}

// SetMouseEnabled 切换鼠标上报：更新状态并调用 TUI 注入的钩子（无 TUI 时只记状态）。
func SetMouseEnabled(on bool) error {
	mouseMu.Lock()
	mouseOn = on
	hook := tuiSetMouseFn
	mouseMu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(on)
}

// SetMouseFromConfig 启动时按 config 的 mouse 字段恢复（TUI 尚未创建，仅记状态）。
func SetMouseFromConfig(v *bool) {
	if v == nil {
		return
	}
	mouseMu.Lock()
	mouseOn = *v
	mouseMu.Unlock()
}

// registerTUISetMouse 由 TUI 注册鼠标切换实现（nil 表示注销）。
func registerTUISetMouse(fn func(bool) error) {
	mouseMu.Lock()
	tuiSetMouseFn = fn
	mouseMu.Unlock()
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
// 依据：部分终端（尤其老 tcell 在 Windows 上）EnablePaste 不生效，粘贴多行文本
// 时换行会变成裸 Enter，导致半截内容被提交。人手打字两次按键间隔通常 ≥30ms，
// 粘贴则是毫秒级。
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
