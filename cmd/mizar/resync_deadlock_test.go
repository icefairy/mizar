package main

// 回归测试：强制重绘绝不能把「重绘请求」再排进 tview 的更新队列。
//
// 事故：Ctrl+L / /redraw 调 tuiRequestResync() → m.app.QueueUpdate(...)，
// 而这两条路径本身就运行在 tview 的事件循环线程里（InputCapture / submitInput）。
// tview 的 QueueUpdate 语义是「把函数投进队列后阻塞等待它执行完」，事件循环此时
// 正卡在这个回调里，永远不会回到 select 去消费队列 → TUI 整体死锁，键盘全哑。
//
// 结论：事件循环线程内必须同步执行重绘（tview 明确允许在 queued update 与
// 直接事件处理中调用 ForceDraw），只有其他 goroutine 才通过 QueueUpdate 排队。

import (
	"sync/atomic"
	"testing"
	"time"

	"mizar/internal/agent"
)

// TestResyncRequestRunsInline resyncRequest 必须同步执行重绘，不得排队等待。
func TestResyncRequestRunsInline(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "test-resync-inline", nil)
	var ran atomic.Bool
	// 替换真正的重绘为标记函数：resyncRequest 若排队则根本不会执行到这里
	// （测试里没有运行中的事件循环），若内联执行则立即置位。
	m.resyncScreenFn = func() { ran.Store(true) }

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.resyncRequest()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resyncRequest 阻塞了 —— 疑似又走了 tview 的 QueueUpdate（会与事件循环死锁）")
	}
	if !ran.Load() {
		t.Fatal("resyncRequest 应同步执行重绘")
	}
}

// TestWatchdogQueuesFromGoroutine 看门狗在 goroutine 中运行，必须排队到事件循环。
func TestWatchdogQueuesFromGoroutine(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "test-resync-queue", nil)
	var queued, ran atomic.Bool
	m.queueUpdateFn = func(f func()) { queued.Store(true) }
	m.resyncScreenFn = func() { ran.Store(true) }

	m.requestResyncFromGoroutine()
	if !queued.Load() {
		t.Fatal("从 goroutine 请求重绘必须投递到事件循环队列")
	}
	if ran.Load() {
		t.Fatal("从 goroutine 不应直接重绘（tview 控件操作要求事件循环线程）")
	}
}
