package agent

import (
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

func TestGrillModeEnterExit(t *testing.T) {
	g := NewGrillMode()
	if g.IsActive() {
		t.Fatal("初始不应激活")
	}
	g.Enter("")
	if !g.IsActive() {
		t.Fatal("Enter 后应激活")
	}
	if !strings.Contains(g.Guidance(), "拷问") {
		t.Fatalf("默认指引应含拷问要点: %q", g.Guidance())
	}
	g.Exit()
	if g.IsActive() {
		t.Fatal("Exit 后应关闭")
	}
}

// 预设提示词必须约束住「别无限追问 / 每题带选项 / 不许边猜边改 / 有收敛条件」。
func TestDefaultGrillGuidanceCoversKeyRules(t *testing.T) {
	g := NewGrillMode()
	g.Enter("")
	gd := g.Guidance()
	for _, want := range []string{
		"ask_user_question", // 用工具提问，用户点编号即可答
		"最多 3 个问题",          // 每轮限量，避免一次堆满
		"禁止改代码",             // 拷问期间不动产物
		"收敛条件",              // 有停止条件
		"确认清单",              // 收敛后的输出结构
		"默认假设",              // 不确定项显式声明
		"你决定",               // 用户甩手时的处理
	} {
		if !strings.Contains(gd, want) {
			t.Errorf("默认拷问指引缺少关键要素 %q", want)
		}
	}
}

func TestGrillModeCustomGuidanceAndVersion(t *testing.T) {
	g := NewGrillMode()
	v0 := g.Version()
	g.Enter("重点问兼容性")
	if g.Guidance() != "重点问兼容性" {
		t.Fatalf("guidance = %q", g.Guidance())
	}
	if g.Version() == v0 {
		t.Fatal("Enter 应使版本号变化")
	}
	if !strings.Contains(g.SystemSection(), "重点问兼容性") {
		t.Fatalf("SystemSection 应包含自定义指引: %q", g.SystemSection())
	}
	g.Exit()
	if g.SystemSection() != "" {
		t.Fatal("退出后 SystemSection 应为空")
	}
}

// 拷问与计划模式可以叠加，且拷问段在前（需求没问清就不要先出方案）。
func TestAgentPromptContainsBothModes(t *testing.T) {
	a := newPromptTestAgent(t)
	a.GrillMode.Enter("")
	a.PlanMode.Enter("")
	p := a.SystemPrompt()
	gi, pi := strings.Index(p, "拷问模式"), strings.Index(p, "计划模式（Plan Mode）")
	if gi < 0 || pi < 0 {
		t.Fatalf("两段都应注入: grill=%d plan=%d", gi, pi)
	}
	if gi > pi {
		t.Fatal("拷问段应在计划段之前")
	}
}

// 关掉模式后 system prompt 缓存必须失效，否则模型继续按老提示跑。
func TestSystemPromptInvalidatedOnModeExit(t *testing.T) {
	a := newPromptTestAgent(t)
	plain := a.SystemPrompt()
	a.PlanMode.Enter("")
	withPlan := a.SystemPrompt()
	if plain == withPlan {
		t.Fatal("进入计划模式后提示应变化")
	}
	a.PlanMode.Exit()
	back := a.SystemPrompt()
	if back == withPlan {
		t.Fatal("退出计划模式后提示应恢复（缓存需失效）")
	}
}

// newPromptTestAgent 构造一个带空插件管理器的 Agent（避免 nil 指针）。
func newPromptTestAgent(t *testing.T) *Agent {
	t.Helper()
	pm := plugins.NewManager(t.TempDir(), &engine.HostFuncs{})
	pm.LoadAll()
	return New(nil, pm)
}
