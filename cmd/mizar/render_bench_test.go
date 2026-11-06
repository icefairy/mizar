package main

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
)

// mkTUIForBench 构造带 N 条长消息的 TUI（模拟真实长会话渲染压力）。
func mkTUIForBench(n int, msgLen int) *tuiModel {
	m := newTuiModel(&agent.Agent{}, nil, "bench", nil)
	m.lines = m.lines[:0]
	for i := 0; i < n; i++ {
		body := strings.Repeat("- 这是一段用于渲染压测的列表项内容，含中文与 `code`。\n", msgLen/40+1)
		m.lines = append(m.lines, chatLine{role: "bot", content: body, ts: time.Now()})
	}
	return m
}

// BenchmarkRenderAllDirect 冷渲染（无 Markdown 缓存）——模拟首批重绘。
func BenchmarkRenderAllDirectCold(b *testing.B) {
	m := mkTUIForBench(30, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range m.lines { // 清缓存，强制重跑 glamour
			m.lines[j].mdCacheOK = false
		}
		m.renderAllDirect()
	}
}

// BenchmarkRenderAllDirectCached 命中 Markdown 缓存（稳态重绘，任务运行中常态）。
func BenchmarkRenderAllDirectCached(b *testing.B) {
	m := mkTUIForBench(30, 2000)
	m.renderAllDirect() // 预热缓存
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.renderAllDirect()
	}
}

// TestRenderPerfBudget 稳态重绘必须在 10ms 内完成（否则 60~100ms 一次的
// 流式/bash 刷新就会把事件循环占满，用户按键明显发涩）。
func TestRenderPerfBudget(t *testing.T) {
	m := mkTUIForBench(30, 2000)
	m.renderAllDirect()
	start := time.Now()
	const rounds = 20
	for i := 0; i < rounds; i++ {
		m.renderAllDirect()
	}
	avg := time.Since(start) / rounds
	t.Logf("稳态 renderAllDirect 平均耗时: %v（%d 条消息）", avg, len(m.lines))
	if avg > 10*time.Millisecond {
		t.Fatalf("稳态重绘过慢: %v > 10ms，事件循环会被占满", avg)
	}
}
