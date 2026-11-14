package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// ============================================================================
// 测试用 LLM / 断言工具
// ============================================================================

// scriptedLLM 按脚本逐轮返回回复；实现 LLM + ToolCallLLM 两个接口，
// 让子 agent 的 Run() 走原生工具调用路径（能解析 {action:tool} 并执行）。
type scriptedLLM struct {
	mu      sync.Mutex
	script  []string
	calls   int
	gotTool [][]plugins.Tool // 每轮收到的工具清单快照
	gotMsgs [][]Message      // 每轮收到的消息快照
}

func (s *scriptedLLM) Chat(msgs []Message) (string, error) {
	return s.next(msgs, nil)
}

func (s *scriptedLLM) ChatWithTools(msgs []Message, tools []plugins.Tool) (string, error) {
	return s.next(msgs, tools)
}

func (s *scriptedLLM) next(msgs []Message, tools []plugins.Tool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 存一份快照（主循环可能复用底层数组）。
	snap := make([]plugins.Tool, len(tools))
	copy(snap, tools)
	s.gotTool = append(s.gotTool, snap)
	msgsSnap := make([]Message, len(msgs))
	copy(msgsSnap, msgs)
	s.gotMsgs = append(s.gotMsgs, msgsSnap)
	i := s.calls
	s.calls++
	if i < len(s.script) {
		return s.script[i], nil
	}
	return `{"action":"reply","text":"done"}`, nil
}

func (s *scriptedLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// waitState 轮询等句柄进入终态（子 agent 是异步 goroutine）。
func waitState(sm *SubagentManager, id string, timeout time.Duration) SubagentState {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h, ok := sm.Get(id); ok {
			switch h.State {
			case SubagentSucceeded, SubagentFailed, SubagentCancelled:
				return h.State
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "TIMEOUT"
}

// safeNotes 线程安全的通知收集器：notify 在子 goroutine 写，测试主 goroutine 读。
type safeNotes struct {
	mu    sync.Mutex
	items []string
}

func (s *safeNotes) add(n string) {
	s.mu.Lock()
	s.items = append(s.items, n)
	s.mu.Unlock()
}

func (s *safeNotes) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.items))
	copy(out, s.items)
	return out
}

func newTestManager(llm LLM, notes *safeNotes) *SubagentManager {
	pm := plugins.NewManager("", nil)
	// 注册一个真实内置工具，供子 agent 调用以证明「会干活」。
	pm.RegisterBuiltin(plugins.Tool{
		Name: "ping", Description: "test tool",
		Run: func(args string) (string, error) { return "pong:" + args, nil },
	})
	// 委派类工具注册进去，验证子 agent 侧会被过滤掉（防递归）。
	pm.RegisterBuiltin(plugins.Tool{Name: "delegate_task", Run: func(string) (string, error) { return "", nil }})
	return NewSubagentManager(SubagentConfig{
		ParentLLM: llm, ParentSys: "test-system", Plugins: pm,
		JobNotify: func(note string) {
			if notes != nil {
				notes.add(note)
			}
		},
	})
}

// ============================================================================
// P0：句柄登记（旧实现最致命的缺失）
// ============================================================================

