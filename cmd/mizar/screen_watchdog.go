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
