package main

// 「粘贴突发」合并：把粘贴产生的一串字符先攒进缓冲，空闲后再一次性写进输入框。
//
// 背景：Windows 的 bracketed paste 不存在（tcell 在 Windows 上 EnablePaste 是
// 空实现），粘贴进来的每个字符都是一个独立的 KeyRune 事件。tview 对每个 KeyRune
// 的处理是：TextArea 内部 replace() 一次 → 触发 changedFunc（本程序里要 GetText()
// 全文 + strings.Fields）→ 事件循环里再 a.draw() 一次全量重绘。
//
// 于是一段几百字的粘贴会变成几百次「改文本 + 全文扫描 + 全屏重绘」，
// 观感就是「一个字一个字地蹦」，长一点要等很久（pi 等 TUI 之所以流畅，是因为
// 它们把粘贴内容当整块处理，只改一次、只画一次）。
//
// 对策：
//   - 与上一按键间隔极小（pasteBurstStartGap）时判定为粘贴，开始攒批；
//   - 攒批期间间隔仍在 pasteBurstExtendGap 内就继续并入（含粘贴来的换行）；
//   - 每收到一个字符就重置一个 pasteFlushDelay 的空闲定时器，空闲到点后
//     一次性 SetText（changedFunc 只跑一次），随后只重绘一次；
//   - 攒批期间用 pasteSkipScreen 吞掉屏幕输出（tview 每个按键都会 draw 一次，
//     每次 draw 结束都 Show() 全屏；这些中间态既浪费又造成「逐字蹦」的观感），
//     落盘后由 QueueUpdateDraw 触发一次真正的重画。
//
// 手工打字的按键间隔通常 ≥30ms，不会误并；一旦间隔变大，突发结束，
// 缓冲立即落盘，后续按键走正常路径。

import (
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	// pasteBurstStartGap 判定「这是粘贴的第 1 个字符」：与上一按键的间隔上限。
	pasteBurstStartGap = 8 * time.Millisecond
	// pasteBurstExtendGap 攒批过程中继续并入的间隔上限（比起点宽松，容忍抖动）。
	pasteBurstExtendGap = 40 * time.Millisecond
	// pasteFlushDelay 空闲多久后把缓冲写进输入框。
	pasteFlushDelay = 30 * time.Millisecond
	// pasteBurstMaxBytes 缓冲上限，防止异常输入把内存吃光。
	pasteBurstMaxBytes = 1 << 20
	// pasteBurstMaxDuration 单次攒批的最长持续时间：兜底防止「重绘一直被跳过」
	// 导致界面冻结（例如终端持续灌入字符）。超过即强制落盘并恢复重绘。
	pasteBurstMaxDuration = 2 * time.Second
)

// pasteBurstEnabledFor 判断是否启用粘贴攒批（getenv 便于测试注入）。
//
// 只在 Windows 上默认开启：Unix 终端支持 bracketed paste，tcell 会把整段粘贴
// 作为 EventPaste 一次性送来（tview 的 PasteHandler 一次写完整段），本来就不逐字；
// Windows 的 tcell EnablePaste 是空实现，粘贴必定逐字符到达，才有攒批的必要。
func pasteBurstEnabledFor(goos string, getenv func(string) string) bool {
	switch getenv("MIZAR_PASTE_BURST") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return goos == "windows"
}

// pasteBurstEnabled 生产环境入口。
func pasteBurstEnabled() bool {
	return pasteBurstEnabledFor(runtime.GOOS, os.Getenv)
}

// pasteBurstEligible 判断该按键能否并入粘贴缓冲：只有无 Alt/Ctrl/Meta 的普通字符。
// （Alt+K/J 滚动、Ctrl+U/D 翻页等快捷键必须立即生效，Shift 允许——粘贴大写字母会带 ModShift。）
func pasteBurstEligible(event *tcell.EventKey) bool {
	if event.Key() != tcell.KeyRune {
		return false
	}
	return event.Modifiers()&(tcell.ModAlt|tcell.ModCtrl|tcell.ModMeta) == 0
}

// startedPasteBurst 判断 now 时刻的字符是否属于「粘贴的第 1 个字符」：
// 与上一按键（last）间隔小于 pasteBurstStartGap。
// last 为零值（本进程还没收到过按键）不算突发——第一下无法判断，按正常输入处理，
// 第二个字符起自然落入突发（那时已能测出间隔）。
func startedPasteBurst(now, last time.Time) bool {
	if last.IsZero() {
		return false
	}
	d := now.Sub(last)
	return d >= 0 && d < pasteBurstStartGap
}