func TestSubagent_HandleIsRegistered(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)

	h, err := sm.Launch(SubagentLaunchRequest{Goal: "测试任务"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// 旧实现：subs 从不写入 → Get 恒 false → 派出去就是孤儿。
	if got, ok := sm.Get(h.ID); !ok {
		t.Fatalf("句柄 %s 没登记进 subs，get_subagent 查不到", h.ID)
	} else if got.ID != h.ID {
		t.Fatalf("Get 返回错句柄: %s", got.ID)
	}
	if len(sm.List()) != 1 {
		t.Fatalf("List 应返回 1 个句柄，实际 %d", len(sm.List()))
	}
	waitState(sm, h.ID, 2*time.Second)
}

// ============================================================================
// P0：子 agent 真能干活（不再是纯聊天壳）
// ============================================================================

func TestSubagent_CanExecuteTools(t *testing.T) {
	// 第 1 轮：让子 agent 调 ping 工具；第 2 轮：交付结果。
	llm := &scriptedLLM{script: []string{
		`{"action":"tool","tool":"ping","args":"{\"x\":1}"}`,
		`{"action":"reply","text":"ping 已执行，结果已拿到"}`,
	}}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "调用 ping 工具"})

	st := waitState(sm, h.ID, 3*time.Second)
	if st != SubagentSucceeded {
		t.Fatalf("子 agent 应成功，实际 %s (err=%v)", st, func() string { g, _ := sm.Get(h.ID); return g.Error }())
	}
	// 旧实现只 ChatWithTools(msgs,nil) 且没有执行/回填环，calls 恒为 1。
	if llm.callCount() < 2 {
		t.Fatalf("子 agent 应至少跑 2 轮（工具调用→结果→reply），实际只跑了 %d 轮 = 纯聊天壳", llm.callCount())
	}
	// 工具清单必须非空：证明子 agent 拿到了工具（不再是 nil）。
	if len(llm.gotTool) == 0 || len(llm.gotTool[0]) == 0 {
		t.Fatal("子 agent 收到的工具清单为空，无法干活")
	}
	got, _ := sm.Get(h.ID)
	if got.Result == "" {
		t.Fatal("子 agent 没有产出结果")
	}
}

// 子 agent 在未达深度上限时**允许**再委派（有限嵌套），但不能无限递归：
// 深度闸门由 LaunchAtDepth/maxDepth 守住（见 TestSubagent_NestedBlockedAtMaxDepth）。
// 这里验证「depth=1 未被误判为顶层而全屏蔽」，同时验证禁止项仍被过滤。
func TestSubagent_ToolsFilteredAgainstRecursion(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{
		Goal:         "检查工具集",
		BlockedTools: []string{"ping"}, // 显式屏蔽
	})
	waitState(sm, h.ID, 2*time.Second)
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.gotTool) == 0 {
		t.Fatal("子 agent 没发起 LLM 调用")
	}
	for _, tl := range llm.gotTool {
		for _, tt := range tl {
			if tt.Name == "ping" {
				t.Fatal("BlockedTools 指定的 ping 不应出现在子 agent 工具集")
			}
		}
	}
}

// ============================================================================
// P0：结果回灌（notify 通道）
// ============================================================================

