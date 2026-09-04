package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// =============================================================================
// 剪贴板桥接
//
// 场景：mizar 通过 SSH/终端连接到远端，用户本地剪贴板与远端 OS 剪贴板是两套。
// 因此这里采用"分层"策略，保证在不同终端下都能尽可能工作：
//
//  1. 内部剪贴板（最可靠，mizar 自身内部复制/粘贴不受任何终端限制）：
//     copy 的内容存入只读缓冲；在任何终端、任何模式下都可从中粘贴。
//  2. OSC 52 剪贴板（跨 SSH 通用协议）：
//     copy 时额外写 \x1b]52;c;base64\x1b\\，支持的终端（XShell/Windows
//     Terminal/kitty/tmux 等）会把内容同步到用户本地剪贴板。
//  3. xsel/xclip 回退（本地直连 / WSLg 时）：
//     copy 时写入本地 X11 剪贴板；get 时优先尝试读取本地剪贴板。
//
// 粘贴优先级：本地 X11 剪贴板 > 内部缓冲。
// 用户始终可用终端自带的 Ctrl+Shift+V 粘贴 OS 剪贴板（bracketed paste，不受此处影响）。
// =============================================================================

// clipboard 是 mizar 的剪贴板桥接实现（内部缓冲 + OSC52 + X11 本地）。
type clipboard struct {
	mu  sync.Mutex
	buf string // 内部剪贴板：copy 时存入，get 时作为兜底
}

// newClipboard 创建一个空剪贴板桥接。
func newClipboard() *clipboard {
	return &clipboard{}
}

// set 写入剪贴板：存内部缓冲，并尽力同步到 OSC 52（终端本地）与 X11（WSLg/直连）。
func (c *clipboard) set(text string) {
	if text == "" {
		return
	}
	c.mu.Lock()
	c.buf = text
	c.mu.Unlock()
	// 尽力而为的外部同步（失败不影响内部功能）
	osc52Set(text)
	setGlobalClipboard(text)
}

// get 返回剪贴板内容：优先本地 X11/WSLg，其次内部缓冲（SSH 下外部读不到时用内部）。
func (c *clipboard) get() string {
	if local := readLocalClipboard(); local != "" {
		return local
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf
}

// osc52Set 通过 OSC 52 序列把文本写入终端剪贴板（跨 SSH 同步到用户本地）。
// 序列：ESC ] 52 ; c ; <base64> 终止符（ST \x1b\\ 与 BEL \x07 都发，最大化兼容）。
func osc52Set(text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\x1b\\", encoded)
	fmt.Fprintf(os.Stderr, "\x1b]52;c;%s\x07", encoded)
}

// setGlobalClipboard 把文本写入全局（X11/WinClip）剪贴板。仅本地直连或 WSLg 时生效。
func setGlobalClipboard(text string) {
	// 用 stdin 管道传给 xclip/xsel，避免大文本进 shell 命令行
	if clip, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(clip, "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return
		}
	}
	if sel, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command(sel, "--clipboard", "--input")
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return
		}
	}
	// WSLg / 原生 Windows 终端：尝试 powershell.exe 的剪贴板 Set-Clipboard
	if ps, err := exec.LookPath("powershell.exe"); err == nil {
		cmd := exec.Command(ps, "-NoProfile", "-Command",
			"$Input | Set-Clipboard -Raw", "-")
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	}
}

// readLocalClipboard 读取本地（X11 / WSLg）剪贴板内容。读取失败返回空串。
func readLocalClipboard() string {
	if clip, err := exec.LookPath("xclip"); err == nil {
		out, perr := exec.Command(clip, "-selection", "clipboard", "-o").Output()
		if perr == nil && len(out) > 0 {
			return strings.TrimRight(string(out), "\x00")
		}
	}
	if sel, err := exec.LookPath("xsel"); err == nil {
		out, perr := exec.Command(sel, "--clipboard", "--output").Output()
		if perr == nil && len(out) > 0 {
			return string(out)
		}
	}
	return ""
}
