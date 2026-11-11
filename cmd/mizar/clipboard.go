package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rivo/tview"
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
	// 尽力而为的外部同步（失败不影响内部功能）。
	// TUI 下绝不直写 OSC52：那串 \x1b]52;c;… 若被写出，tcell 不知道屏幕上多了东西，
	// 残留的 base64 尾巴会永远留在界面上（右下/右半边常见）。TUI 里改用系统剪贴板。
	if !tuiOutputActive() {
		osc52Set(text)
	}
	setGlobalClipboard(text)
}

// peek 只返回内部缓冲（不读系统剪贴板）。
// get 会优先读 X11/系统剪贴板，测试里那是有状态的共享资源，
// 断言「刚复制的内容」必须看内部缓冲才可靠。
func (c *clipboard) peek() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf
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

// clipboardExecTimeout 外部剪贴板工具的超时。
//
// 为什么要超时：xsel/xclip/powershell 在没有可用显示服务（SSH、headless、WSL 未配
// X 转发）时可能既不报错也不返回——而复制是在事件循环里同步调用的，
// 卡住就等于整个界面卡死（实测 xsel 在无 X 环境会挂住不退出）。
// 宁可这次不写系统剪贴板（内部缓冲 + OSC52 仍可用），也绝不能冻结界面。
const clipboardExecTimeout = 2 * time.Second

// runWithTimeout 跑一个外部命令（stdin 为 input），超时即杀。返回是否成功。
func runWithTimeout(name string, args []string, input string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	// WaitDelay 是必须的：xsel/xclip 这类工具在无 X 环境会 fork 出子进程并持有
	// stdin 管道，即使主进程被 kill，cmd.Wait 仍会等那条管道的拷贝 goroutine，
	// 结果就是「超时也返回不了」→ 事件循环照旧卡死。WaitDelay 到点强制关管道。
	cmd.WaitDelay = clipboardExecTimeout
	// 不接 stdout/stderr：部分工具会把内容吐回来，接住只占内存
	return cmd.Run() == nil
}

// outputWithTimeout 跑外部命令并取 stdout，超时/失败返回空串。
func outputWithTimeout(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = clipboardExecTimeout
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// setGlobalClipboard 把文本写入全局（X11/WinClip）剪贴板。仅本地直连或 WSLg 时生效。
func setGlobalClipboard(text string) {
	// 用 stdin 管道传给 xclip/xsel，避免大文本进 shell 命令行
	if clip, err := exec.LookPath("xclip"); err == nil {
		if runWithTimeout(clip, []string{"-selection", "clipboard"}, text) {
			return
		}
	}
	if sel, err := exec.LookPath("xsel"); err == nil {
		if runWithTimeout(sel, []string{"--clipboard", "--input"}, text) {
			return
		}
	}
	// WSLg / 原生 Windows 终端：尝试 powershell.exe 的剪贴板 Set-Clipboard
	if ps, err := exec.LookPath("powershell.exe"); err == nil {
		runWithTimeout(ps, []string{"-NoProfile", "-Command", "$Input | Set-Clipboard -Raw", "-"}, text)
	}
}

// readLocalClipboard 读取本地（X11 / WSLg）剪贴板内容。读取失败返回空串。
func readLocalClipboard() string {
	if clip, err := exec.LookPath("xclip"); err == nil {
		if out := outputWithTimeout(clip, "-selection", "clipboard", "-o"); out != "" {
			return strings.TrimRight(out, "\x00")
		}
	}
	if sel, err := exec.LookPath("xsel"); err == nil {
		if out := outputWithTimeout(sel, "--clipboard", "--output"); out != "" {
			return out
		}
	}
	return ""
}

// copyInputSelectionToClipboard 把输入框当前选中的文字复制到剪贴板。
// 由全局 InputCapture 在 KeyCtrlQ 时调用；无选区则返回 false 由调用方决定回退逻辑。
func copyInputSelectionToClipboard(input *tview.TextArea, clip *clipboard) bool {
	if input == nil || clip == nil || !input.HasSelection() {
		return false
	}
	text, _, _ := input.GetSelection()
	if strings.TrimSpace(text) == "" {
		return false
	}
	clip.set(text)
	return true
}
