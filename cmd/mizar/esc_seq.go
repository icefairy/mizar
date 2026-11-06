package main

// Windows 上的「原始 VT 序列」解码器。
//
// 背景：某些 Windows 终端 / ConPTY 组合（尤其经由 Windows Terminal、mintty、
// 以及被 conhost 以 VT 输入模式接管的场景）并不会把方向键、Home/End、Del、
// F 键等翻译成 tcell 的专用 Key，而是把原始的 ESC 前缀序列逐字符送上来：
//
//	左箭头 = ESC [ D     上箭头 = ESC [ A     Delete = ESC [ 3 ~
//
// tcell 把它拆成三个独立的按键事件（KeyEsc、KeyRune '['、KeyRune 'D'），
// tview 只认识 Key，于是这三个字符被当成普通文本塞进输入框——用户看到的就是
// 「按左键出现 [D」。同理 Alt+K/J（ESC k）、Ctrl+方向键（ESC [ 1 ; 5 C）
// 也会失效。
//
// 对策：在 InputCapture 最前面加一个小的 VT 序列解码器：
//   - ESC 后的字符按极短的间隔（escSeqGap）拼回序列；
//   - 命中已知按键 → 合成真正的 Key 事件交给 tview（方向键、Home/End、PgUp/PgDn、
//     Insert/Delete、F1~F12、带修饰键的方向键、Alt+字母）；
//   - 命中 ConPTY 泄漏的鼠标序列（ESC[<…M / ESC[M… / ESC[0;35;66M）→ 整段吞掉
//     并记录「最近在泄漏」，让 Esc 取消继续走宽窗口/两段式确认；
//   - 认不出来 → 把 ESC 之后的字符按原文重新送进输入框（绝不丢用户输入）。
//
// 只在 Windows 默认开启（Unix 的 tcell 正常翻译，开了反而有误判风险）：
// MIZAR_ESC_SEQ=1/0 可强制开关。

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/diag"
)

const (
	// escSeqGap 序列内部字符的到达间隔上限。真实序列由终端一次写入，间隔是微秒级；
	// 120ms 已非常宽裕，又能避免把「用户先按 Esc 再慢慢打字」粘成一条序列。
	escSeqGap = 120 * time.Millisecond
	// escSeqAltGap ESC 后紧跟单个字符才算 Alt+字符（Alt 是一个物理动作，间隔极短）。
	// 超过它说明用户是「按了 Esc 之后又打字」，不能合并成 Alt。
	escSeqAltGap = 35 * time.Millisecond
	// escSeqMaxLen 缓冲上限，防止异常输入无限增长。
	escSeqMaxLen = 32
)

// escSeqDecodeEnabledFor 是否启用原始 VT 序列解码（getenv 便于测试注入）。
func escSeqDecodeEnabledFor(goos string, getenv func(string) string) bool {
	switch getenv("MIZAR_ESC_SEQ") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return goos == "windows"
}

// escSeqDecodeEnabled 生产环境入口。
func escSeqDecodeEnabled() bool {
	return escSeqDecodeEnabledFor(runtime.GOOS, os.Getenv)
}

// escSeqClass 解码结论。
type escSeqClass int

const (
	// escSeqMore 还需要更多字符才能判定（含「当前只有 ESC」——那是一个真实 Esc，
	// 但必须留一点时间看它是不是某个序列的开头）。
	escSeqMore escSeqClass = iota
	// escSeqKeyDone 已解析出一个按键。
	escSeqKeyDone
	// escSeqLeak 命中 ConPTY 泄漏的鼠标序列，应整段吞掉。
	escSeqLeak
	// escSeqLiteral 不是序列：把 ESC 之后的字符按原文送回输入框。
	escSeqLiteral
)