func TestSubagent_ResultNotify(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"结论：42"}`}}
	notes := &safeNotes{}
	sm := newTestManager(llm, notes)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "算答案"})
	if st := waitState(sm, h.ID, 2*time.Second); st != SubagentSucceeded {
		t.Fatalf("状态 %s", st)
	}
	// notify 在 goroutine 里同步调（setState 之后），waitState 命中终态时已发完。
	got := notes.snapshot()
	if len(got) != 1 {
		t.Fatalf("结果应回灌 1 条完成通知，实际 %d 条", len(got))
	}
	if !strings.Contains(got[0], "结论：42") || !strings.Contains(got[0], h.ID) {
		t.Fatalf("通知内容不含结果/句柄: %q", got[0])
	}
}

// ============================================================================
// P1：tasks[] 并行 fan-out
// ============================================================================

func TestSubagent_BatchFanOut(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	notes := &safeNotes{}
	sm := newTestManager(llm, notes)

	hs, err := sm.LaunchBatch([]SubagentLaunchRequest{
		{Goal: "任务A"}, {Goal: "任务B"}, {Goal: "任务C"},
	}, 1)
	if err != nil {
		t.Fatalf("LaunchBatch: %v", err)
	}
	if len(hs) != 3 {
		t.Fatalf("应返回 3 个句柄，实际 %d", len(hs))
	}
	// 同一批次，句柄互不相同，且都能查到。
	batch := hs[0].Batch
	seen := map[string]bool{}
	for _, h := range hs {
		if h.Batch != batch {
			t.Fatalf("批次 id 不一致: %s vs %s", h.Batch, batch)
		}
		if seen[h.ID] {
			t.Fatalf("句柄 id 重复: %s", h.ID)
		}
		seen[h.ID] = true
		if _, ok := sm.Get(h.ID); !ok {
			t.Fatalf("批量句柄 %s 查不到", h.ID)
		}
	}
	// 等全部终结。
	for _, h := range hs {
		if st := waitState(sm, h.ID, 3*time.Second); st != SubagentSucceeded {
			t.Fatalf("%s 状态 %s", h.ID, st)
		}
	}
	if len(notes.snapshot()) != 3 {
		t.Fatalf("3 个子任务应回灌 3 条通知，实际 %d", len(notes.snapshot()))
	}
}

func TestSubagent_BatchLimit(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	var reqs []SubagentLaunchRequest
	for i := 0; i < defaultMaxConcurrent+1; i++ {
		reqs = append(reqs, SubagentLaunchRequest{Goal: fmt.Sprintf("任务%d", i)})
	}
	if _, err := sm.LaunchBatch(reqs, 1); err == nil {
		t.Fatal("超过并发上限应报错")
	}
}

// ============================================================================
// 工具工厂：单任务 / 批量 / 查询 / 取消
// ============================================================================

func TestDelegateTool_SingleAndBatch(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	tool := DelegateTool(sm)

	// 单任务
	out, err := tool.Run(`{"goal":"单个子任务"}`)
	if err != nil {
		t.Fatalf("单任务 Run: %v", err)
	}
	var single struct {
		Count   int              `json:"count"`
		Handles []SubagentHandle `json:"handles"`
	}
	if json.Unmarshal([]byte(out), &single) != nil || single.Count != 1 {
		t.Fatalf("单任务返回格式错: %s", out)
	}

	// 批量
	out, err = tool.Run(`{"tasks":[{"goal":"A"},{"goal":"B"},{"goal":"C"}]}`)
	if err != nil {
		t.Fatalf("批量 Run: %v", err)
	}
	if json.Unmarshal([]byte(out), &single) != nil || single.Count != 3 {
		t.Fatalf("批量应返回 3 个句柄: %s", out)
	}

	// 参数错误
	if _, err := tool.Run(`{}`); err == nil {
		t.Fatal("缺 goal/tasks 应报错")
	}
}

func TestGetSubagentTool_NotFound(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	if _, err := GetSubagentTool(sm).Run(`{"id":"不存在"}`); err == nil {
		t.Fatal("查不存在的句柄应报错")
	}
}

// blockingLLM 实现 ChatWithToolsCtx，绑定 ctx 阻塞，用于验证 Cancel 真能
// 中断在途 LLM 请求（而不只是改个状态位）。
type blockingLLM struct {
	entered chan struct{}
}

func (b *blockingLLM) Chat(msgs []Message) (string, error) {
	return `{"action":"reply","text":"unexpected"}`, nil
}

func (b *blockingLLM) ChatWithToolsCtx(ctx context.Context, msgs []Message, tools []plugins.Tool) (string, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-ctx.Done() // Cancel → Abort → Run 的 ctx 取消，这里立即返回
	return "", ctx.Err()
}

func TestCancelSubagent_StopsRunning(t *testing.T) {
	bl := &blockingLLM{entered: make(chan struct{})}
	sm := newTestManager(bl, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "长任务", TimeoutSecs: 60})
	// 等子 agent 真正进入在途 LLM 请求（RUNNING）。
	select {
	case <-bl.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("子 agent 没进入在途请求")
	}
	if err := sm.Cancel(h.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// ctx.Done 会先让 Run 返回，随后 runSubagent 的 select 可能命中 ctx.Done
	// 或 done；两者都应落到 CANCELLED（超时/被取消），不能是 SUCCEEDED。
	if st := waitState(sm, h.ID, 2*time.Second); st != SubagentCancelled {
		t.Fatalf("Cancel 后状态应为 CANCELLED，实际 %s", st)
	}
	// 幂等
	if err := sm.Cancel(h.ID); err != nil {
		t.Fatalf("重复 Cancel 应幂等: %v", err)
	}
}

// ============================================================================
// P3：生命周期 —— 模型派生 / 深度闸门 / Steer
// ============================================================================

// deriverLLM 实现 modelDeriver，记录被派生的模型名。
type deriverLLM struct {
	scriptedLLM
	mu        sync.Mutex
	derived   []string
	modelName string
}

func (d *deriverLLM) WithModel(model string) LLM {
	d.mu.Lock()
	d.derived = append(d.derived, model)
	d.mu.Unlock()
	n := &deriverLLM{modelName: model}
	n.script = []string{`{"action":"reply","text":"ok from ` + model + `"}`}
	return n
}

func TestSubagent_ModelDerived(t *testing.T) {
	llm := &deriverLLM{modelName: "parent-model"}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "用指定模型", Model: "child-model"})
	waitState(sm, h.ID, 2*time.Second)

	llm.mu.Lock()
	derived := append([]string{}, llm.derived...)
	llm.mu.Unlock()
	if len(derived) != 1 || derived[0] != "child-model" {
		t.Fatalf("应按 req.Model 派生模型，实际 derived=%v", derived)
	}
	if got, _ := sm.Get(h.ID); got.Model != "child-model" {
		t.Fatalf("句柄应回显 model=child-model，实际 %q", got.Model)
	}
}

// 不支持派生的 LLM：req.Model 应被忽略并在句柄标注，不能静默假装换了。
func TestSubagent_ModelIgnoredIsReported(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "换模型", Model: "child-model"})
	waitState(sm, h.ID, 2*time.Second)
	got, _ := sm.Get(h.ID)
	if !strings.Contains(got.Error, "model 被忽略") {
		t.Fatalf("不支持派生时应在句柄标注忽略原因，实际 Error=%q", got.Error)
	}
}

func TestSubagent_DepthGate(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)

	// 深度 1 可派
	if _, err := sm.LaunchAtDepth(SubagentLaunchRequest{Goal: "d1"}, 1); err != nil {
		t.Fatalf("depth=1 应允许: %v", err)
	}
	// 等于 maxDepth 可派
	if _, err := sm.LaunchAtDepth(SubagentLaunchRequest{Goal: "d3"}, sm.maxDepth); err != nil {
		t.Fatalf("depth=maxDepth 应允许: %v", err)
	}
	// 超过 maxDepth 拒绝
	if _, err := sm.LaunchAtDepth(SubagentLaunchRequest{Goal: "d4"}, sm.maxDepth+1); err == nil {
		t.Fatal("超过 maxDepth 应拒绝")
	}
}

// 子 agent 在未达深度上限时应拿到受限 delegate_task（支持有限嵌套）。
func TestSubagent_NestedDelegateAvailable(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.LaunchAtDepth(SubagentLaunchRequest{Goal: "嵌套检查"}, 1)
	waitState(sm, h.ID, 2*time.Second)

	llm.mu.Lock()
	defer llm.mu.Unlock()
	hasDelegate := false
	for _, tl := range llm.gotTool {
		for _, tt := range tl {
			if tt.Name == "delegate_task" {
				hasDelegate = true
			}
		}
	}
	if !hasDelegate {
		t.Fatal("未达深度上限的子 agent 应能再委派下一层")
	}
}

// 达到深度上限时不应再给 delegate_task（防递归失控）。
func TestSubagent_NestedBlockedAtMaxDepth(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.LaunchAtDepth(SubagentLaunchRequest{Goal: "最深层"}, sm.maxDepth)
	waitState(sm, h.ID, 2*time.Second)

	llm.mu.Lock()
	defer llm.mu.Unlock()
	for _, tl := range llm.gotTool {
		for _, tt := range tl {
			if tt.Name == "delegate_task" {
				t.Fatalf("已达 maxDepth(%d) 的子 agent 不该再有 delegate_task", sm.maxDepth)
			}
		}
	}
}

func TestSubagent_SteerRunning(t *testing.T) {
	bl := &blockingLLM{entered: make(chan struct{})}
	sm := newTestManager(bl, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "长任务", TimeoutSecs: 30})
	select {
	case <-bl.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("子 agent 没进入运行")
	}
	if err := sm.Steer(h.ID, "改用方案B"); err != nil {
		t.Fatalf("运行中 Steer 应成功: %v", err)
	}
	// 不存在的 id
	if err := sm.Steer("nope", "x"); err == nil {
		t.Fatal("不存在的 id 应报错")
	}
	// 空内容
	if err := sm.Steer(h.ID, "  "); err == nil {
		t.Fatal("空 steer 内容应报错")
	}
	sm.Cancel(h.ID)
}

func TestSubagent_SteerNotRunning(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "快任务"})
	waitState(sm, h.ID, 2*time.Second)
	if err := sm.Steer(h.ID, "晚了"); err == nil {
		t.Fatal("已终结的子代理 Steer 应报错，不能静默丢弃")
	}
}

func TestSteerSubagentTool(t *testing.T) {
	bl := &blockingLLM{entered: make(chan struct{})}
	sm := newTestManager(bl, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "长任务", TimeoutSecs: 30})
	select {
	case <-bl.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("子 agent 没进入运行")
	}
	tool := SteerSubagentTool(sm)
	if _, err := tool.Run(`{"id":"` + h.ID + `","message":"走另一条路"}`); err != nil {
		t.Fatalf("steer 工具应成功: %v", err)
	}
	if _, err := tool.Run(`{"id":"` + h.ID + `"}`); err == nil {
		t.Fatal("缺 message 应报错")
	}
	sm.Cancel(h.ID)
}

// ============================================================================
// P2：自动分解指引
// ============================================================================

func TestDelegationGuide_ContainsSuppression(t *testing.T) {
	g := DelegationGuide()
	if g == "" {
		t.Fatal("委派指引不能为空")
	}
	// 必须同时讲「该拆」和「不该拆」——只有鼓励没有抑制会导致简单任务也乱拆。
	for _, want := range []string{"tasks", "不该拆", "独立", "get_subagent", "steer_subagent"} {
		if !strings.Contains(g, want) {
			t.Fatalf("委派指引缺少关键内容 %q", want)
		}
	}
}

// ============================================================================
// P3 收尾：role 生效 / 排队与执行超时分离
// ============================================================================

// role 必须真正影响子代理的行为指令，而不是赋个默认值就丢弃。
func TestSubagent_RoleReachesChildTask(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	sm.Launch(SubagentLaunchRequest{Goal: "只读调研", Role: "explore"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && llm.callCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.gotMsgs) == 0 {
		t.Fatal("子 agent 没发起调用")
	}
	var all strings.Builder
	for _, m := range llm.gotMsgs[0] {
		all.WriteString(m.Content)
	}
	if !strings.Contains(all.String(), "探索者") {
		t.Fatalf("role=explore 的行为指令没进子任务，实际收到: %s", truncate(all.String(), 200))
	}
}

// 未知 role 归一化为 leaf，不能原样接受任意字符串。
func TestSubagent_UnknownRoleNormalized(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	sm.Launch(SubagentLaunchRequest{Goal: "x", Role: "恶意角色注入"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && llm.callCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	llm.mu.Lock()
	defer llm.mu.Unlock()
	var all strings.Builder
	for _, m := range llm.gotMsgs[0] {
		all.WriteString(m.Content)
	}
	if !strings.Contains(all.String(), "叶子执行者") {
		t.Fatal("未知 role 应归一化为 leaf")
	}
	if strings.Contains(all.String(), "恶意角色注入") {
		t.Fatal("未知 role 内容不该被原样注入")
	}
}

// ============================================================================
// GAP-S1 第一层：只读角色（explore/review）的工具白名单强制
// ============================================================================

// explore/review 的写类工具（edit/write）必须从子 agent 工具清单里摘除——
// 只靠 roleHint 提示词约束的话，模型不守约照样能改文件。
// leaf/implement 不受影响，edit/write 仍然在场。
func TestSubagent_RoleWhitelistBlocksWriteTools(t *testing.T) {
	newMgr := func(script []string) (*SubagentManager, *scriptedLLM) {
		llm := &scriptedLLM{script: script}
		pm := plugins.NewManager("", nil)
		pm.RegisterBuiltin(plugins.Tool{Name: "read", Description: "t", Run: func(string) (string, error) { return "", nil }})
		pm.RegisterBuiltin(plugins.Tool{Name: "edit", Description: "t", Run: func(string) (string, error) { return "", nil }})
		pm.RegisterBuiltin(plugins.Tool{Name: "write", Description: "t", Run: func(string) (string, error) { return "", nil }})
		return NewSubagentManager(SubagentConfig{ParentLLM: llm, ParentSys: "sys", Plugins: pm}), llm
	}
	waitCall := func(llm *scriptedLLM) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && llm.callCount() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	toolSeen := func(llm *scriptedLLM, name string) bool {
		llm.mu.Lock()
		defer llm.mu.Unlock()
		for _, tl := range llm.gotTool {
			for _, tt := range tl {
				if tt.Name == name {
					return true
				}
			}
		}
		return false
	}

	// explore：edit/write 必须被摘除，read 必须保留。
	smExp, llmExp := newMgr([]string{`{"action":"reply","text":"ok"}`})
	smExp.Launch(SubagentLaunchRequest{Goal: "只读调研", Role: "explore"})
	waitCall(llmExp)
	if toolSeen(llmExp, "edit") || toolSeen(llmExp, "write") {
		t.Fatal("explore 角色的子 agent 工具清单里不应出现 edit/write（工具层强制）")
	}
	if !toolSeen(llmExp, "read") {
		t.Fatal("explore 角色的子 agent 工具清单应保留 read")
	}

	// review：同 explore。
	smRev, llmRev := newMgr([]string{`{"action":"reply","text":"ok"}`})
	smRev.Launch(SubagentLaunchRequest{Goal: "只读审查", Role: "review"})
	waitCall(llmRev)
	if toolSeen(llmRev, "edit") || toolSeen(llmRev, "write") {
		t.Fatal("review 角色的子 agent 工具清单里不应出现 edit/write")
	}

	// implement：写类工具保留，不受白名单影响。
	smImp, llmImp := newMgr([]string{`{"action":"reply","text":"ok"}`})
	smImp.Launch(SubagentLaunchRequest{Goal: "动手实现", Role: "implement"})
	waitCall(llmImp)
	if !toolSeen(llmImp, "edit") || !toolSeen(llmImp, "write") {
		t.Fatal("implement 角色的子 agent 工具清单应保留 edit/write")
	}

	// leaf（默认）：同 implement。
	smLeaf, llmLeaf := newMgr([]string{`{"action":"reply","text":"ok"}`})
	smLeaf.Launch(SubagentLaunchRequest{Goal: "独立执行"})
	waitCall(llmLeaf)
	if !toolSeen(llmLeaf, "edit") || !toolSeen(llmLeaf, "write") {
		t.Fatal("leaf（默认）角色的子 agent 工具清单应保留 edit/write")
	}
}

// roleWriteBlocked 只对只读角色返回写类工具名，其余角色返回 nil。
func TestRoleWriteBlocked(t *testing.T) {
	for _, role := range []string{"explore", "review"} {
		blocked := roleWriteBlocked(role)
		if !containsStr(blocked, "edit") || !containsStr(blocked, "write") {
			t.Fatalf("role=%s 应屏蔽 edit/write，实际 %v", role, blocked)
		}
	}
	for _, role := range []string{"leaf", "implement", "未知角色"} {
		if got := roleWriteBlocked(role); got != nil {
			t.Fatalf("role=%s 不应屏蔽任何工具，实际 %v", role, got)
		}
	}
}

// ============================================================================
// 并发实测回归：模型把参数包一层 {"args": ...} 导致 "id required" 死循环
// ============================================================================

// get_subagent 收到 {"args":"{\"id\":\"...\"}"} 包装时应剥壳解析，而不是报 id required。
func TestSubagentTools_UnwrapArgsShell(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "被查询的任务"})
	waitState(sm, h.ID, 2*time.Second)

	get := GetSubagentTool(sm)
	// 字符串化包装（实测形态）
	out, err := get.Run(`{"args":"{\"id\":\"` + h.ID + `\"}"}`)
	if err != nil || !strings.Contains(out, h.ID) {
		t.Fatalf("字符串化包装应剥壳成功：out=%q err=%v", out, err)
	}
	// 对象包装 {"args":{"id":...}}
	if out, err = get.Run(`{"args":{"id":"` + h.ID + `"}}`); err != nil || !strings.Contains(out, h.ID) {
		t.Fatalf("对象包装应剥壳成功：out=%q err=%v", out, err)
	}
	// 裸参数不受影响
	if out, err = get.Run(`{"id":"` + h.ID + `"}`); err != nil || !strings.Contains(out, h.ID) {
		t.Fatalf("裸参数应原样解析：out=%q err=%v", out, err)
	}
	// cancel / steer 同样剥壳
	cancel := CancelSubagentTool(sm)
	if out, err = cancel.Run(`{"args":"{\"id\":\"` + h.ID + `\"}"}`); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("cancel 包装参数应剥壳成功：out=%q err=%v", out, err)
	}
	steer := SteerSubagentTool(sm)
	if _, err = steer.Run(`{"args":"{\"id\":\"` + h.ID + `\",\"message\":\"改方向\"}"}`); err != nil {
		if strings.Contains(err.Error(), "id and message required") {
			t.Fatalf("steer 包装参数没剥壳（误报缺参）：%v", err)
		}
		// 已终结任务报「不在运行中」= 剥壳生效、参数已送达
	}
}

// delegate_task 收到包装壳时也应剥壳（单任务 {"args":"{\"goal\":...}"} 形态）。
func TestDelegateTool_UnwrapArgsShell(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	tool := DelegateTool(sm)
	out, err := tool.Run(`{"args":"{\"goal\":\"包装壳里的任务\"}"}`)
	if err != nil || !strings.Contains(out, "包装壳里的任务") {
		t.Fatalf("delegate 包装参数应剥壳成功：out=%q err=%v", out, err)
	}
}

// ============================================================================
// GAP-S3：运行中进度上报（OnStep → 句柄 Progress → get_subagent //subagents）
// ============================================================================

// 子 agent 的每步活动要写进句柄 Progress：step 开始行 + 工具调用行（含工具名
// 与参数摘要）；超 maxSubagentProgress 只留最近 N 条，长任务不撑爆句柄。
func TestSubagent_ProgressRecorded(t *testing.T) {
	script := make([]string, 0, 26)
	for i := 0; i < 25; i++ {
		script = append(script, `{"action":"tool","tool":"ping","args":"{\"n\":`+strconv.Itoa(i)+`}"}`)
	}
	script = append(script, `{"action":"reply","text":"done"}`)
	llm := &scriptedLLM{script: script}
	sm := newTestManager(llm, nil)
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "进度测试"})
	if st := waitState(sm, h.ID, 5*time.Second); st != SubagentSucceeded {
		t.Fatalf("应成功，实际 %s", st)
	}
	got, _ := sm.Get(h.ID)
	if len(got.Progress) != maxSubagentProgress {
		t.Fatalf("Progress 应截取到最近 %d 条，实际 %d", maxSubagentProgress, len(got.Progress))
	}
	foundTool := false
	for _, p := range got.Progress {
		if strings.Contains(p, "ping(") {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Fatalf("Progress 应包含工具调用行: %v", got.Progress)
	}
	last := got.Progress[len(got.Progress)-1]
	if !strings.HasPrefix(last, "step ") {
		t.Fatalf("最后一条应是收尾 step 开始行: %q", last)
	}
}

// 运行中的子任务在 RenderSubagentsForHuman（/subagents）里可见「进度：step N」行。
// 复用 Cancel 测试的 blockingLLM（阻塞在 LLM 调用 = 稳定的运行中状态）；
// step 开始行在 LLM 调用前写入进度，阻塞期间即可断言。
func TestSubagent_ProgressVisibleWhileRunning(t *testing.T) {
	bl := &blockingLLM{entered: make(chan struct{})}
	pm := plugins.NewManager("", nil)
	pm.RegisterBuiltin(plugins.Tool{Name: "ping", Description: "t", Run: func(string) (string, error) { return "", nil }})
	sm := NewSubagentManager(SubagentConfig{ParentLLM: bl, ParentSys: "sys", Plugins: pm})
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "运行中可见", TimeoutSecs: 60})

	// 等子 agent 进入阻塞的 LLM 调用（step 1 进度已在调用前写入）
	deadline := time.Now().Add(2 * time.Second)
	entered := false
	for time.Now().Before(deadline) && !entered {
		select {
		case <-bl.entered:
			entered = true
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !entered {
		t.Fatal("子 agent 没进入阻塞的 LLM 调用")
	}

	shown := ""
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := sm.Get(h.ID); ok && s.State == SubagentRunning && len(s.Progress) > 0 {
			shown = sm.RenderSubagentsForHuman()
			if strings.Contains(shown, "进度：step") {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(shown, "进度：step") {
		t.Fatalf("运行中应能看到进度行: %q", shown)
	}
	// 收尾：取消阻塞任务（ctx 解除），避免泄漏 goroutine
	sm.Cancel(h.ID)
}
