package main

// 「伪 ESC」防护：过滤 ConPTY 泄漏的鼠标序列，避免滚轮被当成取消任务。
//
// 背景（与 platform_input.go 呼应）：Windows 上鼠标上报已默认关闭，但 ConPTY 会
// 无条件开启 win32-input-mode，宿主终端（Windows Terminal）把滚轮/移动编码成
// win32 鼠标记录，ConPTY 解析时又存在双重编码缺陷（microsoft/terminal#15083、
// #16343），最终这些事件被还原成普通字符流灌进控制台输入缓冲，形如：
//
//	ESC [ < 35 ; 66 ; 61 M
//
// tcell 把开头的 ESC 当成一次真正的 KeyEsc 事件交给 tview，于是用户在聊天区
// 滚一下滚轮 → 无辜触发「任务已取消（当前步骤结束后停止）」，观感像按了 Esc 暂停。
// 更常见的是 tcell 的 Alt 解析路径：ESC 后紧跟 '[' 会被合成一个 Alt+'[' 事件。
//
// 对策：不立即把 ESC 当作取消，而是先挂起 ~90ms：
//   - 若紧跟其后出现 '[' / '<'（含 Alt+'[' 形式）+ 数字/;/M 组成的坐标序列
//     → 判定为泄漏，吞掉整段（ESC 与后续字符都不进输入框），任务继续跑；
//   - 若这段时间没有泄漏序列出现 → 才真正提交「取消任务」。
//
// 代价是手按 Esc 取消会延迟约 90ms，用户无感；换来滚轮不再误杀任务。

import (
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/diag"
)

// escJunkWindow 判定「ESC 后紧跟的字符是否属于同一次泄漏序列」的时间窗口。
// ConPTY 一次写入的序列内部间隔是微秒级，90ms 足够宽裕又不至于误伤手速。
const escJunkWindow = 90 * time.Millisecond

// escGuardAction 是 escGuardKey 的处理结论。
type escGuardAction int

const (
	// escGuardNone：本事件与泄漏序列无关，调用方继续正常处理。
	escGuardNone escGuardAction = iota
	// escGuardConsumed：本事件属于泄漏序列，调用方应吞掉（return nil）。
	escGuardConsumed
	// escGuardPendingEsc：本事件是待确认的 Esc，调用方应安排定时确认后吞掉。
	escGuardPendingEsc
)

// escJunkRune 判定字符是否可能出现在泄漏的鼠标/坐标序列里。
//
// inSeq=false 时只认序列起始符（'[' / '<'）——单独出现的数字（用户回答选项、
// 输入数字）绝不能被当成泄漏；inSeq=true 时才允许数字、';'、'M'、'm'。
func escJunkRune(r rune, inSeq bool) bool {
	if r == '[' || r == '<' {
		return true
	}
	if !inSeq {
		return false
	}
	switch r {
	case ';', 'M', 'm':
		return true
	}
	return r >= '0' && r <= '9'
}

// escGuardKey 处理单个按键事件，返回调用方应如何处置。
//
// 状态机（字段在 tuiModel 上，见 tui.go）：
//
//	escPending —— 有 Esc 挂起，等待确认是否泄漏序列的起点
//	escSeq     —— 挂起序号，用于让过期的确认定时器失效
//	escJunk    —— 正在吞一段已确认的泄漏序列
func (m *tuiModel) escGuardKey(event *tcell.EventKey, now time.Time) (int, escGuardAction) {
	// 1) 正在吞泄漏序列：窗口内的坐标字符继续吞，出窗口或非坐标字符即结束
	if m.escJunk {
		if event.Key() == tcell.KeyRune && now.Sub(m.escJunkAt) <= escJunkWindow &&
			escJunkRune(event.Rune(), true) {
			m.escJunkAt = now
			// 'M' / 'm' 是坐标序列的终结符：吞掉它即整段结束，后续按键恢复正常
			if event.Rune() == 'M' || event.Rune() == 'm' {
				m.escJunk = false
			}
			return 0, escGuardConsumed
		}
		m.escJunk = false
	}

	// 2) 泄漏序列的起点：tcell 把「ESC [」合成 Alt+'['；也可能是 ESC 后单独到达
	//    的 '[' / '<'（由下面的 escPending 分支覆盖）。
	if event.Key() == tcell.KeyRune && event.Modifiers()&tcell.ModAlt != 0 &&
		(event.Rune() == '[' || event.Rune() == '<') {
		m.escPending = false
		m.escSeq++ // 让挂起的取消定时器失效
		m.escJunk = true
		m.escJunkAt = now
		diag.Logf("tui", "Alt+%q → 判定为终端泄漏的鼠标序列，吞掉（不取消任务）", event.Rune())
		return 0, escGuardConsumed
	}

	// 3) 真正的 Esc（仅在任务运行中有意义）：挂起，等待确认
	if event.Key() == tcell.KeyEsc && m.loading {
		m.escPending = true
		m.escSeq++
		return m.escSeq, escGuardPendingEsc
	}

	// 4) 挂起中的 Esc 遇到后续字符：是序列起点就吞掉整段；否则是用户真按了 Esc
	if m.escPending {
		m.escPending = false
		m.escSeq++ // 让挂起的取消定时器失效
		if event.Key() == tcell.KeyRune && escJunkRune(event.Rune(), false) {
			m.escJunk = true
			m.escJunkAt = now
			diag.Logf("tui", "ESC 后紧跟 %q → 判定为终端泄漏的鼠标序列，吞掉（不取消任务）", event.Rune())
			return 0, escGuardConsumed
		}
		m.cancelCurrentTask()
		// 取消后本事件继续按正常按键处理（例如用户用 Esc 关掉某层界面）
	}
	return 0, escGuardNone
}

// scheduleEscConfirm 安排「挂起的 Esc 到点确认」：窗口内没有出现泄漏序列即真正取消。
//
// 定时器跑在独立 goroutine，因此必须经 QueueUpdateDraw 回到事件循环；
// 用 escSeq 比对来作废旧定时器（用户连按 Esc 或中途识别出泄漏时）。
func (m *tuiModel) scheduleEscConfirm(seq int) {
	if seq == 0 {
		return
	}
	time.AfterFunc(escJunkWindow, func() {
		m.app.QueueUpdateDraw(func() {
			if !m.escPending || m.escSeq != seq {
				return
			}
			m.escPending = false
			m.cancelCurrentTask()
		})
	})
}

// escConfirmForTest 立即执行挂起 Esc 的确认逻辑（仅供测试，避免等待真实定时器）。
func (m *tuiModel) escConfirmForTest() {
	if !m.escPending {
		return
	}
	m.escPending = false
	m.cancelCurrentTask()
}
