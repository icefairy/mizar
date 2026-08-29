// Package agent 实现会话目标服务（复刻 deepseek-harness goal）。
//
// 简化版：单会话单目标，生命周期 pending → in_progress → completed/blocked。
// create_goal 需人类直接消息触发（通过 HumanTurn 标记）；complete/blocked 可由模型自动报告。
package agent

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// GoalPhase 目标阶段。
type GoalPhase string

const (
	GoalPending    GoalPhase = "pending"
	GoalInProgress GoalPhase = "in_progress"
	GoalCompleted  GoalPhase = "completed"
	GoalBlocked    GoalPhase = "blocked"
	GoalPaused     GoalPhase = "paused"
)

// Goal 会话内单一长期目标。
type Goal struct {
	ID              string      `json:"id"`
	Revision        int         `json:"revision"`
	Objective       string      `json:"objective"`
	Phase           GoalPhase   `json:"phase"`
	RoundsStarted   int         `json:"roundsStarted"`
	MaxGoalRounds   int         `json:"maxGoalRounds,omitempty"`
	BlockedReason   string      `json:"blockedReason,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}

// GoalService 会话目标服务（线程安全）。
type GoalService struct {
	mu               sync.RWMutex
	goal             *Goal
	humanTurnSince   atomic.Int64 // UnixNano：最近一次人类直接消息的时间戳
	blockThreshold   int          // 连续自报 blocked 所需的最低轮数（默认 3）
}

// NewGoalService 创建目标服务。
func NewGoalService() *GoalService {
	return &GoalService{blockThreshold: 3}
}

// SetBlockThreshold 设置自报 blocked 的阈值（正整数）。
func (g *GoalService) SetBlockThreshold(n int) {
	if n >= 1 {
		g.blockThreshold = n
	}
}

// MarkHumanTurn 记录一次人类直接消息（create/edit/pause/resume 需要人类权限）。
func (g *GoalService) MarkHumanTurn() {
	g.humanTurnSince.Store(time.Now().UnixNano())
}

// Get 返回当前目标（nil = 无目标）。
func (g *GoalService) Get() *Goal {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.goal == nil {
		return nil
	}
	cp := *g.goal
	return &cp
}

// Create 创建新目标（要求人类直接消息权限）。
func (g *GoalService) Create(objective string, maxRounds int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal != nil && g.goal.Phase != GoalCompleted && g.goal.Phase != GoalBlocked {
		return fmt.Errorf("goal: existing active goal %q; complete or block it first", g.goal.ID)
	}
	if !g.hasHumanTurn() {
		return fmt.Errorf("goal: create requires a direct human message in current turn")
	}
	now := time.Now()
	g.goal = &Goal{
		ID:            fmt.Sprintf("goal-%d", now.UnixNano()),
		Revision:      1,
		Objective:     objective,
		Phase:         GoalPending,
		RoundsStarted: 0,
		MaxGoalRounds: maxRounds,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return nil
}

// Update 更新目标（action: edit/pause/resume/complete/blocked）。
// 需要携带正确的 goal_id 和 revision（read-before-write）。
func (g *GoalService) Update(id, revisionStr, action, objective string, maxRounds int, blockedReason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil || g.goal.ID != id {
		return fmt.Errorf("goal: goal %q not found", id)
	}
	// 解析 revision
	var rev int
	fmt.Sscanf(revisionStr, "%d", &rev)
	if rev != g.goal.Revision {
		return fmt.Errorf("goal: revision mismatch (expected %d, got %s)", g.goal.Revision, revisionStr)
	}
	g.goal.Revision++
	g.goal.UpdatedAt = time.Now()
	switch action {
	case "edit":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: edit requires human authority")
		}
		if objective != "" {
			g.goal.Objective = objective
		}
		if maxRounds > 0 {
			g.goal.MaxGoalRounds = maxRounds
		}
	case "pause":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: pause requires human authority")
		}
		g.goal.Phase = GoalPaused
	case "resume":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: resume requires human authority")
		}
		g.goal.Phase = GoalInProgress
		g.goal.RoundsStarted++
	case "complete":
		g.goal.Phase = GoalCompleted
	case "blocked":
		if blockedReason == "" {
			return fmt.Errorf("goal: blocked requires blocked_reason")
		}
		// 自报 blocked 需要满足阈值（连续同条件轮数）
		if g.goal.Phase != GoalBlocked {
			// 首次 blocked：允许
			g.goal.Phase = GoalBlocked
			g.goal.BlockedReason = blockedReason
		} else {
			// 已 blocked：检查阈值
			g.goal.RoundsStarted++
			if g.goal.RoundsStarted < g.blockThreshold {
				return fmt.Errorf("goal: blocked condition must persist for %d consecutive rounds (current: %d)", g.blockThreshold, g.goal.RoundsStarted)
			}
			g.goal.BlockedReason = blockedReason
		}
	default:
		return fmt.Errorf("goal: unknown action %q (expected edit/pause/resume/complete/blocked)", action)
	}
	return nil
}

func (g *GoalService) hasHumanTurn() bool {
	// 简化：只要 humanTurnSince 在 5 秒内设置过即视为有效（足够覆盖一次工具调用回合）
	return time.Now().UnixNano()-g.humanTurnSince.Load() < 5_000_000_000
}

// RenderJSON 将当前目标渲染为工具调用的 JSON 输出。
func (g *GoalService) RenderJSON() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.goal == nil {
		return `{"goal": null}`
	}
	return fmt.Sprintf(`{"goal": {"id": %q, "revision": %d, "objective": %q, "phase": %q, "roundsStarted": %d, "maxGoalRounds": %d, "blockedReason": %q}}`,
		g.goal.ID, g.goal.Revision, g.goal.Objective, g.goal.Phase,
		g.goal.RoundsStarted, g.goal.MaxGoalRounds, g.goal.BlockedReason)
}
