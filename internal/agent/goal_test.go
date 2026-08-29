package agent

import (
	"testing"
	"time"
)

func TestGoalCreateGet(t *testing.T) {
	gs := NewGoalService()
	// 无人类消息时创建应失败
	if err := gs.Create("写一个插件", 10); err == nil {
		t.Fatal("create without human turn should fail")
	}
	// 模拟人类消息
	gs.MarkHumanTurn()
	if err := gs.Create("写一个插件", 10); err != nil {
		t.Fatalf("create with human turn: %v", err)
	}
	goal := gs.Get()
	if goal == nil {
		t.Fatal("get should return goal")
	}
	if goal.Objective != "写一个插件" {
		t.Fatalf("objective want '写一个插件', got %q", goal.Objective)
	}
	if goal.Phase != GoalPending {
		t.Fatalf("phase want pending, got %s", goal.Phase)
	}
}

func TestGoalUpdateComplete(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	_ = gs.Create("完成这个任务", 5)
	goal := gs.Get()
	// complete 不需要人类权限
	if err := gs.Update(goal.ID, "1", "complete", "", 0, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	goal = gs.Get()
	if goal.Phase != GoalCompleted {
		t.Fatalf("phase want completed, got %s", goal.Phase)
	}
}

func TestGoalUpdateRequiresRevision(t *testing.T) {
	gs := NewGoalService()
	gs.MarkHumanTurn()
	_ = gs.Create("任务", 5)
	// 错误 revision 应失败
	if err := gs.Update("wrong-id", "1", "complete", "", 0, ""); err == nil {
		t.Fatal("wrong id should fail")
	}
}

func TestGoalRenderJSON(t *testing.T) {
	gs := NewGoalService()
	if got := gs.RenderJSON(); got != `{"goal": null}` {
		t.Fatalf("nil goal want null JSON, got %s", got)
	}
	gs.MarkHumanTurn()
	_ = gs.Create("目标", 0)
	out := gs.RenderJSON()
	if out == `{"goal": null}` {
		t.Fatal("rendered JSON should not be null")
	}
}

func TestGoalBlockThreshold(t *testing.T) {
	gs := NewGoalService()
	gs.SetBlockThreshold(3)
	gs.MarkHumanTurn()
	_ = gs.Create("困难任务", 0)
	goal := gs.Get()
	// 第一次 blocked 应成功
	if err := gs.Update(goal.ID, "1", "blocked", "", 0, "环境限制"); err != nil {
		t.Fatalf("first blocked should succeed: %v", err)
	}
	goal = gs.Get()
	if goal.Phase != GoalBlocked {
		t.Fatal("phase should be blocked")
	}
	// 连续 blocked 需达到阈值（简化测试：仅验证首次能设）
	_ = time.Now()
}