// pasteBurstKey 尝试把 event 归入粘贴突发。
// 返回 true 表示该事件已被缓冲（调用方应吞掉，不再往下处理）。
func (m *tuiModel) pasteBurstKey(event *tcell.EventKey, now time.Time) bool {
	if !m.pasteBurst {
		return false
	}
	// 1) 已在攒批中：间隔够小就继续并入
	if m.pastePending.Load() {
		if now.Sub(m.pasteLastAt) <= pasteBurstExtendGap {
			if pasteBurstEligible(event) {
				m.pasteAppend(string(event.Rune()), now)
				return true
			}
			// 粘贴里的换行（Windows 上没有 bracketed paste，换行就是裸 Enter）。
			// 这里用更严的窗口（8ms）：粘贴带来的换行是毫秒级紧跟的，而用户
			// 粘贴完顺手敲 Enter 提交通常 ≥30ms —— 那个 Enter 必须真正发送。
			if event.Key() == tcell.KeyEnter && event.Modifiers()&tcell.ModAlt == 0 &&
				isPasteNewline(now, m.pasteLastAt) {
				m.pasteAppend("\n", now)
				return true
			}
		}
		// 突发结束：先把攒下的内容落进输入框，本事件继续走正常路径
		m.flushPasteBurst()
		return false
	}

	// 2) 新突发的起点：与上一按键间隔极小 → 判定为粘贴
	if pasteBurstEligible(event) && startedPasteBurst(now, m.lastKeyAt) {
		m.pasteBuf.Reset()
		m.pasteStartAt = now
		m.pastePending.Store(true)
		m.pasteAppend(string(event.Rune()), now)
		return true
	}
	return false
}

// pasteAppend 追加一段文本并重置空闲落盘定时器。
func (m *tuiModel) pasteAppend(s string, now time.Time) {
	if m.pasteBuf.Len() >= pasteBurstMaxBytes || now.Sub(m.pasteStartAt) > pasteBurstMaxDuration {
		// 缓冲已满 / 攒得太久：直接落盘并重新开始计时，避免无限增长与界面冻结
		m.flushPasteBurst()
		m.pasteStartAt = now
		m.pastePending.Store(true)
	}
	m.pasteBuf.WriteString(s)
	m.pasteLastAt = now
	// 复用同一个定时器：粘贴几百字就是几百次 Reset，而不是新建几百个 goroutine
	// 和几百次事件循环往返（每次投递都会唤醒事件循环）。
	if m.pasteTimer == nil {
		m.pasteTimer = time.AfterFunc(pasteFlushDelay, m.pasteFlushTick)
	} else {
		m.pasteTimer.Reset(pasteFlushDelay)
	}
}

// pasteFlushTick 空闲到点：把缓冲一次性写进输入框并重画一次。
// 在定时器 goroutine 中运行，故经 QueueUpdateDraw 回到事件循环。
func (m *tuiModel) pasteFlushTick() {
	m.app.QueueUpdateDraw(func() {
		if !m.pastePending.Load() {
			return // 已被按键路径提前落盘（快捷键到达时）
		}
		m.flushPasteBurst()
		// QueueUpdateDraw 会在回调后自绘一次，这里不必再 renderAllDirect
	})
}

// flushPasteBurst 把缓冲一次性写入输入框（changedFunc 只触发一次）。
// 必须在事件循环线程上调用（InputCapture 内，或经 QueueUpdate 回到事件循环）。
func (m *tuiModel) flushPasteBurst() {
	if !m.pastePending.Swap(false) {
		return
	}
	text := m.pasteBuf.String()
	m.pasteBuf.Reset()
	if m.pasteTimer != nil {
		m.pasteTimer.Stop() // 让待触发的空闲定时器失效
	}
	if text == "" {
		return
	}
	cur := m.inputField.GetText()
	// 统一换行符，避免 \r 残留（多行粘贴常见）
	m.inputField.SetText(cur+normalizePastedNewlines(text), true)
}

// pasteSkipScreen 在粘贴攒批期间吞掉屏幕输出：tview 每个按键事件都会 draw 一次，
// 而每次 draw 结束时调用 Show() 把整个屏幕（逐行）写到终端。攒批期间这些输出全是
// 中间态（输入框还没拿到粘贴内容），既浪费又造成「逐字蹦」的观感，直接跳过。
// 其余方法全部透传底层屏幕（嵌入接口，只覆写必要方法）。
type pasteSkipScreen struct {
	tcell.Screen
	skip     func() bool
	deferred atomic.Bool // 攒批期间曾请求过输出（结束时必须重画一次）
}

func (s *pasteSkipScreen) Show() {
	if s.skip != nil && s.skip() {
		s.deferred.Store(true)
		return
	}
	s.deferred.Store(false)
	s.Screen.Show()
}

// TookDeferred 报告攒批期间是否吞掉过输出（测试用）。
func (s *pasteSkipScreen) TookDeferred() bool { return s.deferred.Load() }

// pasteBurstActive 供 pasteSkipScreen 判断是否需要跳过屏幕输出。
func (m *tuiModel) pasteBurstActive() bool {
	return m.pastePending.Load()
}
