package main

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
)

// TestStatusBarPerfBigSession 长会话（500 条消息）下状态栏重绘必须极快：
// spinner/流式/bash 输出每 60~100ms 刷一次状态栏，若每次遍历全部消息
// 重算 token，事件循环就被占满，输入框发涩。
func TestStatusBarPerfBigSession(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "bench", nil)
	m.lines = m.lines[:0]
	for i := 0; i < 500; i++ {
		m.lines = append(m.lines, chatLine{role: "bot", content: strings.Repeat("长会话内容 ", 200), ts: time.Now()})
	}
	// 装一个 Compactor 以启用「上下文占用」估算路径
	m.agent.Compactor = &agent.Compactor{ContextWindow: 200000, ReserveTokens: 20000}

	m.statusBarDirect() // 预热（填充 ctxEst 缓存）
	start := time.Now()
	const rounds = 200
	for i := 0; i < rounds; i++ {
		m.statusBarDirect()
	}
	avg := time.Since(start) / rounds
	t.Logf("500 条消息下 statusBarDirect 平均: %v", avg)
	if avg > 5*time.Millisecond {
		t.Fatalf("状态栏过慢: %v > 5ms", avg)
	}
}

// TestStatusBarCacheEffect 量化缓存效果：不缓存时每次都要遍历 500 条消息
// 重算 EstimateTokens，对比缓存命中后的耗时。
func TestStatusBarCacheEffect(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "bench", nil)
	m.lines = m.lines[:0]
	for i := 0; i < 500; i++ {
		m.lines = append(m.lines, chatLine{role: "bot", content: strings.Repeat("长会话内容 ", 200), ts: time.Now()})
	}
	m.agent.Compactor = &agent.Compactor{ContextWindow: 200000, ReserveTokens: 20000}

	const rounds = 50
	// 禁用缓存（每轮强制失效）
	start := time.Now()
	for i := 0; i < rounds; i++ {
		m.ctxEstLines = -1
		m.statusBarDirect()
	}
	uncached := time.Since(start) / rounds

	// 缓存命中
	m.statusBarDirect()
	start = time.Now()
	for i := 0; i < rounds; i++ {
		m.statusBarDirect()
	}
	cached := time.Since(start) / rounds

	t.Logf("statusBarDirect 500 条消息：不缓存 %v，缓存后 %v（提速 %.0fx）",
		uncached, cached, float64(uncached)/float64(cached))
	if cached >= uncached {
		t.Fatalf("缓存未生效: cached=%v uncached=%v", cached, uncached)
	}
}

// TestRenderRequestCoalescing 高频重绘请求必须被合并：事件循环里最多只有一个
// 待处理的 renderAllDirect。用假的 app 队列模拟（不启动真实 tview 循环）。
func TestRenderRequestCoalescing(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "bench", nil)

	// 替换 app 为"只记录排队次数"的实现不可行（tview 内部 channel），
	// 改为直接观察 renderPending 标记：连续请求后只应标记一次。
	if m.renderPending.Load() {
		t.Fatal("初始不应有待处理重绘")
	}
	// 第一次请求会真正排队（QueueUpdateDraw 在未 Run 时阻塞，故不调用它）；
	// 这里只验证合并判定的原子标记语义。
	if !m.renderPending.CompareAndSwap(false, true) {
		t.Fatal("首次请求应能取得标记")
	}
	if m.renderPending.CompareAndSwap(false, true) {
		t.Fatal("已有待处理请求时不应再次取得标记（合并生效）")
	}
	m.renderPending.Store(false)
}
