//go:build windows

package main

// 控制台能力探测（Windows 专用）。
//
// 为什么需要：tcell 在 Windows 上只有「VT 序列」（ENABLE_VIRTUAL_TERMINAL_PROCESSING）
// 一条实现路径，没有老式 CHAR_INFO 回退。Windows 10 起默认可用；Windows 7/8 的
// conhost 不支持（除非换了 ConEmu / Windows Terminal 之类支持 VT 的宿主），
// 此时 tcell.NewScreen 返回 "failed to initialize: VT output not supported?"。
//
// 若不预检，用户看到的就是「窗口一闪 / 黑一下就没了」——体验上很像启动闪退。
// 这里提前探一次并给出明确指引，然后用经典模式继续跑（不会闪退）。
//
// 实现说明：SetConsoleMode / SetConsoleOutputCP 不在标准库 syscall 里，
// 为了不给项目引入新的直接依赖，这里直接用 kernel32 LazyDLL 调（全是 Win2k 起就有的 API）。

import (
	"syscall"
	"unsafe"
)

const (
	enableVirtualTerminalProcessing = 0x0004
	enableProcessedOutput           = 0x0001
	enableWrapAtEolOutput           = 0x0002
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procGetConsoleCP       = kernel32.NewProc("GetConsoleCP")
	procGetConsoleWindow   = kernel32.NewProc("GetConsoleWindow")
)

func stdHandle(which int) uintptr {
	h, _, _ := kernel32.NewProc("GetStdHandle").Call(uintptr(which))
	return h
}

func getConsoleMode(h uintptr) (uint32, bool) {
	var m uint32
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return 0, false
	}
	r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&m)))
	return m, r != 0
}

func setConsoleMode(h uintptr, m uint32) bool {
	r, _, _ := procSetConsoleMode.Call(h, uintptr(m))
	return r != 0
}

// consoleVTEnabled 判断当前标准输出控制台是否支持 VT 序列（TUI 的前提）。
//
// 被重定向到管道/文件、或根本没有控制台（GetConsoleWindow == 0）时返回 false。
func consoleVTEnabled() bool {
	if r, _, _ := procGetConsoleWindow.Call(); r == 0 {
		// 完全没有控制台窗口（例如双击 exe 且之前已 detach）——TUI 无从谈起。
		return false
	}
	h := stdHandle(-11) // STD_OUTPUT_HANDLE
	mode, ok := getConsoleMode(h)
	if !ok {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	// 尝试开启一次：Win10+ 成功；Win7/8 上 SetConsoleMode 对 VT 位要么报错
	// 要么「成功但读回来没置位」，两种都判为不支持。
	want := mode | enableVirtualTerminalProcessing | enableProcessedOutput | enableWrapAtEolOutput
	if !setConsoleMode(h, want) {
		return false
	}
	m2, ok2 := getConsoleMode(h)
	return ok2 && m2&enableVirtualTerminalProcessing != 0
}

// windowsVersionHint 给 Win7/8 用户的一段人话提示。
func windowsVersionHint() string {
	return "当前控制台不支持 ANSI/VT 转义序列（Windows 7/8 的 conhost 不支持，TUI 无法工作）。\n" +
		"可选方案：\n" +
		"  1) 经典交互模式：mizar -tui=false\n" +
		"  2) 只用网页界面：mizar -serve -addr :3003 ，浏览器打开 http://127.0.0.1:3003\n" +
		"  3) 换用 ConEmu / Windows Terminal 等支持 VT 的终端后再开 TUI"
}

// ensureConsoleUTF8 把控制台代码页切到 65001，否则中文输入/输出会乱码。
// 失败不致命（现代终端本来就是 UTF-8）。
func ensureConsoleUTF8() {
	const cpUTF8 = 65001
	if out, _, _ := procGetConsoleOutputCP.Call(); out != cpUTF8 {
		procSetConsoleOutputCP.Call(cpUTF8)
	}
	if in, _, _ := procGetConsoleCP.Call(); in != cpUTF8 {
		procSetConsoleCP.Call(cpUTF8)
	}
}