// escSeqKey 处理按键事件，返回 (是否已接管, 要交给 tview 的替换事件)。
//
// 状态机（字段在 tuiModel 上）：escSeqBuf 以 0x1b 开头，escSeqStart 是它的起点时间。
func (m *tuiModel) escSeqKey(event *tcell.EventKey, now time.Time) (bool, *tcell.EventKey) {
	if !m.escSeqDecode {
		return false, event
	}
	// 缓冲太久没续上：丢弃残留（不要让它污染后续按键）
	if len(m.escSeqBuf) > 0 && now.Sub(m.escSeqAt) > escSeqGap {
		m.escSeqBuf = nil
	}

	// 1) 缓冲区非空：本事件可能是序列的延续
	if len(m.escSeqBuf) > 0 {
		switch event.Key() {
		case tcell.KeyRune:
			// ESC 之后紧跟着一个普通字符：间隔够短才是 Alt+字符，
			// 否则是「按了 Esc 又打字」，就此结束序列、按正常按键处理。
			if len(m.escSeqBuf) == 1 && now.Sub(m.escSeqStart) > escSeqAltGap {
				m.escSeqBuf = nil
				return false, event
			}
			if len(m.escSeqBuf) >= escSeqMaxLen {
				m.escSeqBuf = nil
				return false, event
			}
			m.escSeqBuf = append(m.escSeqBuf, event.Rune())
			m.escSeqAt = now
			handled, ev := m.escSeqStep()
			m.scheduleEscSeqFlush() // 半截序列超时后按原文补进输入框
			return handled, ev
		case tcell.KeyEsc:
			// 连按 Esc：重新开始缓冲，并走既有的两段式确认
			m.escSeqBuf = []rune{0x1b}
			m.escSeqStart, m.escSeqAt = now, now
			m.beginEscPending()
			m.scheduleEscSeqFlush()
			return true, nil
		default:
			// 其他按键：序列到此为止（此前那次 Esc 已按真实 Esc 处理过）
			m.escSeqBuf = nil
			return false, event
		}
	}

	// 2) tcell 已合成的 Alt+字符：等价于「ESC + 字符」的序列起点。
	//    Alt+'[' / Alt+'<' 是 ConPTY 泄漏鼠标序列的典型形态，必须继续按序列解析
	//    （否则 '[' 会被当成普通字符塞进输入框）；其余 Alt+字符是真正的组合键，
	//    原样交出（Alt+K/J 滚动、Alt+C 拷贝模式都走这条路）。
	if event.Key() == tcell.KeyRune && event.Modifiers()&tcell.ModAlt != 0 {
		if event.Rune() == '[' || event.Rune() == '<' || event.Rune() == 'O' {
			m.escSeqBuf = []rune{0x1b, event.Rune()}
			m.escSeqStart, m.escSeqAt = now, now
			m.escPending = false
			m.escSeq++ // 让挂起的取消定时器失效
			handled, ev := m.escSeqStep()
			m.scheduleEscSeqFlush()
			return handled, ev
		}
		// Alt+字母/数字本身就是一个完整按键，直接交出（保留 Alt 语义）
		m.escSeqBuf = nil
		m.escPending = false
		m.escSeq++ // 让挂起的取消定时器失效
		return true, event
	}

	// 3) 单独的 Esc：先记下来，等一个极短窗口看它是不是某个序列的开头。
	//    任务运行中 → 同时走 esc_guard 的确认流程（取消任务）；
	//    任务没在跑 → 窗口到期仍无后续字符时才把 Esc 交回 tview（用于收起提示条）。
	//    这样「按左箭头」永远不会漏出 '[' 'D'，而单击 Esc 的行为不变（延迟 ~130ms）。
	if event.Key() == tcell.KeyEsc {
		m.escSeqBuf = []rune{0x1b}
		m.escSeqStart, m.escSeqAt = now, now
		m.beginEscPending()
		m.scheduleEscSeqFlush()
		return true, nil
	}
	return false, event
}

