package main

// 屏幕自愈看门狗：定期核对尺寸、空闲时整屏重画，消灭「右半边残留文字」。
//
// 现象：聊天区刷新后，屏幕右侧（或上半区）留着旧文字，怎么滚动都不消失。
// 两个常见成因，都需要「整屏强制重画」才能根治：
//
//  1. tcell 的 CellBuffer 宽度小于终端实际宽度（窗口调整后 ConPTY 没送 resize
//     记录，或送了但 tview 没重排）。此后每次 draw 只覆盖左边那一块，右侧
//     永远保持上一次更宽窗口的内容 → 观察到的就是「右半边一堆残留」。
//     tcell 的 Sync() 会重新向系统查询尺寸并补发 EventResize，是唯一解。
//
//  2. 有东西绕过 tcell 直接往终端写（第三方库 log、子进程输出、终端自身的
//     提示）。tcell 认为那些格子还是自己画的，增量重绘不会覆盖 → 残留。
//
// 策略：每 screenWatchInterval 检查一次
//   - 尺寸变了（screen.Size() 与上次记录不同）→ 立即 Sync + 全量重绘；
//   - 用户空闲（≥ screenIdleResync 没有按键）且距上次 Sync 够久 → 也 Sync 一次，
//     兜底清掉成 2 类残留。忙时不打扰，避免与输入/流式渲染抢事件循环。

import (
	"runtime"
	"sync/atomic"
	"time"

	"mizar/internal/diag"
)

const (
	screenWatchInterval = 2 * time.Second
	// screenIdleResync 空闲多久后自动整屏重画一次（兜底清除残留文字）。
	screenIdleResync = 5 * time.Second
	// screenMinSyncGap 两次自动重画的最小间隔（防止频繁全屏 Sync 造成闪烁）。
	screenMinSyncGap = 10 * time.Second
)

// startScreenWatchdog 启动屏幕看门狗（Run 内调用一次）。
// 用 m.watchStop 退出，避免 goroutine 泄漏到下一个 Run。
func (m *tuiModel) startScreenWatchdog() {
	m.watchStop = make(chan struct{})
	stop := m.watchStop
	go func() {
		ticker := time.NewTicker(screenWatchInterval)
		defer ticker.Stop()
		var lastW, lastH int
		lastSync := time.Now()
		if m.scr != nil {
			lastW, lastH = m.scr.Size()
		}
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if m.scr == nil {
					continue
				}
				w, h := m.scr.Size()
				if sizeChanged(w, h, lastW, lastH) {
					lastW, lastH = w, h
					diag.Logf("tui", "看门狗：终端尺寸变化 %dx%d → 整屏重画", w, h)
					m.requestResyncFromGoroutine()
					lastSync = time.Now()
					continue
				}
				// 空闲兜底：无按键、未在跑任务、未流式输出、不在拷贝模式
				// （拷贝模式重画会重置选区与光标，绝不能打扰）。
				if m.loading || m.streamBusy() || m.pasteBurstActive() || m.copyMode {
					continue
				}
				idle := time.Since(m.lastKeyAt)
				if m.lastKeyAt.IsZero() {
					idle = screenIdleResync
				}
				if idle >= screenIdleResync && time.Since(lastSync) >= screenMinSyncGap {
					m.requestResyncFromGoroutine()
					lastSync = time.Now()
				}
			}
		}
	}()
}

// =============================================================================
// 事件循环卡顿看门狗（「TUI 卡住、键盘全哑」的现场取证）
// =============================================================================
//
// 现象：TUI 用着用着突然不接受任何输入，但进程还活着（WebUI 照常可用）。
// 根因永远是「tcell/tview 事件循环线程被某段代码堵住」——可能是同步跑外部命令、
// 阻塞式网络请求、或 QueueUpdate 自死锁。这类问题只在特定环境（内网慢盘、
// 大仓库、慢终端）复现，事后无法回溯。
//
// 对策：独立 goroutine 定期往事件循环里投一个空更新，量「多久才被执行」。
// 超过阈值就说明事件循环被占住了 —— 立刻 dump 全部 goroutine 栈到诊断日志，
// 下次卡住时 ~/.mizar/logs 里就有现场（谁在堵、堵在哪一行）。
// 开销：每 loopLagInterval 一次空回调，纳秒级，可忽略。

const (
	loopLagInterval = 3 * time.Second
	// loopLagThreshold 事件循环多久没响应算「卡住」。
	// 3 秒已经远超任何正常渲染耗时（statusBarDirect 缓存后是微秒级），
	// 取值为阈值的一半：每 3s 探测一次，连续 2 次没响应即 6s，够稳也不误报。
	loopLagThreshold = 6 * time.Second
	// loopLagStackCap 栈 dump 上限（防止超长栈把日志撑爆）。
	loopLagStackCap = 256 << 10
)

// startLoopLagWatchdog 启动事件循环看门狗（Run 内调用一次，随 watchStop 退出）。
func (m *tuiModel) startLoopLagWatchdog() {
	stop := m.watchStop
	if stop == nil {
		return
	}
	// probePending/probeAt 用原子量：写入发生在事件循环线程（回调里），
	// 读取发生在看门狗 goroutine，必须避免数据竞争。
	var probePending atomic.Bool
	var probeAt atomic.Int64 // UnixNano
	dumped := &atomic.Bool{}
	go func() {
		ticker := time.NewTicker(loopLagInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if probePending.Load() {
					d := time.Since(time.Unix(0, probeAt.Load()))
					if d >= loopLagThreshold && !dumped.Swap(true) {
						buf := make([]byte, loopLagStackCap)
						n := runtime.Stack(buf, true)
						diag.Logf("tui", "事件循环已 %.1fs 无响应（疑似 TUI 卡住），goroutine 栈：\n%s",
							d.Seconds(), string(buf[:n]))
					}
					continue
				}
				probePending.Store(true)
				probeAt.Store(time.Now().UnixNano())
				go func() {
					// QueueUpdate 会阻塞到回调在事件循环里执行完；
					// 事件循环正常时这里几乎立刻返回。
					m.app.QueueUpdate(func() {
						probePending.Store(false)
						dumped.Store(false) // 恢复响应：下次卡住可再取证
					})
				}()
			}
		}
	}()
}

// stopScreenWatchdog 停止看门狗（Run 退出时调用）。
func (m *tuiModel) stopScreenWatchdog() {
	if m.watchStop != nil {
		close(m.watchStop)
		m.watchStop = nil
	}
}

// streamBusy 报告是否正在流式输出（看门狗据此决定是否打扰）。
func (m *tuiModel) streamBusy() bool {
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	return m.streamActive
}

// sizeChanged 判断终端尺寸是否变化（纯函数，便于测试）。
func sizeChanged(curW, curH, lastW, lastH int) bool {
	return curW != lastW || curH != lastH
}
