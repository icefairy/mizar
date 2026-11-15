package agent

import (
	"sync"
)

// ReviewSignals 自学习节奏信号（P2，2026-11-15，对齐 hermes agent/review_signals.py）。
//
// 背景：hermes 把「该不该做后台复盘」拆成两个**独立计数器**公开给插件：
//   - memory 复盘看「用户轮数」（memory.nudge_interval）
//   - skill 复盘看「工具调用迭代数」（skills.creation_nudge_interval）
//
// 为什么不能统一按轮次：一次用户提问里可能连调 30 次工具（一次 skill 复盘机会），
// 也可能连着 10 轮纯聊天（一次工具都没调）。混用一个计数器，前者永远攒不够、
// 后者被无意义地触发。
//
// 另一个关键设计：**只读**。hermes 用 dict 子类把 __setitem__ 全部改成抛错——
// 一个实例传给所有钩子，谁也别想篡改下一个人读到的值。Go 里我们用
// 「返回副本 + 只有 unexported 字段 + 只读 getter」达到同样效果。
type ReviewSignals struct {
	// ToolIterationsSinceSkillReview 距上次技能复盘的工具调用迭代数。
	ToolIterationsSinceSkillReview int
	// UserTurnsSinceMemoryReview 距上次记忆复盘的用户轮数。
	UserTurnsSinceMemoryReview int
	// SkillReviewDue 本轮是否该做技能复盘。
	SkillReviewDue bool
	// MemoryReviewDue 本轮是否该做记忆复盘（已在本轮开始时决定，计数器已归零）。
	MemoryReviewDue bool
}

// reviewSignalsTracker 双计数器追踪器。
//
// 与 BackgroundReview 共用同一组阈值（避免两处真相），但独立维护计数：
// BackgroundReview 决定「真的去跑复盘」，tracker 负责「把信号算出来给钩子看」。
type reviewSignalsTracker struct {
	mu sync.Mutex
	// skillIters 距上次技能复盘的工具调用迭代数。
	skillIters int
	// userTurns 距上次记忆复盘的用户轮数。
	userTurns int
	// skillInterval / memoryInterval 阈值（<=0 关该维度）。
	skillInterval  int
	memoryInterval int
}

// newReviewSignalsTracker 创建追踪器。阈值沿用 BackgroundReview 的轮次配置。
func newReviewSignalsTracker(interval int) *reviewSignalsTracker {
	if interval <= 0 {
		interval = 3
	}
	return &reviewSignalsTracker{skillInterval: interval, memoryInterval: interval}
}

// OnUserTurn 每轮用户消息开始时调用：用户轮数 +1，并算出 memory 是否到期。
// 返回 (memoryDue, signals)：到期时计数器归零（与 hermes「fire 时已归零」一致）。
func (t *reviewSignalsTracker) OnUserTurn() (bool, ReviewSignals) {
	if t == nil {
		return false, ReviewSignals{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.userTurns++
	memoryDue := t.memoryInterval > 0 && t.userTurns >= t.memoryInterval
	if memoryDue {
		t.userTurns = 0
	}
	return memoryDue, t.signalsLocked(memoryDue)
}

// OnToolIteration 每次工具调用后调用：迭代数 +1，并算出 skill 是否到期。
// 返回 (skillDue, signals)：到期时计数器在**钩子返回后**才归零——
// 这里先给出 due=true 但保留计数，由 CommitSkillReview 负责清零
// （hermes 的语义：post_llm_call 看到的计数是钩子时刻的值）。
func (t *reviewSignalsTracker) OnToolIteration() (bool, ReviewSignals) {
	if t == nil {
		return false, ReviewSignals{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.skillIters++
	skillDue := t.skillInterval > 0 && t.skillIters >= t.skillInterval
	return skillDue, t.signalsLocked(false)
}

// CommitSkillReview 技能复盘真的发生了，计数器归零。
func (t *reviewSignalsTracker) CommitSkillReview() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.skillIters = 0
	t.mu.Unlock()
}

// Signals 读取当前信号（只读快照）。
func (t *reviewSignalsTracker) Signals() ReviewSignals {
	if t == nil {
		return ReviewSignals{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.signalsLocked(false)
}

func (t *reviewSignalsTracker) signalsLocked(memoryDue bool) ReviewSignals {
	return ReviewSignals{
		ToolIterationsSinceSkillReview: t.skillIters,
		UserTurnsSinceMemoryReview:     t.userTurns,
		SkillReviewDue:                 t.skillInterval > 0 && t.skillIters >= t.skillInterval,
		MemoryReviewDue:                memoryDue,
	}
}
