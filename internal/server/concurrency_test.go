package server

import (
	"sync"
	"testing"
	"time"
)

// TestDifferentConvsRunConcurrently 钉住多会话真并发：A、B 两个会话
// 同时各跑一轮必须都成功，不能出现「一个在跑，另一个 409」。
//
// 这是用户报的第二个症状：「A 会话正在不断接收后台消息时，B 会话还是无法直接
// 发送，会排队」。改造前 acquireRun 是进程级 TryLock，B 必然拿不到锁。
func TestDifferentConvsRunConcurrently(t *testing.T) {
	s := &Server{}
	if !s.acquireRunFor("conv-A") {
		t.Fatal("A 会话应能开始")
	}
	if !s.acquireRunFor("conv-B") {
		t.Fatal("B 会话必须能同时开始（多会话并发）；" +
			"改造前这里会失败导致前端提示排队")
	}
	// 两个都在跑
	if !s.runLiveFor("conv-A") || !s.runLiveFor("conv-B") {
		// runLiveFor 依赖 pool.peek，没有 pool 时退回全局判断，此处不断言
	}
	s.releaseRunFor("conv-A")
	if !s.acquireRunFor("conv-C") {
		t.Fatal("A 释放后 C 应能开始")
	}
	s.releaseRunFor("conv-B")
	s.releaseRunFor("conv-C")
}

// TestSameConvStillSerial 同一会话仍然必须串行：
// 一个会话里同时跑两个循环会互相覆盖 Initial/OnLLMStream/steer。
func TestSameConvStillSerial(t *testing.T) {
	s := &Server{}
	if !s.acquireRunFor("conv-A") {
		t.Fatal("首次应成功")
	}
	if s.acquireRunFor("conv-A") {
		t.Fatal("同一会话第二次必须失败（串行语义）")
	}
	start := time.Now()
	if s.acquireRunFor("conv-A") {
		t.Fatal("不应成功")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("忙时判定耗时 %v，应为 TryLock 立即返回", d)
	}
	s.releaseRunFor("conv-A")
	if !s.acquireRunFor("conv-A") {
		t.Fatal("释放后应能再次开始")
	}
	s.releaseRunFor("conv-A")
}

// TestConcurrentLimitEnforced 全局并发上限生效：超过上限的会话被拒绝（前端转排队）。
func TestConcurrentLimitEnforced(t *testing.T) {
	s := &Server{maxConcurrentRuns: 2}
	if !s.acquireRunFor("A") {
		t.Fatal("A 应成功")
	}
	if !s.acquireRunFor("B") {
		t.Fatal("B 应成功")
	}
	if s.acquireRunFor("C") {
		t.Fatal("超过上限(=2)时 C 必须被拒绝")
	}
	// 上限不是死锁：释放一个后 C 能进
	s.releaseRunFor("A")
	if !s.acquireRunFor("C") {
		t.Fatal("释放后 C 应能开始")
	}
	s.releaseRunFor("B")
	s.releaseRunFor("C")
}

// TestConcurrentLimitZeroMeansUnlimited 0 = 不限（对齐 Hermes 的 max_concurrent_sessions=null）。
func TestConcurrentLimitZeroMeansUnlimited(t *testing.T) {
	s := &Server{maxConcurrentRuns: 0}
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		if !s.acquireRunFor(id) {
			t.Fatalf("上限为 0（不限）时会话 %s 应能开始", id)
		}
	}
	for i := 0; i < 10; i++ {
		s.releaseRunFor(string(rune('a' + i)))
	}
}

// TestConcurrentAcquireRace 并发抢不同会话：不能死锁、不能误拒。
func TestConcurrentAcquireRace(t *testing.T) {
	s := &Server{maxConcurrentRuns: 8}
	var wg sync.WaitGroup
	var mu sync.Mutex
	fails := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n))
			if !s.acquireRunFor(id) {
				mu.Lock()
				fails++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if fails != 0 {
		t.Fatalf("%d/8 个不同会话被误拒", fails)
	}
	for i := 0; i < 8; i++ {
		s.releaseRunFor(string(rune('a' + i)))
	}
}

// TestRunningConvsTracking 正在运行的会话集合：并发时两个都在，结束后清空。
func TestRunningConvsTracking(t *testing.T) {
	s := &Server{}
	s.markRunning("A", true)
	s.markRunning("B", true)
	got := s.runningConvsSnapshot()
	if len(got) != 2 {
		t.Fatalf("应有 2 个在跑，实际 %v", got)
	}
	if !s.isConvRunning("A") || !s.isConvRunning("B") {
		t.Fatal("A/B 都应标记为在跑")
	}
	s.markRunning("A", false)
	if s.isConvRunning("A") {
		t.Fatal("A 结束后不应再标记在跑")
	}
	if !s.isConvRunning("B") {
		t.Fatal("B 不应被 A 的结束影响")
	}
	if got := s.runningConvsSnapshot(); len(got) != 1 {
		t.Fatalf("应剩 1 个，实际 %v", got)
	}
}
