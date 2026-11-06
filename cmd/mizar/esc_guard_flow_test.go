package main

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/agent"
)

// newTUIForEscTest 构造可驱动 InputCapture 的 TUI（复用真实构造函数，确保
// renderAllDirect 依赖的组件都在）。
func newTUIForEscTest(t *testing.T) *tuiModel {
	t.Helper()
	m := newTuiModel(&agent.Agent{}, nil, "test-esc", nil)
	return m
}

// pressKey 模拟一次按键经过真实 InputCapture。
func (m *tuiModel) pressKey(event *tcell.EventKey) *tcell.EventKey {
	cap := m.app.GetInputCapture()
	if cap == nil {
		panic("InputCapture 未安装")
	}
	return cap(event)
}

// TestEscJunkSynthetic 端到端验证「滚轮泄漏序列不取消任务」：
// 模拟 tcell 把 ESC[<35;66;61M 拆成 Alt+'[' + 若干字符事件，
// InputCapture 应全部吞掉，且任务保持运行。
func TestEscJunkSynthetic(t *testing.T) {
	m := newTUIForEscTest(t)
	m.setLoading(true)

	// 1) 用户真的按 Esc（loading 中）：先挂起，不立刻取消
	if ret := m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); ret != nil {
		t.Fatalf("Esc 应被吞掉, got %v", ret)
	}
	if !m.escPending {
		t.Fatal("Esc 应处于挂起态（等待确认是否泄漏序列）")
	}
	if m.agent.Aborted() {
		t.Fatal("挂起期间不应取消任务")
	}

	// 2) 紧跟着是泄漏序列：Alt+'[' → 判定泄漏，吞掉后续字符
	m.pressKey(tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModAlt))
	if m.escPending {
		t.Fatal("识别泄漏序列后应清除挂起态")
	}
	for _, r := range "<35;66;61M" {
		m.pressKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if m.agent.Aborted() {
		t.Fatal("泄漏序列绝不能让任务被取消")
	}
	if m.inputField.GetText() != "" {
		t.Fatalf("泄漏字符不应进入输入框, got %q", m.inputField.GetText())
	}
	if m.escJunk {
		t.Fatal("序列结束后应退出吞字符状态")
	}
	if !m.loading {
		t.Fatal("任务应仍在运行（loading 保持 true）")
	}
}

// TestEscAloneCancels 用户单独按 Esc（无后续序列）→ 到点应真正取消任务。
func TestEscAloneCancels(t *testing.T) {
	m := newTUIForEscTest(t)
	m.setLoading(true)
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	if m.agent.Aborted() {
		t.Fatal("挂起窗口内不应立刻取消")
	}
	m.escConfirmForTest() // 等价于确认定时器到点
	if !m.agent.Aborted() {
		t.Fatal("确认后应取消任务")
	}
	if m.loading {
		t.Fatal("取消后应退出 loading")
	}
}

// TestEscThenDigitNotLeak 挂起中的 Esc 后面跟普通数字（用户在答选项）不能当成泄漏。
func TestEscThenDigitNotLeak(t *testing.T) {
	m := newTUIForEscTest(t)
	m.setLoading(true)
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.pressKey(tcell.NewEventKey(tcell.KeyRune, '1', tcell.ModNone))
	if !m.agent.Aborted() {
		t.Fatal("Esc 后跟数字应视为真实取消")
	}
	if m.escJunk {
		t.Fatal("普通数字不应进入吞字符状态")
	}
}

// TestEscJunkExpires 泄漏序列超窗口后，后续普通字符必须正常回到输入框。
func TestEscJunkExpires(t *testing.T) {
	m := newTUIForEscTest(t)
	m.setLoading(true)
	m.pressKey(tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModAlt))
	m.escJunkAt = time.Now().Add(-2 * escJunkWindow) // 模拟窗口过期
	if ret := m.pressKey(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)); ret == nil {
		t.Fatal("窗口过期后普通字符不应被吞")
	}
	if m.escJunk {
		t.Fatal("窗口过期后应退出吞字符状态")
	}
}

// TestEscJunkWindowConstant 窗口值保持在合理范围（太小误判、太大影响手感）。
func TestEscJunkWindowConstant(t *testing.T) {
	if escJunkWindow < 30*time.Millisecond || escJunkWindow > 300*time.Millisecond {
		t.Fatalf("escJunkWindow=%v 超出合理范围", escJunkWindow)
	}
}
