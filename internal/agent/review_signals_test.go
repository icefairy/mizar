package agent

import "testing"

func TestTrackerUserTurns(t *testing.T) {
	tr := newReviewSignalsTracker(3)
	// 前两轮不到期
	for i := 1; i <= 2; i++ {
		due, sig := tr.OnUserTurn()
		if due {
			t.Errorf("第 %d 轮不该到期", i)
		}
		if sig.UserTurnsSinceMemoryReview != i {
			t.Errorf("轮数应 %d，got %d", i, sig.UserTurnsSinceMemoryReview)
		}
	}
	// 第 3 轮到期并清零
	due, sig := tr.OnUserTurn()
	if !due {
		t.Error("第 3 轮应到期")
	}
	if sig.MemoryReviewDue != true {
		t.Error("signals 里 MemoryReviewDue 应为 true")
	}
	// 归零后下一轮从 1 开始
	_, sig = tr.OnUserTurn()
	if sig.UserTurnsSinceMemoryReview != 1 {
		t.Errorf("到期后应归零重计，got %d", sig.UserTurnsSinceMemoryReview)
	}
}

func TestTrackerToolIterations(t *testing.T) {
	tr := newReviewSignalsTracker(3)
	for i := 1; i <= 2; i++ {
		due, sig := tr.OnToolIteration()
		if due {
			t.Errorf("第 %d 次迭代不该到期", i)
		}
		if sig.ToolIterationsSinceSkillReview != i {
			t.Errorf("迭代数应 %d，got %d", i, sig.ToolIterationsSinceSkillReview)
		}
	}
	// 第 3 次到期，但计数保留（等 CommitSkillReview 才清零）
	due, sig := tr.OnToolIteration()
	if !due {
		t.Error("第 3 次迭代应到期")
	}
	if !sig.SkillReviewDue {
		t.Error("SkillReviewDue 应为 true")
	}
	if sig.ToolIterationsSinceSkillReview != 3 {
		t.Errorf("到期时计数应保持 3（钩子看到的是钩子时刻的值），got %d", sig.ToolIterationsSinceSkillReview)
	}
	// 提交后清零
	tr.CommitSkillReview()
	if got := tr.Signals().ToolIterationsSinceSkillReview; got != 0 {
		t.Errorf("提交后应归零，got %d", got)
	}
}

// TestTwoCountersIndependent 双计数器必须互不影响（这是 P2 的核心价值）。
func TestTwoCountersIndependent(t *testing.T) {
	tr := newReviewSignalsTracker(5)
	// 连调 10 次工具，但一轮用户消息都没增加
	for i := 0; i < 10; i++ {
		tr.OnToolIteration()
	}
	sig := tr.Signals()
	if !sig.SkillReviewDue {
		t.Error("工具迭代 10 次 ≥ 阈值 5，技能应到期")
	}
	if sig.MemoryReviewDue || sig.UserTurnsSinceMemoryReview != 0 {
		t.Error("用户轮数不该被工具调用影响")
	}
	// 关掉阈值则不触发
	off := newReviewSignalsTracker(3)
	off.skillInterval = 0
	off.memoryInterval = 0
	for i := 0; i < 10; i++ {
		if due, _ := off.OnToolIteration(); due {
			t.Error("阈值 0 时不该到期")
		}
		if due, _ := off.OnUserTurn(); due {
			t.Error("阈值 0 时不该到期")
		}
	}
}

func TestTrackerNilSafe(t *testing.T) {
	var tr *reviewSignalsTracker
	if due, _ := tr.OnUserTurn(); due {
		t.Error("nil 应返回 false")
	}
	if due, _ := tr.OnToolIteration(); due {
		t.Error("nil 应返回 false")
	}
	tr.CommitSkillReview() // 不 panic
	if sig := tr.Signals(); sig.SkillReviewDue || sig.MemoryReviewDue {
		t.Error("nil 应返回零值")
	}
}

// TestReviewSignalsInStepEnd HookContext 应带只读快照。
func TestReviewSignalsInStepEnd(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{}, pm)
	a.Hooks = NewHooks() // New 的 Hooks 默认是 nil（首次 Run 时才补）
	a.ReviewSignals = newReviewSignalsTracker(2)
	a.ReviewSignals.OnToolIteration()
	a.ReviewSignals.OnToolIteration()

	var got *ReviewSignals
	a.Hooks.OnStepEnd(func(ctx *HookContext) error {
		got = ctx.ReviewSignals
		return nil
	})
	a.Hooks.fireStepEnd(&HookContext{ReviewSignals: a.reviewSignalsSnapshot()}, a.logf)
	if got == nil {
		t.Fatal("StepEnd 应带 ReviewSignals")
	}
	if got.ToolIterationsSinceSkillReview != 2 {
		t.Errorf("快照值应 2，got %d", got.ToolIterationsSinceSkillReview)
	}
	// 修改快照不影响 tracker（值拷贝语义）
	got.ToolIterationsSinceSkillReview = 999
	if a.ReviewSignals.Signals().ToolIterationsSinceSkillReview != 2 {
		t.Error("修改快照不应影响真实计数（只读语义）")
	}
}
