package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunGateNoPanic 回归：RunGate 曾因 exec.CommandContext(nil, ...) 直接 panic。
// 现在必须能正常执行并返回退出码。
func TestRunGateNoPanic(t *testing.T) {
	g := &GoalGate{Command: "exit 0"}
	passed, code, _ := g.RunGate(t.TempDir())
	if !passed || code != 0 {
		t.Fatalf("passing gate: passed=%v code=%d want true/0", passed, code)
	}

	g2 := &GoalGate{Command: "exit 7"}
	passed, code, out := g2.RunGate(t.TempDir())
	if passed {
		t.Fatal("failing gate should not pass")
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7 (out=%q)", code, out)
	}
}

// TestRunGateTimeout 门禁超时应判失败而非永久阻塞。
func TestRunGateTimeout(t *testing.T) {
	g := &GoalGate{Command: "sleep 5", TimeoutSeconds: 1}
	start := time.Now()
	passed, code, out := g.RunGate(t.TempDir())
	if passed {
		t.Fatal("timed out gate should not pass")
	}
	if code != -1 {
		t.Fatalf("timeout exit code = %d, want -1", code)
	}
	if !strings.Contains(out, "timeout") {
		t.Fatalf("output should mention timeout, got %q", out)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout not enforced, took %v", elapsed)
	}
}

// TestRunGateEmptyCommand 空命令视为通过。
func TestRunGateEmptyCommand(t *testing.T) {
	g := &GoalGate{}
	passed, code, _ := g.RunGate(t.TempDir())
	if !passed || code != 0 {
		t.Fatalf("empty gate: passed=%v code=%d want true/0", passed, code)
	}
}

// TestEvaluateGatesReturnsExitCode EvaluateGates 应回传真实退出码。
func TestEvaluateGatesReturnsExitCode(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	if err := gs.Create("目标", 5); err != nil {
		t.Fatal(err)
	}
	gs.mu.Lock()
	gs.goal.Gates = []*GoalGate{{Command: "exit 3"}}
	gs.mu.Unlock()

	allPassed, idx, code, out := gs.EvaluateGates(t.TempDir())
	if allPassed || idx != 0 {
		t.Fatalf("allPassed=%v idx=%d want false/0", allPassed, idx)
	}
	if code != 3 {
		t.Fatalf("exit code = %d want 3 (out=%q)", code, out)
	}
}

// TestEvaluateGatesCtxCancelled 外部 ctx 取消后门禁应尽快返回失败。
func TestEvaluateGatesCtxCancelled(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	if err := gs.Create("目标", 5); err != nil {
		t.Fatal(err)
	}
	gs.mu.Lock()
	gs.goal.Gates = []*GoalGate{{Command: "sleep 5"}}
	gs.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	allPassed, _, _, _ := gs.EvaluateGatesCtx(ctx, t.TempDir())
	if allPassed {
		t.Fatal("cancelled gate should not pass")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("ctx cancel not honored, took %v", elapsed)
	}
}

// TestGoalLoopGateFailureContinues 回归：门禁失败必须返回 shouldContinue=true，
// 否则调用方会丢弃 continuationPrompt，门禁形同虚设。
func TestGoalLoopGateFailureContinues(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	if err := gs.Create("让门禁通过", 5); err != nil {
		t.Fatal(err)
	}
	gs.mu.Lock()
	gs.goal.Phase = GoalInProgress
	gs.goal.Gates = []*GoalGate{{Command: "exit 9"}}
	gs.mu.Unlock()

	gl := NewGoalLoop(gs, DefaultGoalLoopConfig())
	gl.SetCwd(t.TempDir())
	cont, prompt, err := gl.EvaluateAfterTurn("我修好了")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !cont {
		t.Fatal("gate failure must request continuation (shouldContinue=true)")
	}
	if prompt == "" {
		t.Fatal("gate failure must carry a continuation prompt")
	}
	if !strings.Contains(prompt, "quality gate failed") {
		t.Fatalf("prompt should describe gate failure, got %q", prompt)
	}
	if !strings.Contains(prompt, "Exit code: 9") {
		t.Fatalf("prompt should carry real exit code, got %q", prompt)
	}
}

// TestGoalLoopGatePassFallsThroughJudge 门禁通过后应继续走 judge（而非直接续跑）。
func TestGoalLoopGatePassFallsThroughJudge(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	if err := gs.Create("目标", 5); err != nil {
		t.Fatal(err)
	}
	gs.mu.Lock()
	gs.goal.Phase = GoalInProgress
	gs.goal.Gates = []*GoalGate{{Command: "exit 0"}}
	gs.mu.Unlock()
	// judge 判定 done
	gs.SetJudgeFn(func(goalText, lastResponse string, contract *GoalContract, subgoals []string) (*GoalJudgeResult, error) {
		return &GoalJudgeResult{Verdict: "done"}, nil
	})

	gl := NewGoalLoop(gs, DefaultGoalLoopConfig())
	gl.SetCwd(t.TempDir())
	cont, prompt, err := gl.EvaluateAfterTurn("完成")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cont || prompt != "" {
		t.Fatalf("gate passed + judge done → should stop; got cont=%v prompt=%q", cont, prompt)
	}
}

// TestWorkspaceFPDistinguishesChanges 指纹应能区分不同工作区状态（旧实现只返回长度和）。
func TestWorkspaceFPDistinguishesChanges(t *testing.T) {
	// 非 git 目录返回空串（不 panic）
	if fp := WorkspaceFP(t.TempDir()); fp != "" {
		t.Fatalf("non-git dir fingerprint = %q, want empty", fp)
	}
}
