package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
)

// newTUIForStatusTest 构造状态栏测试用 tuiModel（带 Compactor，原生复用与生产一致）。
func newTUIForStatusTest() *tuiModel {
	m := newTUIForQueueTest()
	m.agent.Compactor = agent.DefaultCompactor(nil)
	return m
}

// TestStatusBarStreamingShowsLiveStats 流式回复中状态栏应展示：实时输入/输出估算、
// 实时 tok/s、「上下文:」标签且计入当前流式输出后的比例高于不计入时。
func TestStatusBarStreamingShowsLiveStats(t *testing.T) {
	m := newTUIForStatusTest()
	m.lines = append(m.lines,
		chatLine{role: "user", content: "帮我写一段介绍"},
		chatLine{role: "bot", content: "好的，开阳是一个……"},
	)
	m.stats.CumulativeTotalTokens = 1000
	m.stats.CumulativePromptTokens = 600
	m.stats.CumulativeCompletionTokens = 400

	// 模拟流式中：本步已输出 60 个 CJK 字符（≈60 tok）、输入估算 500
	m.streamMu.Lock()
	m.streamActive = true
	m.streamStepStart = now()
	m.streamPromptEst = 500
	m.streamOutCJK = 60
	m.streamOutStart = now()
	m.streamMu.Unlock()

	m.statusBarDirect()
	txt := m.statusBar.GetText(true)

	if !strings.Contains(txt, "上下文:") {
		t.Errorf("status bar should show 上下文 tag: %q", txt)
	}
	if strings.Contains(txt, "压缩:") {
		t.Errorf("status bar should not show old 压缩 tag: %q", txt)
	}
	if !strings.Contains(txt, "本步 in≈500 out≈60") {
		t.Errorf("status bar should show streaming in/out estimate: %q", txt)
	}
	if !strings.Contains(txt, "tok/s") {
		t.Errorf("status bar should show live tok/s: %q", txt)
	}
}

// TestStatusBarContextPctGrowsWithStream 流式输出计入上下文估算：流中存在输出时，
// 「上下文」比例应大于（等于）无流式输出时的比例，验证回复中该数值持续增长。
func TestStatusBarContextPctGrowsWithStream(t *testing.T) {
	m := newTUIForStatusTest()
	m.lines = append(m.lines, chatLine{role: "user", content: "你好"})

	pct := func(withStream int) float64 {
		m.streamMu.Lock()
		if withStream > 0 {
			m.streamActive = true
			m.streamOutCJK = withStream
		} else {
			m.streamActive = false
			m.streamOutCJK = 0
		}
		m.streamMu.Unlock()
		m.statusBarDirect()
		s := m.statusBar.GetText(true)
		// 解析 “上下文: NN%”
		idx := strings.Index(s, "上下文: ")
		if idx < 0 {
			t.Fatalf("no 上下文 column in %q", s)
		}
		var p float64
		if _, err := fmt.Sscanf(s[idx+len("上下文: "):], "%f%%", &p); err != nil {
			t.Fatalf("parse pct from %q: %v", s, err)
		}
		return p
	}
	base := pct(0)
	with := pct(4000) // 模拟完成了一条较长的流式回复
	if with <= base {
		t.Fatalf("上下文占用应随流式输出增长: base=%.1f%% withStream=%.1f%%", base, with)
	}
}

// 在测试文件内补最小 date 时间：调用真实 time。
func now() time.Time { return time.Now() }