// scheduleEscSeqFlush 安排一次「序列超时落盘」：若 escSeqGap 后缓冲仍未续上，
// 就把残留字符按原文补进输入框（避免 ESC O 这种半截序列把 'O' 吞掉）。
func (m *tuiModel) scheduleEscSeqFlush() {
	m.escSeqGen++
	gen := m.escSeqGen
	time.AfterFunc(escSeqGap+10*time.Millisecond, func() {
		m.app.QueueUpdate(func() {
			if m.escSeqGen != gen || len(m.escSeqBuf) == 0 {
				return
			}
			if len(m.escSeqBuf) == 1 {
				// 只有 ESC：任务运行中已由 esc_guard 的确认流程处理；未运行时
				// 现在才把 Esc 交回 tview（收起提示条/结束编辑）。
				m.escSeqBuf = nil
				if !m.loading {
					m.app.QueueEvent(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
				}
				return
			}
			rest := string(m.escSeqBuf[1:])
			m.escSeqBuf = nil
			m.escPending = false // 半截序列 → 不是 Esc 取消
			diag.Logf("tui", "半截序列 %q 超时 → 按原文补进输入框", rest)
			for _, ch := range rest {
				m.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
			}
		})
	})
}

// beginEscPending 进入「Esc 已按下，等待确认取消」状态（复用 esc_guard 的两段式逻辑）。
func (m *tuiModel) beginEscPending() {
	if !m.loading {
		// 任务没在跑，Esc 没有取消语义；缓冲仍然保留，用于识别后续序列
		m.escPending = false
		return
	}
	if m.recentLeak() {
		// 终端正在泄漏鼠标序列：不安排确认，免得滚动时反复弹「再按一次 Esc」提示。
		// 真正的取消由用户再按一次 Esc（届时泄漏记忆已过期）完成。
		m.escPending = false
		return
	}
	m.escPending = true
	m.escSeq++
	m.scheduleEscConfirm(m.escSeq)
}

// escSeqStep 对当前缓冲做一次判定，必要时合成按键事件。
func (m *tuiModel) escSeqStep() (bool, *tcell.EventKey) {
	buf := m.escSeqBuf
	if len(buf) == 1 {
		// 只有 ESC：真实 Esc（beginEscPending 已在调用方执行）；继续等后续字符
		return true, nil
	}
	class, key, r, mod, literal := decodeEscSeq(buf)
	switch class {
	case escSeqMore:
		return true, nil
	case escSeqKeyDone:
		m.escSeqBuf = nil
		diag.Logf("tui", "原始序列 %q → 识别为按键 %s", string(buf), tcell.KeyNames[key])
		return m.emitEscKey(key, r, mod)
	case escSeqLeak:
		m.escSeqBuf = nil
		m.escLastJunkAt = time.Now()
		diag.Logf("tui", "吞掉 ConPTY 泄漏的鼠标序列 %q", string(buf))
		return true, nil
	default: // escSeqLiteral
		m.escSeqBuf = nil
		// 认不出来：把 ESC 之后的字符按原文送回输入框（绝不吞用户输入）
		if literal == "" {
			return true, nil
		}
		runes := []rune(literal)
		for _, ch := range runes[1:] {
			m.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
		}
		return m.emitEscKey(tcell.KeyRune, runes[0], tcell.ModNone)
	}
}

// emitEscKey 把一个识别出的按键交回 tview（直接返回给 InputCapture 调用方，
// 避免再走一遍解码器造成自我循环）。
func (m *tuiModel) emitEscKey(key tcell.Key, r rune, mod tcell.ModMask) (bool, *tcell.EventKey) {
	// 识别出按键说明这次 ESC 是序列的一部分，取消挂起的「Esc 取消任务」
	m.escPending = false
	m.escSeq++
	return true, tcell.NewEventKey(key, r, mod)
}

// decodeEscSeq 解析以 ESC 开头的字符缓冲（纯函数，便于测试）。
// 返回：结论、按键（KeyRune 时用 r）、修饰键、按原文处理的字符串。
func decodeEscSeq(buf []rune) (escSeqClass, tcell.Key, rune, tcell.ModMask, string) {
	if len(buf) == 0 || buf[0] != 0x1b {
		return escSeqLiteral, 0, 0, 0, string(buf)
	}
	rest := buf[1:]
	if len(rest) == 0 {
		return escSeqMore, 0, 0, 0, ""
	}
	switch rest[0] {
	case '[':
		return decodeCSI(rest[1:])
	case 'O':
		return decodeSS3(rest[1:])
	}
	if len(rest) == 1 {
		// ESC + 单个字符 = Alt+字符（tcell 在部分平台上会自己合成 Alt+ch，
		// 但 Windows 走原始序列时这里才是权威判断）
		mod := tcell.ModAlt
		if rest[0] >= 'A' && rest[0] <= 'Z' {
			mod |= tcell.ModShift // 大写字母通常伴随 Shift
		}
		return escSeqKeyDone, tcell.KeyRune, rest[0], mod, ""
	}
	// ESC + 多个非序列字符：不是序列，原文送回
	return escSeqLiteral, 0, 0, 0, string(rest)
}

// decodeCSI 解析 CSI（ESC[ … 终结符）序列体。
func decodeCSI(body []rune) (escSeqClass, tcell.Key, rune, tcell.ModMask, string) {
	if len(body) == 0 {
		return escSeqMore, 0, 0, 0, ""
	}
	switch body[0] {
	case '<':
		// SGR 鼠标（ConPTY 泄漏的典型形态）：ESC[<35;66;61M
		for _, c := range body[1:] {
			if c == 'M' || c == 'm' {
				return escSeqLeak, 0, 0, 0, ""
			}
		}
		if len(body) > 24 {
			return escSeqLiteral, 0, 0, 0, string(body)
		}
		return escSeqMore, 0, 0, 0, ""
	case 'M':
		// X11 鼠标：ESC[M + 3 字节
		if len(body) >= 4 {
			return escSeqLeak, 0, 0, 0, ""
		}
		return escSeqMore, 0, 0, 0, ""
	}
	// 参数（数字与分号）后跟终结符
	i := 0
	for i < len(body) && (body[i] >= '0' && body[i] <= '9' || body[i] == ';') {
		i++
	}
	if i == len(body) {
		if len(body) > 16 {
			return escSeqLiteral, 0, 0, 0, string(body)
		}
		return escSeqMore, 0, 0, 0, ""
	}
	final := body[i]
	params := string(body[:i])
	if final == 'M' || final == 'm' {
		return escSeqLeak, 0, 0, 0, ""
	}
	nums, mod := parseCSIParams(params)
	if final == '~' {
		if len(nums) > 0 {
			if key, ok := csiTildeKey(nums[0]); ok {
				return escSeqKeyDone, key, 0, mod, ""
			}
		}
		return escSeqLiteral, 0, 0, 0, string(body)
	}
	if key, ok := csiLetterKey(final); ok {
		return escSeqKeyDone, key, 0, mod, ""
	}
	return escSeqLiteral, 0, 0, 0, string(body)
}

// decodeSS3 解析 SS3（ESC O …）。
func decodeSS3(body []rune) (escSeqClass, tcell.Key, rune, tcell.ModMask, string) {
	if len(body) == 0 {
		return escSeqMore, 0, 0, 0, ""
	}
	switch body[0] {
	case 'A':
		return escSeqKeyDone, tcell.KeyUp, 0, 0, ""
	case 'B':
		return escSeqKeyDone, tcell.KeyDown, 0, 0, ""
	case 'C':
		return escSeqKeyDone, tcell.KeyRight, 0, 0, ""
	case 'D':
		return escSeqKeyDone, tcell.KeyLeft, 0, 0, ""
	case 'H':
		return escSeqKeyDone, tcell.KeyHome, 0, 0, ""
	case 'F':
		return escSeqKeyDone, tcell.KeyEnd, 0, 0, ""
	case 'P':
		return escSeqKeyDone, tcell.KeyF1, 0, 0, ""
	case 'Q':
		return escSeqKeyDone, tcell.KeyF2, 0, 0, ""
	case 'R':
		return escSeqKeyDone, tcell.KeyF3, 0, 0, ""
	case 'S':
		return escSeqKeyDone, tcell.KeyF4, 0, 0, ""
	}
	// 小键盘：ESC O q = 数字 1、ESC O p = 0 …（vt100 application keypad 编码）
	if digit, ok := ss3NumpadDigit(body[0]); ok {
		return escSeqKeyDone, tcell.KeyRune, digit, tcell.ModNone, ""
	}
	return escSeqLiteral, 0, 0, 0, "O" + string(body)
}

// ss3NumpadDigit 把 application-keypad 的 SS3 字母映射为数字键。
func ss3NumpadDigit(r rune) (rune, bool) {
	m := map[rune]rune{
		'q': '1', 'r': '2', 's': '3', 't': '4', 'u': '5',
		'v': '6', 'w': '7', 'x': '8', 'y': '9', 'p': '0',
		'n': '.', 'm': '-', 'k': '+', 'l': ',', 'o': '/',
	}
	d, ok := m[r]
	return d, ok
}

// parseCSIParams 解析 CSI 参数串（"1;5" 之类）：返回各参数与修饰键。
// 修饰键编码（xterm）：1=无、2=Shift、3=Alt、4=Alt+Shift、5=Ctrl、6=Ctrl+Shift、
// 7=Ctrl+Alt、8=Ctrl+Alt+Shift。
func parseCSIParams(params string) ([]int, tcell.ModMask) {
	if params == "" {
		return nil, tcell.ModNone
	}
	fields := strings.Split(params, ";")
	nums := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			nums = append(nums, 0)
			continue
		}
		nums = append(nums, n)
	}
	var mod tcell.ModMask
	if len(nums) >= 2 {
		switch nums[1] {
		case 2:
			mod = tcell.ModShift
		case 3:
			mod = tcell.ModAlt
		case 4:
			mod = tcell.ModShift | tcell.ModAlt
		case 5:
			mod = tcell.ModCtrl
		case 6:
			mod = tcell.ModCtrl | tcell.ModShift
		case 7:
			mod = tcell.ModCtrl | tcell.ModAlt
		case 8:
			mod = tcell.ModCtrl | tcell.ModAlt | tcell.ModShift
		}
	}
	return nums, mod
}

