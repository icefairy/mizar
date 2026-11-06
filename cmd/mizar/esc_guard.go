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
	"os"
	"runtime"
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/diag"
)

// escJunkWindow 判定「ESC 后紧跟的字符是否属于同一次泄漏序列」的时间窗口。
//
// Unix 下 ConPTY 的问题不存在，90ms 足够宽裕又不至于误伤手速。
//
// Windows 下必须更宽（250ms）：ConPTY 把泄漏的鼠标序列拆成多个 win32 输入记录，
// 记录之间可能隔着几十毫秒（尤其滚轮快速连滚时 ConPTY 会分批 flush），90ms 内
// 等不到 '[' 就会误判成「用户真按了 Esc」→ 仍然无辜取消任务（v0.7.1 实测复现）。
// 代价是 Windows 上按 Esc 取消会延迟 250ms，用户基本无感；换来滚轮彻底不再误杀。
const (
	escJunkWindowDefault = 90 * time.Millisecond
	escJunkWindowWindows = 300 * time.Millisecond
	// escJunkWindowWide 「最近刚见过泄漏序列」时用的宽窗口：滚轮快速连滚会让
	// ConPTY 分批 flush，序列到达间隔可达几百毫秒。已知当前终端正在泄漏时，
	// 宁可多等一会儿，也绝不把滚轮误判成取消。
	escJunkWindowWide = 1200 * time.Millisecond
	// escLeakMemory 记住「最近一次见到泄漏序列」的时长（用于选择宽窗口）。
	escLeakMemory = 3 * time.Second
	// escArmWindow 两段式 Esc 的第二次按键有效窗口。
	escArmWindow = 2500 * time.Millisecond
)

// escJunkWindow 生效窗口（按平台初始化，见 init）。
var escJunkWindow = escJunkWindowDefault

func init() {
	if runtime.GOOS == "windows" {
		escJunkWindow = escJunkWindowWindows
	}
}

// escJunkWindowFor 按平台返回窗口（便于测试注入 goos）。
func escJunkWindowFor(goos string) time.Duration {
	if goos == "windows" {
		return escJunkWindowWindows
	}
	return escJunkWindowDefault
}

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
	// 序列起始符：'['（CSI）、'<'（xterm SGR 鼠标）
	// 注：不能把 'O' 也算进来——小键盘兼容层用 Alt+O 作为前导（见 numpadState），
	// 算进来会把小键盘按键当成泄漏吞掉。
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
//
// escConfirmEnabledFor 是否启用「两段式 Esc 取消」（getenv 便于测试注入）。
//
// Windows 默认开启：ConPTY 的 win32-input-mode 双重编码缺陷（microsoft/terminal#15083、
// #16343）会把滚轮事件还原成以 ESC 开头的字符流，单次 Esc 即取消的实现无法保证
// 100% 不误杀（序列被拆成多批、批次间隔超过时间窗口时就会漏判）。
// 两段式确认后，滚轮最多只会出现一行提示，绝不会取消任务。
func escConfirmEnabledFor(goos string, getenv func(string) string) bool {
	switch getenv("MIZAR_ESC_CONFIRM") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return goos == "windows"
}

// escConfirmEnabled 生产环境入口。
func escConfirmEnabled() bool {
	return escConfirmEnabledFor(runtime.GOOS, os.Getenv)
}

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
			m.escLastJunkAt = now
			return 0, escGuardConsumed
		}
		m.escJunk = false
	}

	// 2) 泄漏序列的起点：tcell 把「ESC [」合成 Alt+'['；也可能是 ESC 后单独到达
	//    的 '[' / '<'（由下面的 escPending 分支覆盖）。
	if event.Key() == tcell.KeyRune && event.Modifiers()&tcell.ModAlt != 0 &&
		escJunkRune(event.Rune(), false) {
		m.escPending = false
		m.escSeq++ // 让挂起的取消定时器失效
		m.escJunk = true
		m.escJunkAt = now
		m.escLastJunkAt = now
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
			m.escLastJunkAt = now
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
	delay := m.escConfirmDelay(time.Now())
	time.AfterFunc(delay, func() {
		m.app.QueueUpdateDraw(func() {
			if m.escSeq != seq {
				return // 期间识别出泄漏序列 / 用户又按了 Esc → 本定时器作废
			}
			m.escConfirmPending()
		})
	})
}

// escConfirmPending 挂起 Esc 的最终确认（事件循环线程上调用）。
//
// 三层拦截，目标只有一个：滚轮绝不能杀掉正在跑的任务。
//  1. 最近仍在泄漏鼠标序列（ConPTY 的已知缺陷）→ 这一次 Esc 极可能也是泄漏，
//     直接放弃取消；
//  2. Windows 上 Esc 取消改为「两段式」：第一次只提示「再按一次 Esc 取消」，
//     2.5 秒内再按一次才真正取消。泄漏序列只要凑不成两次完整的 Esc，
//     就永远不会误杀任务（这是对付「窗口内没等到坐标字符」的最终兜底）；
//  3. 其余情况按原逻辑取消。
func (m *tuiModel) escConfirmPending() {
	if !m.escPending {
		return
	}
	m.escPending = false
	if m.recentLeak() {
		m.escSeq++
		diag.Logf("tui", "挂起 Esc 到点时仍在泄漏鼠标序列 → 判定为泄漏，不取消任务")
		return
	}
	if m.escConfirm {
		if !m.escArmedAt.IsZero() && time.Since(m.escArmedAt) <= escArmWindow {
			m.escArmedAt = time.Time{}
			m.cancelCurrentTask()
			return
		}
		m.escArmedAt = time.Now()
		diag.Logf("tui", "Esc 第一次按下 → 进入「再按一次取消」待确认态")
		m.addChatLine(chatLine{role: "err", ts: time.Now(),
			content: "⚠ 再按一次 Esc 取消任务（Windows 下滚轮会被误编码成 Esc，故需确认）"})
		m.renderAllDirect()
		return
	}
	m.cancelCurrentTask()
}

// recentLeak 报告最近是否观察到过 ConPTY 泄漏的鼠标序列。
func (m *tuiModel) recentLeak() bool {
	return !m.escLastJunkAt.IsZero() && time.Since(m.escLastJunkAt) < escLeakMemory
}

// escConfirmDelay 挂起 Esc 的确认延迟：终端正在泄漏时用更宽的窗口。
func (m *tuiModel) escConfirmDelay(now time.Time) time.Duration {
	if m.recentLeak() {
		return escJunkWindowWide
	}
	return escJunkWindow
}

// escConfirmForTest 立即执行挂起 Esc 的确认逻辑（仅供测试，避免等待真实定时器）。
func (m *tuiModel) escConfirmForTest() {
	m.escConfirmPending()
}
