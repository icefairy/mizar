package main

import (
	"log"
	"testing"

	"mizar/internal/agent"
)

// TestSizeChanged 尺寸变化判定（看门狗据此触发整屏重画）。
func TestSizeChanged(t *testing.T) {
	if sizeChanged(120, 40, 120, 40) {
		t.Fatal("尺寸相同不应判定为变化")
	}
	if !sizeChanged(120, 40, 100, 40) {
		t.Fatal("宽度变化应判定为变化")
	}
	if !sizeChanged(120, 40, 120, 30) {
		t.Fatal("高度变化应判定为变化")
	}
	if !sizeChanged(120, 40, 0, 0) {
		t.Fatal("首次测量（上次为 0）应判定为变化")
	}
}

// TestResyncScreenRebuildsContent 整屏重画必须重建聊天区/状态栏内容
// （只 Sync 不重建的话，CellBuffer 里仍是旧内容，等于把旧画面又刷一遍）。
func TestResyncScreenRebuildsContent(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "test-sync", nil)
	m.lines = nil
	m.lines = append(m.lines, chatLine{role: "bot", content: "重画后的内容"})
	m.resyncScreen()
	text := m.textView.GetText(true)
	if text == "" {
		t.Fatal("整屏重画后聊天区应有内容")
	}
}

// TestTUIOutputDisciplineRedirectsLog TUI 期间标准 log 不得写终端（否则会盖出
// 永远擦不掉的残留文字），且内容仍要落进诊断日志。
func TestTUIOutputDisciplineRedirectsLog(t *testing.T) {
	prev := log.Writer()
	defer log.SetOutput(prev)
	enableTUIOutputDiscipline()
	defer disableTUIOutputDiscipline()

	if !tuiOutputActive() {
		t.Fatal("enableTUIOutputDiscipline 后 tuiOutputActive 应为 true")
	}
	// 写入后不应 panic，也不应出现在 stderr（diagWriter 吞掉）。
	log.Printf("看门狗测试：这条不能上屏")
	disableTUIOutputDiscipline()
	if tuiOutputActive() {
		t.Fatal("disableTUIOutputDiscipline 后 tuiOutputActive 应为 false")
	}
}

// TestResyncScreenWatchdogStops 看门狗可重复启停而不泄漏/panic。
func TestResyncScreenWatchdogStops(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "test-wd", nil)
	m.startScreenWatchdog()
	m.stopScreenWatchdog()
	m.stopScreenWatchdog() // 幂等
}
