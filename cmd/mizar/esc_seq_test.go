package main

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/agent"
)

// newTUIForSeqTest 构造启用「原始 VT 序列解码」的 TUI。
func newTUIForSeqTest(t *testing.T) *tuiModel {
	t.Helper()
	m := newTuiModel(&agent.Agent{}, nil, "test-seq", nil)
	m.escSeqDecode = true
	return m
}

// keysOf 直接把按键送进「序列解码层」而不是完整 InputCapture：
// 后者会继续做滚动/历史浏览等正常处理（Up 未运行时会被历史浏览消费、Alt+K 会被
// 滚动消费），那属于别处的既有行为，不应该混进解码器的断言。
func keysOf(m *tuiModel, evs ...*tcell.EventKey) []*tcell.EventKey {
	out := make([]*tcell.EventKey, 0, len(evs))
	for _, ev := range evs {
		handled, repl := m.escSeqKey(ev, time.Now())
		if !handled {
			out = append(out, ev)
			continue
		}
		out = append(out, repl) // nil = 被解码器吞掉
	}
	return out
}

// TestEscSeqArrowLeft 复现用户报告：按左箭头时 tcell 送 ESC / '[' / 'D'，
// 必须被合成成 KeyLeft —— 绝不能让 "[D" 落进输入框。
func TestEscSeqArrowLeft(t *testing.T) {
	m := newTUIForSeqTest(t)
	got := keysOf(m,
		tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, 'D', tcell.ModNone),
	)
	if got[0] != nil {
		t.Fatalf("Esc 应被解码器接管（等窗口确认是否序列开头），got %v", got[0])
	}
	if got[1] != nil {
		t.Fatalf("'[' 应被吞掉（属于序列），got %v", got[1])
	}
	if got[2] == nil || got[2].Key() != tcell.KeyLeft {
		t.Fatalf("最后一个事件应是 KeyLeft，got %v", got[2])
	}
	if m.escSeqBuf != nil {
		t.Fatalf("解析完成后缓冲应清空, got %q", string(m.escSeqBuf))
	}
}

// TestEscSeqAllArrowsAndNav 方向键 + Home/End/PgUp/PgDn/Delete 的常见编码。
func TestEscSeqAllArrowsAndNav(t *testing.T) {
	cases := []struct {
		seq  string
		want tcell.Key
	}{
		{"[A", tcell.KeyUp},
		{"[B", tcell.KeyDown},
		{"[C", tcell.KeyRight},
		{"[D", tcell.KeyLeft},
		{"[H", tcell.KeyHome},
		{"[F", tcell.KeyEnd},
		{"[1~", tcell.KeyHome},
		{"[2~", tcell.KeyInsert},
		{"[3~", tcell.KeyDelete},
		{"[4~", tcell.KeyEnd},
		{"[5~", tcell.KeyPgUp},
		{"[6~", tcell.KeyPgDn},
		{"[15~", tcell.KeyF5},
		{"[24~", tcell.KeyF12},
		{"OA", tcell.KeyUp},
		{"OD", tcell.KeyLeft},
	}
	for _, c := range cases {
		m := newTUIForSeqTest(t)
		evs := []*tcell.EventKey{tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)}
		for _, r := range c.seq {
			evs = append(evs, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		}
		got := keysOf(m, evs...)
		last := got[len(got)-1]
		if last == nil || last.Key() != c.want {
			t.Fatalf("序列 ESC%s → %v, want key %d", c.seq, last, c.want)
		}
	}
}

// TestEscSeqCtrlArrowModifiers ESC[1;5C = Ctrl+右箭头：必须带上 ModCtrl。
func TestEscSeqCtrlArrowModifiers(t *testing.T) {
	m := newTUIForSeqTest(t)
	got := keysOf(m,
		tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, ';', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, '5', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModNone),
	)
	last := got[len(got)-1]
	if last == nil || last.Key() != tcell.KeyRight {
		t.Fatalf("ESC[1;5C 应识别为 KeyRight, got %v", last)
	}
	if last.Modifiers()&tcell.ModCtrl == 0 {
		t.Fatalf("ESC[1;5C 应带 ModCtrl, got mod=%v", last.Modifiers())
	}
}

