package main

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/agent"
)

// newTUIForPasteTest 构造启用粘贴攒批的 TUI。
func newTUIForPasteTest(t *testing.T) *tuiModel {
	t.Helper()
	m := newTuiModel(&agent.Agent{}, nil, "test-paste", nil)
	m.pasteBurst = true
	return m
}

// TestPasteBurstMergesIntoOneWrite 快速连发的字符应被攒批，且在空闲后才
// 一次性写入输入框——这正是「粘贴不再一个字一个字地蹦」的核心保证。
func TestPasteBurstMergesIntoOneWrite(t *testing.T) {
	m := newTUIForPasteTest(t)
	cap := m.app.GetInputCapture()

	// 第一个字符按正常路径处理（此处直接调用 InputCapture，不会真的写进
	// 输入框，故手工模拟它已落盘），并记录按键时间作为「上一个按键」。
	if ret := cap(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)); ret == nil {
		t.Fatal("首个字符应交给输入框正常处理")
	}
	m.inputField.SetText("a", true)
	// 后续字符间隔极小 → 判定为粘贴，全部吞下
	for _, r := range "bcdefg" {
		if ret := cap(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)); ret != nil {
			t.Fatalf("粘贴字符 %q 应被缓冲，got %v", string(r), ret)
		}
	}
	if !m.pastePending.Load() {
		t.Fatal("应处于攒批状态")
	}
	// 攒批期间输入框里只有第一字符（其余还没落盘）
	if got := m.inputField.GetText(); got != "a" {
		t.Fatalf("攒批期间输入框应仍为 %q, got %q", "a", got)
	}
	// 载入并检查缓冲内容
	if m.pasteBuf.String() != "bcdefg" {
		t.Fatalf("缓冲内容 = %q, want %q", m.pasteBuf.String(), "bcdefg")
	}
	// 手工触发落盘（等价于空闲定时器到点）
	m.flushPasteBurst()
	if got := m.inputField.GetText(); got != "abcdefg" {
		t.Fatalf("落盘后输入框 = %q, want %q", got, "abcdefg")
	}
	if m.pastePending.Load() {
		t.Fatal("落盘后应退出攒批状态")
	}
}

// TestPasteBurstNewlinesKept 粘贴里的换行不能被当成「提交」，而要保留为换行。
func TestPasteBurstNewlinesKept(t *testing.T) {
	m := newTUIForPasteTest(t)
	cap := m.app.GetInputCapture()

	cap(tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone)) // 首个字符走正常路径
	m.inputField.SetText("1", true)
	if ret := cap(tcell.NewEventKey(tcell.KeyRune, '2', tcell.ModNone)); ret != nil {
		t.Fatal("第二个字符应进入攒批")
	}
	if ret := cap(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); ret != nil {
		t.Fatal("粘贴中的换行应被吞掉（不提交）")
	}
	if ret := cap(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone)); ret != nil {
		t.Fatal("换行后的字符应继续攒批")
	}
	m.flushPasteBurst()
	if got := m.inputField.GetText(); got != "12\n3" {
		t.Fatalf("输入框 = %q, want %q", got, "12\n3")
	}
	if m.agent.Aborted() {
		t.Fatal("粘贴不应取消任务")
	}
}

// TestPasteBurstHotkeyNotSwallowed 攒批期间快捷键必须立即生效、不被并入缓冲。
func TestPasteBurstHotkeyNotSwallowed(t *testing.T) {
	m := newTUIForPasteTest(t)
	cap := m.app.GetInputCapture()

	cap(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	m.inputField.SetText("x", true)
	cap(tcell.NewEventKey(tcell.KeyRune, 'y', tcell.ModNone))
	// Alt+K（上滚）不是普通字符 → 应先落盘，再交给快捷键处理（滚动键被消费，返回 nil）
	cap(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModAlt))
	if m.pastePending.Load() {
		t.Fatal("快捷键到达时应已落盘")
	}
	if got := m.inputField.GetText(); got != "xy" {
		t.Fatalf("落盘内容 = %q, want %q", got, "xy")
	}
}

