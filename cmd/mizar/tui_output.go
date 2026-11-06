package main

// TUI 期间的「终端输出纪律」：任何绕过 tcell 直接写 stdout/stderr 的内容，
// 都会在屏幕上留下永远擦不掉的残留文字（tcell 以为那些格子还是它自己画的，
// 后续增量重绘不会覆盖）。
//
// 现象：屏幕（尤其右半边/上半区）突然多出几行字，之后无论怎么刷新都在。
// 典型来源：
//   - 标准库 log（插件 hook 报错、模型/供应商持久化失败、定时提醒……）→ stderr
//   - fmt.Printf（定时提醒横幅、统计周报……）→ stdout
//   - OSC 52 剪贴板序列（\x1b]52;c;…）→ stderr
//
// 对策：
//   1. 进入 TUI 后把标准 log 重定向到诊断文件（不丢信息，也不再污染屏幕）；
//   2. 需要展示给用户的消息（定时提醒）改走聊天区；
//   3. OSC 52 仅在非 TUI 模式写；TUI 下由内部缓冲 + 系统剪贴板（xclip/xsel/
//      Set-Clipboard）承担，屏幕一个字节都不碰；
//   4. 兜底：Ctrl+L / /redraw 强制全屏 Sync（把任何残留彻底重画掉）。

import (
	"io"
	"log"
	"os"
	"sync/atomic"

	"mizar/internal/diag"
)

// tuiActive 标记当前是否在 TUI（tcell 独占终端）模式下运行。
var tuiActive atomic.Bool

// logOutputBeforeTUI 保存进入 TUI 前的 log 输出目标，退出时恢复。
var logOutputBeforeTUI io.Writer = os.Stderr

// enableTUIOutputDiscipline 进入 TUI：接管所有会污染终端的输出通道。
func enableTUIOutputDiscipline() {
	if tuiActive.Swap(true) {
		return // 已接管（重复调用安全）
	}
	// 标准 log → 诊断日志（保留可追溯性，但不写终端）
	log.SetOutput(diagWriter{})
}

// disableTUIOutputDiscipline 退出 TUI：恢复标准 log 输出。
func disableTUIOutputDiscipline() {
	if !tuiActive.Swap(false) {
		return
	}
	log.SetOutput(logOutputBeforeTUI)
}

// tuiOutputActive 报告 TUI 是否在独占终端（供 OSC52 等直写路径判断）。
func tuiOutputActive() bool {
	return tuiActive.Load()
}

// diagWriter 把标准 log 的每一行转进诊断日志（tag=log），避免丢信息。
type diagWriter struct{}

func (diagWriter) Write(p []byte) (int, error) {
	msg := string(p)
	for len(msg) > 0 && (msg[len(msg)-1] == '\n' || msg[len(msg)-1] == '\r') {
		msg = msg[:len(msg)-1]
	}
	if msg != "" {
		diag.Logf("log", "%s", msg)
	}
	return len(p), nil
}

// tuiDeliverReminder 由 TUI 注册：定时提醒到期时把内容送进聊天区（而不是 Printf）。
// main.go 的 schedule 投递回调优先走它；非 TUI 模式保持 nil。
var tuiDeliverReminder func(prompt string)

// tuiRequestResync 由 TUI 注册：请求一次「全屏强制重画」（清掉任何残留文字）。
// 命令 /redraw 与 Ctrl+L 都走它；非 TUI 模式为 nil。
var tuiRequestResync func()
