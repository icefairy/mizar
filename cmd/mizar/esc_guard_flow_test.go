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
	if escJunkWindowFor("linux") < 30*time.Millisecond || escJunkWindowFor("linux") > 300*time.Millisecond {
		t.Fatalf("unix 窗口=%v 超出合理范围", escJunkWindowFor("linux"))
	}
	// Windows 要更宽：ConPTY 把泄漏序列拆成多条输入记录，记录间隔可达几十毫秒，
	// 90ms 窗口实测仍会误取消（v0.7.1 复现）。
	if escJunkWindowFor("windows") <= escJunkWindowFor("linux") {
		t.Fatalf("Windows 窗口 %v 必须大于 Unix 窗口 %v",
			escJunkWindowFor("windows"), escJunkWindowFor("linux"))
	}
	if escJunkWindowWide <= escJunkWindowFor("windows") {
		t.Fatal("Wide 窗口必须大于 Windows 常规窗口")
	}
}

// TestEscLeakBroadensWindowOrSuppresses 已知终端正在泄漏时：
//
//	① 挂起 Esc 的确认窗口放宽到 escJunkWindowWide；
//	② 到点确认时若仍在泄漏，宁可不取消任务（滚轮连滚分批到达的典型场景）。
func TestEscLeakBroadensWindowOrSuppresses(t *testing.T) {
	m := newTUIForEscTest(t)
	m.setLoading(true)

	// 先来一段泄漏序列（Alt+'[' + 坐标）
	m.pressKey(tcell.NewEventKey(tcell.KeyRune, '[', tcell.ModAlt))
	for _, r := range "<35;66;61M" {
		m.pressKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if !m.recentLeak() {
		t.Fatal("观察到泄漏序列后 recentLeak 应为 true")
	}
	if got := m.escConfirmDelay(time.Now()); got != escJunkWindowWide {
		t.Fatalf("泄漏记忆期内确认窗口 = %v, want %v", got, escJunkWindowWide)
	}

	// 用户（或下一批泄漏）又送来一个 Esc：挂起后到点，此时仍在泄漏记忆期内
	// → 判定为泄漏，不取消任务。
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	if !m.escPending {
		t.Fatal("Esc 应挂起")
	}
	m.escConfirmForTest() // 等价于确认定时器到点（此时仍在泄漏记忆期内）
	if m.agent.Aborted() {
		t.Fatal("泄漏记忆期内不应因 Esc 取消任务")
	}
	if m.escPending {
		t.Fatal("确认后应清除挂起态")
	}
	// 泄漏记忆过期后，Esc 才应真正取消
	m.escLastJunkAt = time.Now().Add(-escLeakMemory - time.Second)
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.escConfirmForTest()
	if !m.agent.Aborted() {
		t.Fatal("泄漏记忆过期后，Esc 应正常取消任务")
	}
}

// TestEscConfirmTwoStageWindows 两段式取消（Windows 默认行为）：
// 第一次 Esc 只提示、不取消；窗口内第二次 Esc 才真正取消；超时后重新计数。
func TestEscConfirmTwoStageWindows(t *testing.T) {
	m := newTUIForEscTest(t)
	m.escConfirm = true
	m.setLoading(true)

	// 第一次 Esc：挂起 → 确认到点 → 仅进入待确认态
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.escConfirmForTest()
	if m.agent.Aborted() {
		t.Fatal("第一次 Esc 不应取消任务")
	}
	if m.escArmedAt.IsZero() {
		t.Fatal("第一次 Esc 后应进入待确认态")
	}
	if m.loading == false {
		t.Fatal("任务应仍在运行")
	}

	// 第二次 Esc（窗口内）：真正取消
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.escConfirmForTest()
	if !m.agent.Aborted() {
		t.Fatal("窗口内第二次 Esc 应取消任务")
	}
}

// TestEscConfirmExpires 待确认态超时后失效：过一会儿再按一次 Esc 只重新提示。
func TestEscConfirmExpires(t *testing.T) {
	m := newTUIForEscTest(t)
	m.escConfirm = true
	m.setLoading(true)

	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.escConfirmForTest()
	if m.agent.Aborted() {
		t.Fatal("第一次 Esc 不应取消")
	}
	m.escArmedAt = time.Now().Add(-escArmWindow - time.Second) // 模拟超时
	m.pressKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	m.escConfirmForTest()
	if m.agent.Aborted() {
		t.Fatal("待确认态超时后，再按一次 Esc 应重新提示而非取消")
	}
}

// TestEscConfirmEnabledFor 平台默认值：Windows 开、Unix 关，可用环境变量覆盖。
func TestEscConfirmEnabledFor(t *testing.T) {
	noEnv := func(string) string { return "" }
	if !escConfirmEnabledFor("windows", noEnv) {
		t.Fatal("Windows 应默认启用两段式 Esc 取消")
	}
	if escConfirmEnabledFor("linux", noEnv) {
		t.Fatal("Unix 无 ConPTY 缺陷，单按 Esc 应直接取消")
	}
	if escConfirmEnabledFor("windows", func(string) string { return "0" }) {
		t.Fatal("MIZAR_ESC_CONFIRM=0 应关闭两段式")
	}
	if !escConfirmEnabledFor("linux", func(string) string { return "1" }) {
		t.Fatal("MIZAR_ESC_CONFIRM=1 应强制启用")
	}
}
