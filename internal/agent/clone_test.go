package agent

import (
	"testing"
)

// TestCloneSharesProcessServicesIsolatesRunState 钉住 Clone 的语义：
// 共享进程级服务（LLM/插件/Jobs/GoalService），隔离运行态（Initial/回调/steer）。
//
// 这是多会话并发正确性的根基：如果 Initial 或 OnLLMStream 被共享，
// 两个会话同时跑就会互相覆盖头部历史、把 A 的流式增量写进 B 的窗口。
func TestCloneSharesProcessServicesIsolatesRunState(t *testing.T) {
	parent := &Agent{
		System:         "系统提示",
		Soul:           "魂魄",
		MaxSteps:       42,
		WorkDir:        "/work",
		PluginDir:      "/plugins",
		Initial:        []Message{{Role: "user", Content: "父会话历史"}},
		StatsData:      &StatsDataRef{},
		Jobs:           nil,
		OnLLMStream:    func(step int, d StreamDelta) {},
		OnToolExchange: func(tool, args, reason, out string, err error) {},
		OnCompact:      func(summary string, dropped, before int) {},
	}

	child := parent.Clone()
	if child == nil {
		t.Fatal("Clone 不应返回 nil")
	}

	// ---- 共享：进程级服务必须同一份 ----
	if child.System != parent.System || child.Soul != parent.Soul {
		t.Fatal("提示词素材应共享")
	}
	if child.MaxSteps != parent.MaxSteps || child.WorkDir != parent.WorkDir {
		t.Fatal("配置项应共享")
	}
	if child.Jobs != parent.Jobs {
		t.Fatal("Jobs 注册表必须共享（否则两边的后台任务互不可见）")
	}
	if child.GoalService != parent.GoalService {
		t.Fatal("GoalService 必须共享")
	}

	// TestCloneSharesPermission（内联）：权限门禁进程级共享——
	// /permission 切换模式后所有克隆会话与子代理即时生效（GAP-S2）。
	parent.Permission = NewPermissionGate(PermDefault)
	if child2 := parent.Clone(); child2.Permission != parent.Permission {
		t.Fatal("Permission 必须共享同一 gate 实例")
	}
	// nil 透传：未启用的主代理克隆后同样无门禁
	parent2 := &Agent{}
	if c := parent2.Clone(); c.Permission != nil {
		t.Fatal("nil gate 应透传为 nil")
	}

	// ---- 隔离：运行态必须独立 ----
	if child.Initial != nil {
		t.Fatalf("Initial 必须隔离，实际继承了 %v", child.Initial)
	}
	if child.OnLLMStream != nil {
		t.Fatal("OnLLMStream 必须隔离（否则两个会话的增量会串台）")
	}
	if child.OnToolExchange != nil {
		t.Fatal("OnToolExchange 必须隔离")
	}
	if child.OnCompact != nil {
		t.Fatal("OnCompact 必须隔离")
	}
	if child.StatsData == parent.StatsData {
		t.Fatal("StatsData 必须隔离（统计串台）")
	}
	if child.StatsData == nil {
		t.Fatal("StatsData 不应为 nil")
	}
}

// TestCloneIsolatesSteerSlots 纠正消息槽位必须独立：
// A 会话的 steer 不能被 B 会话的循环消费。
func TestCloneIsolatesSteerSlots(t *testing.T) {
	parent := &Agent{}
	child := parent.Clone()

	child.Steer("给子会话的纠正")
	if !child.PendingSteer() {
		t.Fatal("子会话应持有自己的 steer")
	}
	if parent.PendingSteer() {
		t.Fatal("父会话不应看到子会话的 steer（会串到别的会话的循环里）")
	}
	if parent.steerSeq != 0 {
		t.Fatal("steerSeq 不应被污染")
	}
}

// TestCloneRunStateNotInherited 运行标志不能被继承：
// 父实例在跑时克隆出来的子实例必须是「未运行、未中止」。
func TestCloneRunStateNotInherited(t *testing.T) {
	parent := &Agent{}
	parent.runActive.Store(true)
	parent.aborted.Store(true)

	child := parent.Clone()
	if child.Running() {
		t.Fatal("新实例不应继承「正在运行」")
	}
	if child.Aborted() {
		t.Fatal("新实例不应继承「已中止」（否则一出生就死）")
	}
}

// TestCloneNilSafe Clone 对 nil 接收者必须安全（池化代码里可能拿到空模板）。
func TestCloneNilSafe(t *testing.T) {
	var a *Agent
	if got := a.Clone(); got != nil {
		t.Fatal("nil 的 Clone 应返回 nil")
	}
}