// TestEscSeqAltLetter ESC k（Alt+K，本程序用于上滚）不能被当成 "ESC" + "k"。
func TestEscSeqAltLetter(t *testing.T) {
	m := newTUIForSeqTest(t)
	// tcell 在部分终端会自己合成 Alt+字符
	_, ev := m.escSeqKey(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModAlt), time.Now())
	if ev == nil || ev.Key() != tcell.KeyRune || ev.Rune() != 'k' || ev.Modifiers()&tcell.ModAlt == 0 {
		t.Fatalf("Alt+K 应原样交给 tview, got %v", ev)
	}
	// 原始序列路径：ESC 紧跟 'k'（间隔为 0）→ 合成 Alt+k
	m2 := newTUIForSeqTest(t)
	h1, r1 := m2.escSeqKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone), time.Now())
	if !h1 || r1 != nil {
		t.Fatalf("Esc 应先被解码器接管（等窗口确认是否序列开头）")
	}
	_, ev2 := m2.escSeqKey(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModNone), time.Now())
	if ev2 == nil || ev2.Key() != tcell.KeyRune || ev2.Rune() != 'k' || ev2.Modifiers()&tcell.ModAlt == 0 {
		t.Fatalf("ESC k 应合成 Alt+K, got %v", ev2)
	}
}

// TestEscSeqLeakSwallowed ESC[<35;66;61M（ConPTY 泄漏的滚轮）必须整段吞掉。
func TestEscSeqLeakSwallowed(t *testing.T) {
	m := newTUIForSeqTest(t)
	evs := []*tcell.EventKey{tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)}
	for _, r := range "[<35;66;61M" {
		evs = append(evs, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	for i, got := range keysOf(m, evs...) {
		if got != nil {
			t.Fatalf("第 %d 个事件应被吞掉（泄漏序列），got %v", i, got)
		}
	}
	if m.escLastJunkAt.IsZero() {
		t.Fatal("应记录「最近见过泄漏序列」")
	}
	if !m.recentLeak() {
		t.Fatal("recentLeak 应为 true")
	}
}

// TestEscSeqUnknownKeepsInput 认不出的序列绝不能吞掉用户输入：
// ESC [ ? 25 h 之类不能被静默丢弃（至少 '?25h' 要回到输入框）。
func TestEscSeqUnknownKeepsInput(t *testing.T) {
	m := newTUIForSeqTest(t)
	post := func(ev *tcell.EventKey) { m.escSeqKey(ev, time.Now()) }
	post(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	post(tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModNone))
	post(tcell.NewEventKey(tcell.KeyRune, '?', tcell.ModNone))
	_, got := m.escSeqKey(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone), time.Now())
	// '?' 与 'h' 之间不构成已知序列 → 认不出 → 应把原文交回
	if got == nil {
		t.Fatal("认不出的序列不应静默吞掉")
	}
}

// TestEscSeqQuitWithoutLoading 任务未运行时按左箭头，Esc 交给 tview 但 '[' 仍要被
// 解码器吃掉，最终仍然得到 KeyLeft（不能出现 "[D"）。
func TestEscSeqArrowWhileIdle(t *testing.T) {
	m := newTUIForSeqTest(t)
	m.escSeqKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone), time.Now())
	if _, ev := m.escSeqKey(tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModNone), time.Now()); ev != nil {
		t.Fatalf("'[' 应被吞掉, got %v", ev)
	}
	if _, ev := m.escSeqKey(tcell.NewEventKey(tcell.KeyRune, 'D', tcell.ModNone), time.Now()); ev == nil || ev.Key() != tcell.KeyLeft {
		t.Fatalf("应得到 KeyLeft, got %v", ev)
	}
}

// TestEscSeqDecodeEnabledFor 平台默认值与环境变量覆盖。
func TestEscSeqDecodeEnabledFor(t *testing.T) {
	noEnv := func(string) string { return "" }
	if !escSeqDecodeEnabledFor("windows", noEnv) {
		t.Fatal("Windows 应默认启用原始序列解码")
	}
	if escSeqDecodeEnabledFor("linux", noEnv) {
		t.Fatal("Unix 的 tcell 会正常翻译，不应默认开启")
	}
	if escSeqDecodeEnabledFor("windows", func(string) string { return "0" }) {
		t.Fatal("MIZAR_ESC_SEQ=0 应关闭")
	}
	if !escSeqDecodeEnabledFor("linux", func(string) string { return "1" }) {
		t.Fatal("MIZAR_ESC_SEQ=1 应强制开启")
	}
}

// TestEscSeqTruncatedFlush 半截序列（ESC O）超时后必须把 'O' 补回输入框。
func TestEscSeqTruncatedFlush(t *testing.T) {
	m := newTUIForSeqTest(t)
	m.escSeqBuf = []rune{0x1b, 'O'}
	m.escSeqAt = time.Now().Add(-time.Second)
	// 直接调用解码入口触发超时清理
	if handled, _ := m.escSeqKey(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone), time.Now()); handled {
		t.Fatal("超时的残留缓冲不应再吞掉后续按键")
	}
	if m.escSeqBuf != nil {
		t.Fatalf("超时后缓冲应清空, got %q", string(m.escSeqBuf))
	}
}
