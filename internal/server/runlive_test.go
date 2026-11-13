package server

import (
	"sync"
	"testing"
	"time"
)

// TestRunLiveCoversAcquireToRunWindow 钉住「打断模式下纠正被误判为排队」的根因。
//
// 现场症状：默认「打断」模式，用户发出的消息后端已经收到并开始处理，
// 前端却提示「已排队，本轮结束后发送」。
//
// 根因：runMu（acquireRun）管的是整轮 HTTP 请求的生命周期，
// 而 agent.Running() 读的 runActive 只在 agent.Run() 体内才置 true。
// 两者之间存在窗口：acquireRun 已成功、setRunConv 已设、消息已受理，
// 但 Run 还没跑到 runActive.Store(true)。此时 steer 查 agent.Running()
// 得到 false，按「没有活跃循环」处理 → 前端把这条纠正当成排队消息。
//
// 修法：acquireRun 成功即置 busyRun，runLive() = Running() || busyRun，
// 所有判断活跃的地方一律用 runLive()。
func TestRunLiveCoversAcquireToRunWindow(t *testing.T) {
	s := &Server{}

	// 起点：真的空闲
	if s.runLive() {
		t.Fatal("空闲时 runLive() 应为 false")
	}

	// acquireRun 成功 —— 这一轮已经开始受理，但 agent.Run() 还没执行
	if !s.acquireRun() {
		t.Fatal("首次 acquireRun 应成功")
	}
	if !s.runLive() {
		t.Fatal("acquireRun 之后、Run() 之前必须判定为「在跑」；" +
			"否则用户在这段窗口里发的纠正会被误判成排队")
	}

	// 窗口期并发拿锁必须失败（保持单实例语义不变）
	if s.acquireRun() {
		t.Fatal("已有一轮在跑时 acquireRun 必须失败")
	}

	// 释放后回到空闲
	s.releaseRun()
	if s.runLive() {
		t.Fatal("releaseRun 之后 runLive() 应为 false")
	}

	// 释放后能再次抢锁（不能因为 busyRun 卡死）
	if !s.acquireRun() {
		t.Fatal("释放后应能再次 acquireRun")
	}
	s.releaseRun()
}

// TestRunLiveConcurrentSendsDuringWindow 并发压测：窗口期内多个 goroutine
// 同时判断活跃，必须全部看到「在跑」，不能有任何一个看到 false。
func TestRunLiveConcurrentSendsDuringWindow(t *testing.T) {
	s := &Server{}
	if !s.acquireRun() {
		t.Fatal("acquireRun 失败")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	falseCount := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.runLive() {
				mu.Lock()
				falseCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	s.releaseRun()
	if falseCount != 0 {
		t.Fatalf("%d/50 次判断误认为空闲（会提示排队）", falseCount)
	}
}

// TestAcquireRunRejectsSecondWhileHeld 忙时立即拒绝（保持 409 语义，不排队阻塞）。
func TestAcquireRunRejectsSecondWhileHeld(t *testing.T) {
	s := &Server{}
	if !s.acquireRun() {
		t.Fatal("首次应成功")
	}
	start := time.Now()
	if s.acquireRun() {
		t.Fatal("第二次不应成功")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("忙时判定耗时 %v，应为立即返回（TryLock 语义）", d)
	}
	s.releaseRun()
}