// TestPasteBurstEnabledFor 组装平台判定：Windows 默认开，Unix 默认关（有 bracketed paste）。
func TestPasteBurstEnabledFor(t *testing.T) {
	noEnv := func(string) string { return "" }
	if !pasteBurstEnabledFor("windows", noEnv) {
		t.Fatal("Windows 应默认启用粘贴攒批")
	}
	if pasteBurstEnabledFor("linux", noEnv) {
		t.Fatal("Unix 已有 bracketed paste，不应攒批（否则会误并快速输入）")
	}
	env := func(k string) string { return "0" }
	if pasteBurstEnabledFor("windows", env) {
		t.Fatal("MIZAR_PASTE_BURST=0 应关闭")
	}
	env = func(k string) string { return "1" }
	if !pasteBurstEnabledFor("linux", env) {
		t.Fatal("MIZAR_PASTE_BURST=1 应强制开启")
	}
}

// TestPasteBurstGapWindows 阈值合理性：起点窗口要极短（人手打字不会误判），
// 空闲落盘延迟要短到用户无感。
func TestPasteBurstGapWindows(t *testing.T) {
	if pasteBurstStartGap > 15*time.Millisecond {
		t.Fatalf("pasteBurstStartGap=%v 太长，会误并人手快速输入", pasteBurstStartGap)
	}
	if pasteFlushDelay > 60*time.Millisecond {
		t.Fatalf("pasteFlushDelay=%v 太长，粘贴后要等一下才看到文字", pasteFlushDelay)
	}
	if pasteFlushDelay < 5*time.Millisecond {
		t.Fatalf("pasteFlushDelay=%v 太短，攒批几乎失效", pasteFlushDelay)
	}
}

// fakeScreen 只记录 Show 次数的最小 tcell.Screen 实现（其余方法由嵌入接口提供）。
type fakeScreen struct {
	tcell.Screen
	shows int
}

func (f *fakeScreen) Show() { f.shows++ }

// TestPasteSkipScreenSuppressesShow 攒批期间 Show() 必须被吞掉（否则每个字符都
// 全屏刷一次终端，就是「一个字一个字地蹦」）；攒批结束后恢复正常输出。
func TestPasteSkipScreenSuppressesShow(t *testing.T) {
	base := &fakeScreen{}
	active := true
	s := &pasteSkipScreen{Screen: base, skip: func() bool { return active }}

	for i := 0; i < 50; i++ {
		s.Show() // 模拟粘贴期间的 50 次中间态重绘
	}
	if base.shows != 0 {
		t.Fatalf("攒批期间不应真正输出屏幕, got %d 次", base.shows)
	}
	if !s.TookDeferred() {
		t.Fatal("应记录「攒批期间吞掉过输出」")
	}
	active = false
	s.Show()
	if base.shows != 1 {
		t.Fatalf("攒批结束后应输出一次, got %d 次", base.shows)
	}
	if s.TookDeferred() {
		t.Fatal("恢复输出后应清除延迟标记")
	}
}

// TestResyncScreenNilSafe resyncScreen 在屏幕尚未装配时必须安全（测试/降级路径）。
func TestResyncScreenNilSafe(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "test-resync", nil)
	m.resyncScreen() // 不应 panic
}

// TestPasteSkipScreenSyncPasses 攒批期间 Sync 也要透传（否则残留永远清不掉）。
func TestPasteSkipScreenSyncPasses(t *testing.T) {
	base := &syncCounterScreen{}
	active := false
	s := &pasteSkipScreen{Screen: base, skip: func() bool { return active }}
	s.Sync()
	if base.syncs != 1 {
		t.Fatalf("Sync 应透传到底层屏幕, got %d", base.syncs)
	}
}

// syncCounterScreen 统计 Sync 调用次数。
type syncCounterScreen struct {
	tcell.Screen
	syncs int
}

func (s *syncCounterScreen) Sync() { s.syncs++ }
