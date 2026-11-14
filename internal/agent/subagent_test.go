package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
}

func (s *scriptedLLM) Chat(msgs []Message) (string, error) { return s.next(nil) }

func (s *scriptedLLM) ChatWithTools(msgs []Message, tools []plugins.Tool) (string, error) {
	return s.next(tools)
}

func (s *scriptedLLM) next(tools []plugins.Tool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 存一份快照（主循环可能复用底层数组）。
	snap := make([]plugins.Tool, len(tools))
	copy(snap, tools)
	s.gotTool = append(s.gotTool, snap)
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

func TestSubagent_ToolsFilteredAgainstRecursion(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	// 用一个「卡住」的任务，在子 agent 跑起来时检查它收到的工具清单。
	// 简化：直接跑一个正常任务后检查 gotTool 里不含 delegate_task。
	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "检查工具集"})
	waitState(sm, h.ID, 2*time.Second)
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.gotTool) == 0 {
		t.Fatal("子 agent 没发起 LLM 调用")
	}
	for _, tl := range llm.gotTool {
		for _, tt := range tl {
			if isDelegationTool(tt.Name) {
				t.Fatalf("委派工具 %s 泄漏进子 agent 工具集，会导致无限递归", tt.Name)
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
		Count   int               `json:"count"`
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
