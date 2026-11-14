package main

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// TestSubagentsCommand delegate_task 派发后 /subagents 命令应显示
// 子任务列表与状态（含批次与结果摘要）；未派发时给空态提示。
// LLM 用 starttask_lock_test.go 里已有的 stubLLM（跑一轮即 reply）。
func TestSubagentsCommand(t *testing.T) {
	pm := plugins.NewManager("", nil)
	sm := agent.NewSubagentManager(agent.SubagentConfig{ParentLLM: stubLLM{}, ParentSys: "sys", Plugins: pm})

	a := &agent.Agent{}
	a.Commands = agent.NewCommandRegistry()
	a.SubAgents = sm
	a.Commands.Register(agent.Command{
		Name: "subagents",
		Run:  func(args string) (string, error) { return sm.RenderSubagentsForHuman(), nil },
	})

	// 空态
	if _, out, _ := a.Commands.Dispatch("/subagents"); !strings.Contains(out, "还没有派发过") {
		t.Fatalf("空态提示: %q", out)
	}

	// 派发两个（batch fan-out），等子任务跑完
	if _, err := sm.LaunchBatch([]agent.SubagentLaunchRequest{
		{Goal: "调研 A"},
		{Goal: "调研 B"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, h := range sm.List() {
			switch h.State {
			case agent.SubagentSucceeded, agent.SubagentFailed, agent.SubagentCancelled:
			default:
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	_, out, err := a.Commands.Dispatch("/subagents")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub-2-1") || !strings.Contains(out, "sub-3-2") {
		t.Fatalf("应列出两个句柄 id（全局计数器编号）: %q", out)
	}
	if !strings.Contains(out, "调研 A") || !strings.Contains(out, "调研 B") {
		t.Fatalf("应显示任务描述: %q", out)
	}
	if !strings.Contains(out, "batch-") || !strings.Contains(out, "已完成") {
		t.Fatalf("应显示批次与状态: %q", out)
	}
	// stubLLM 返回 reply 文本应作为结果摘要可见
	if !strings.Contains(out, "ok") {
		t.Fatalf("应显示结果摘要: %q", out)
	}
}
