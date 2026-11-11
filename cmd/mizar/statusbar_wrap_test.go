package main

import (
	"strings"
	"testing"

	"github.com/rivo/tview"

	"mizar/internal/agent"
)

// 状态栏文本必须能按终端宽度自动折成多行：窄终端下内容放不下时
// 不能只渲染 1 行被截断（用户只能看到半截 tokens/上下文/模型）。
func TestMeasureRowsWraps(t *testing.T) {
	text := strings.Repeat("上下文: 42% | 缓存: 88% | 12 tok/s | qwen3-235b ", 3)
	if got := measureRows(text, 500); got != 1 {
		t.Fatalf("宽终端应 1 行，got %d", got)
	}
	narrow := measureRows(text, 40)
	if narrow < 3 {
		t.Fatalf("40 列下应折成多行，got %d", narrow)
	}
	// 宽度越大行数不增
	if wide := measureRows(text, 120); wide >= narrow {
		t.Fatalf("更宽应更少行：120列=%d 40列=%d", wide, narrow)
	}
}

// CJK 占两列：同样是 N 个字符，中文行数应不少于等长 ASCII。
func TestMeasureRowsCJKWider(t *testing.T) {
	cjk := strings.Repeat("上下文", 20)   // 60 个汉字 = 120 列
	ascii := strings.Repeat("ctx", 20) // 60 个 ASCII = 60 列
	if measureRows(cjk, 80) < measureRows(ascii, 80) {
		t.Fatal("中文应按双宽计算，行数不应少于 ASCII")
	}
}

// syncStatusBarHeight 在窄终端下把状态栏调高，宽终端下收回 1 行。
func TestSyncStatusBarHeight(t *testing.T) {
	m := newTUIForQueueTest()
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.projBar, 1, 0, false).
		AddItem(m.statusBar, 1, 0, false)
	m.statusBar = tview.NewTextView().SetDynamicColors(true)
	m.scr = nil

	long := strings.Repeat("tokens: 123k(in:100k out:23k) | 上下文: 42% | 缓存: 88% | 12 tok/s | 模型 | 思考: auto ", 2)

	m.widthOverride, m.heightOverride = 40, 40
	m.syncStatusBarHeight(long)
	narrowRows := m.statusRows
	if narrowRows < 2 {
		t.Fatalf("40 列下状态栏应多行，got %d", narrowRows)
	}

	// 宽度变宽 → 行数应减少（同步逻辑必须感知宽度变化）
	m.widthOverride, m.heightOverride = 200, 40
	m.syncStatusBarHeight(long)
	if m.statusRows >= narrowRows {
		t.Fatalf("宽度恢复后行数应减少：narrow=%d now=%d（宽度=%d）", narrowRows, m.statusRows, m.statusWidth)
	}
}

// 真实渲染后，flex 里 statusBar 的 fixedSize 应等于算出的行数。
func TestStatusBarHeightApplied(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.Compactor = &agent.Compactor{ContextWindow: 200000, ReserveTokens: 20000}
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)

	m.statusBarDirect()
	text := m.statusBar.GetText(true)
	if text == "" {
		t.Fatal("状态栏文本不应为空")
	}
	if m.statusWidth <= 0 {
		t.Fatalf("应记录测量宽度: %d", m.statusWidth)
	}
	if m.statusRows < 1 {
		t.Fatalf("行数应 >= 1: %d", m.statusRows)
	}
}

// 极窄终端下底部栏不能无限增高（否则聊天区被挤没）：最多 3 行，且受屏幕高度约束。
func TestMaxBarRowsCap(t *testing.T) {
	m := newTUIForQueueTest()
	m.heightOverride = 40
	if got := m.maxBarRows(); got != 3 {
		t.Fatalf("40 行终端上限应为 3，got %d", got)
	}
	m.heightOverride = 12
	if got := m.maxBarRows(); got > 2 {
		t.Fatalf("12 行终端上限应 <=2，got %d", got)
	}
	// 极窄宽度下 syncBarHeight 不应把行数拉爆
	m.widthOverride, m.heightOverride = 8, 40
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.statusBar, 1, 0, false)
	m.syncStatusBarHeight("tokens: 1(in:1 out:1) | 上下文: 42% | 缓存: 88% | 12 tok/s | qwen | 思考: auto")
	if m.statusRows > 3 {
		t.Fatalf("8 列下行数应被限制在 3 行内，got %d", m.statusRows)
	}
}

// 项目行同样按宽度自适应（窄终端下分支/模式/会话/时长一行放不下）。
func TestProjBarWrapsWhenNarrow(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.GrillMode = agent.NewGrillMode()
	m.project = projectInfo{Dir: "codes/mizar", Branch: "feature/very-long-branch-name", Dirty: 3, IsGit: true}
	m.projBar = tview.NewTextView().SetDynamicColors(true)
	m.widthOverride, m.heightOverride = 40, 40

	m.projBarDirect()
	if m.projRows < 2 {
		t.Fatalf("40 列下项目行应多行，got %d（内容: %q）", m.projRows, m.projRaw)
	}
	if !strings.Contains(m.projRaw, "正常编码模式") {
		t.Fatalf("项目行应含模式段: %q", m.projRaw)
	}
}
