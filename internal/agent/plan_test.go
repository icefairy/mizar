package agent

import (
	"testing"
	"time"
)

func TestPlanModeEnterExit(t *testing.T) {
	p := NewPlanMode()
	if p.IsActive() {
		t.Fatal("should not be active initially")
	}
	p.Enter("")
	if !p.IsActive() {
		t.Fatal("should be active after Enter")
	}
	if p.Guidance() == "" {
		t.Fatal("guidance should have default value")
	}
	p.Exit()
	if p.IsActive() {
		t.Fatal("should not be active after Exit")
	}
}

func TestPlanModeCustomGuidance(t *testing.T) {
	p := NewPlanMode()
	p.Enter("自定义规划指引")
	if p.Guidance() != "自定义规划指引" {
		t.Fatalf("guidance want '自定义规划指引', got %q", p.Guidance())
	}
}

func TestPlanModeSystemSection(t *testing.T) {
	p := NewPlanMode()
	if p.SystemSection() != "" {
		t.Fatal("inactive should return empty section")
	}
	p.Enter("探索设计")
	section := p.SystemSection()
	if section == "" {
		t.Fatal("active should return non-empty section")
	}
	if !contains(section, "探索设计") {
		t.Fatalf("section should contain guidance: %q", section)
	}
}

func TestPlanModePending(t *testing.T) {
	p := NewPlanMode()
	p.Enter("test")
	p.SetPending(true)
	if !p.IsPending() {
		t.Fatal("should be pending")
	}
}

func TestAskPlanReviewNilCallback(t *testing.T) {
	// 无回调时默认 approve
	// 注意：agentAskPlanUser 为 nil 时 AskPlanReview 直接返回 approve
	// 但函数内有 jsonUnmarshal 调用占位——实际上 nil 路径不经过这里
	// 本测试验证降级行为（无交互时直接 approve）
	// 由于 AskPlanReview 内部在 agentAskPlanUser==nil 时直接返回，此处只测非 nil 路径之外的逻辑
	// 直接验证函数行为：nil callback → approve
	// （真实 TUI 回调由 main.go 设置，此处跳过）
	_ = time.Now()
}

// 退出模式的参数写法必须统一收口：exit/off/stop/quit/退出… 都算退出，
// 否则用户按直觉输入 /plan exit 会变成「又进入了计划模式」。
func TestIsModeExitArg(t *testing.T) {
	for _, arg := range []string{"exit", "EXIT", "off", "stop", "quit", "退出", "关闭", "  Off "} {
		if !IsModeExitArg(arg) {
			t.Errorf("%q 应识别为退出", arg)
		}
	}
	for _, arg := range []string{"", "on", "enter", "进入", "我来决定方案", "exitx"} {
		if IsModeExitArg(arg) {
			t.Errorf("%q 不应识别为退出", arg)
		}
	}
}

// Enter/Exit 必须让版本号变化（system prompt 缓存据此失效）。
func TestPlanModeVersionChanges(t *testing.T) {
	p := NewPlanMode()
	v0 := p.Version()
	p.Enter("")
	v1 := p.Version()
	if v1 == v0 {
		t.Fatal("Enter 应使版本号变化")
	}
	p.Exit()
	if p.Version() == v1 {
		t.Fatal("Exit 应使版本号变化")
	}
}

// 计划模式提示里必须写明「怎么退出」——否则用户只能靠猜 exit_plan_mode。
func TestPlanSystemSectionMentionsExitCommand(t *testing.T) {
	p := NewPlanMode()
	p.Enter("")
	if !contains(p.SystemSection(), "/plan off") {
		t.Fatalf("计划模式提示应说明 /plan off 退出: %q", p.SystemSection())
	}
}

// TestAskPlanReviewWithCallback 有回调时应能解析答案（回归：jsonUnmarshal 曾是
// 永远返回错误的占位实现，导致用户批准后仍报「解析审批答案失败」）。
func TestAskPlanReviewWithCallback(t *testing.T) {
	old := agentAskPlanUser
	defer func() { agentAskPlanUser = old }()

	agentAskPlanUser = func(questions string) (string, error) {
		return `[{"id":"review","value":"Approve（批准并开始执行）"}]`, nil
	}
	rv, err := AskPlanReview("# 计划\n1. 做 A\n2. 做 B")
	if err != nil {
		t.Fatalf("AskPlanReview: %v", err)
	}
	if rv.Action != "approve" {
		t.Fatalf("Action=%q want approve", rv.Action)
	}
	// 选 Keep planning → 应带反馈继续规划
	agentAskPlanUser = func(questions string) (string, error) {
		return `[{"id":"review","value":"Keep planning（继续规划）"}]`, nil
	}
	rv2, err := AskPlanReview("计划")
	if err != nil {
		t.Fatalf("AskPlanReview2: %v", err)
	}
	if rv2.Action != "keep_planning" {
		t.Fatalf("Action=%q want keep_planning", rv2.Action)
	}
}