// csiTildeKey 把 "ESC[<n>~" 的 n 映射到按键。
func csiTildeKey(n int) (tcell.Key, bool) {
	switch n {
	case 1, 7:
		return tcell.KeyHome, true
	case 2:
		return tcell.KeyInsert, true
	case 3:
		return tcell.KeyDelete, true
	case 4, 8:
		return tcell.KeyEnd, true
	case 5:
		return tcell.KeyPgUp, true
	case 6:
		return tcell.KeyPgDn, true
	case 11:
		return tcell.KeyF1, true
	case 12:
		return tcell.KeyF2, true
	case 13:
		return tcell.KeyF3, true
	case 14:
		return tcell.KeyF4, true
	case 15:
		return tcell.KeyF5, true
	case 17:
		return tcell.KeyF6, true
	case 18:
		return tcell.KeyF7, true
	case 19:
		return tcell.KeyF8, true
	case 20:
		return tcell.KeyF9, true
	case 21:
		return tcell.KeyF10, true
	case 23:
		return tcell.KeyF11, true
	case 24:
		return tcell.KeyF12, true
	}
	return 0, false
}

// csiLetterKey 把 CSI 的字母终结符映射到按键。
func csiLetterKey(final rune) (tcell.Key, bool) {
	switch final {
	case 'A':
		return tcell.KeyUp, true
	case 'B':
		return tcell.KeyDown, true
	case 'C':
		return tcell.KeyRight, true
	case 'D':
		return tcell.KeyLeft, true
	case 'H':
		return tcell.KeyHome, true
	case 'F':
		return tcell.KeyEnd, true
	case 'Z':
		return tcell.KeyBacktab, true
	case 'P':
		return tcell.KeyF1, true
	case 'Q':
		return tcell.KeyF2, true
	case 'R':
		return tcell.KeyF3, true
	case 'S':
		return tcell.KeyF4, true
	}
	return 0, false
}
