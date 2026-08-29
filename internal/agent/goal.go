// Package agent 实现会话目标服务（复刻 deepseek-harness goal + pi-goal）。
//
// 增强版：支持 GoalContract（结构化完成契约）、GoalJudge（辅助模型自动判断）、
// QualityGate（确定性门禁命令）以及 GoalLoop（每轮后自动 judge 并续跑）。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// GoalPhase：目标阶段
// ============================================================================

type GoalPhase string

const (
	GoalPending    GoalPhase = "pending"
	GoalInProgress GoalPhase = "in_progress"
	GoalCompleted  GoalPhase = "completed"
	GoalBlocked    GoalPhase = "blocked"
	GoalPaused     GoalPhase = "paused"
)

// ============================================================================
// GoalContract：结构化完成契约（复刻 hermes GoalContract）
// ============================================================================

// GoalContract 目标完成契约，定义"什么是 done"的客观标准。
type GoalContract struct {
	Outcome      string `json:"outcome"`       // 最终必须成立的状态
	Verification string `json:"verification"`  // 证明完成的具体测试/命令/产物
	Constraints  string `json:"constraints"`   // 不得破坏的约束
	Boundaries   string `json:"boundaries"`    // 工作范围（文件/目录/工具）
	StopWhen     string `json:"stop_when"`     // 遇到此条件应停止并请求人工输入
}

// IsEmpty 返回 true 当所有字段均为空。
func (c *GoalContract) IsEmpty() bool {
	return c.Outcome == "" && c.Verification == "" &&
		c.Constraints == "" && c.Boundaries == "" && c.StopWhen == ""
}

// RenderBlock 渲染为非空字段的编号块，空契约返回空串。
func (c *GoalContract) RenderBlock() string {
	if c.IsEmpty() {
		return ""
	}
	var fields = []struct{ label, key string }{
		{"Outcome", "outcome"},
		{"Verification", "verification"},
		{"Constraints", "constraints"},
		{"Boundaries", "boundaries"},
		{"Stop when blocked", "stop_when"},
	}
	var sb string
	for _, f := range fields {
		val := ""
		switch f.key {
		case "outcome":
			val = c.Outcome
		case "verification":
			val = c.Verification
		case "constraints":
			val = c.Constraints
		case "boundaries":
			val = c.Boundaries
		case "stop_when":
			val = c.StopWhen
		}
		if val != "" {
			sb += fmt.Sprintf("- %s: %s\n", f.label, val)
		}
	}
	return sb
}

// ============================================================================
// GoalGate：质量门禁（复刻 hermes GoalGate）
// ============================================================================

// GoalGate 确定性 shell 命令，必须在 judge 宣布 done 之前通过。
type GoalGate struct {
	Command          string        `json:"command"`
	TimeoutSeconds   int           `json:"timeout_seconds"`
	MaxRetries       int           `json:"max_retries"`
	Attempts         int           `json:"attempts"`
	LastExitCode     *int          `json:"last_exit_code,omitempty"`
	LastOutputTail   string        `json:"last_output_tail,omitempty"`
	LastFailedFP     string        `json:"last_failed_fingerprint,omitempty"`
}

const (
	defaultGateTimeout  = 300
	defaultGateMaxRetry = 3
	gateOutputTailChars = 3000
)

// RunGate 执行门禁命令，返回 (passed, exitCode, outputTail)。
func (g *GoalGate) RunGate(cwd string) (bool, int, string) {
	if g.Command == "" {
		return true, 0, ""
	}
	timeout := g.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultGateTimeout
	}
	cmd := exec.CommandContext(nil, "sh", "-c", g.Command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	tail := string(out)
	if len(tail) > gateOutputTailChars {
		tail = tail[len(tail)-gateOutputTailChars:]
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return false, exitErr.ExitCode(), tail
		}
		return false, -1, tail
	}
	return true, 0, tail
}

// WorkspaceFP cheap workspace fingerprint（git status + HEAD）。
func WorkspaceFP(cwd string) string {
	if cwd == "" {
		cwd = "."
	}
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headOut, headErr := headCmd.Output()
	if headErr != nil {
		return ""
	}
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOut, statusErr := statusCmd.Output()
	if statusErr != nil {
		return ""
	}
	data := string(headOut) + "\n" + string(statusOut)
	h := time.Now().UnixNano() // 简化：不做 crypto hash，用时间戳区分
	_ = h
	_ = data
	return fmt.Sprintf("%x", len(data)+len(string(headOut)))
}

// ============================================================================
// GoalJudgeResult：judge 的输出
// ============================================================================

// GoalJudgeResult judge 模型的判断结果。
type GoalJudgeResult struct {
	Verdict    string `json:"verdict"`    // "done" | "continue" | "wait"
	Reason     string `json:"reason"`     // 原因说明
	WaitPID    *int   `json:"wait_on_pid,omitempty"`
	WaitSecs   *int   `json:"wait_for_seconds,omitempty"`
}

// ============================================================================
// Goal：会话内单一长期目标
// ============================================================================

// Goal 会话内单一长期目标。
type Goal struct {
	ID              string         `json:"id"`
	Revision        int            `json:"revision"`
	Objective       string         `json:"objective"`
	Phase           GoalPhase      `json:"phase"`
	RoundsStarted   int            `json:"roundsStarted"`
	MaxGoalRounds   int            `json:"maxGoalRounds,omitempty"`
	BlockedReason   string         `json:"blockedReason,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	// 新增字段
	Contract        *GoalContract  `json:"contract,omitempty"`
	Gates           []*GoalGate    `json:"gates,omitempty"`
	Subgoals        []string       `json:"subgoals,omitempty"`
}

// ============================================================================
// GoalService：会话目标服务（线程安全）
// ============================================================================

// GoalService 会话目标服务（线程安全）。
type GoalService struct {
	mu               sync.RWMutex
	goal             *Goal
	humanTurnSince   atomic.Int64
	blockThreshold   int
	// Judge 回调：调用辅助模型判断目标是否完成（nil = 不启用 judge）
	JudgeFn func(goalText, lastResponse string, contract *GoalContract, subgoals []string) (*GoalJudgeResult, error)
	// 连续 judge parse 失败计数
	consecutiveParseFailures int
	// 连续 transport 失败计数
	consecutiveTransportFailures int
}

// NewGoalService 创建目标服务。
func NewGoalService() *GoalService {
	return &GoalService{blockThreshold: 3}
}

// SetBlockThreshold 设置自报 blocked 的阈值。
func (g *GoalService) SetBlockThreshold(n int) {
	if n >= 1 {
		g.blockThreshold = n
	}
}

// SetJudgeFn 设置 judge 回调。
func (g *GoalService) SetJudgeFn(fn func(string, string, *GoalContract, []string) (*GoalJudgeResult, error)) {
	g.JudgeFn = fn
}

// MarkHumanTurn 记录一次人类直接消息。
func (g *GoalService) MarkHumanTurn() {
	g.humanTurnSince.Store(time.Now().UnixNano())
}

func (g *GoalService) hasHumanTurn() bool {
	return time.Now().UnixNano()-g.humanTurnSince.Load() < 5_000_000_000
}

// Get 返回当前目标。
func (g *GoalService) Get() *Goal {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.goal == nil {
		return nil
	}
	cp := *g.goal
	if g.goal.Contract != nil {
		c := *g.goal.Contract
		cp.Contract = &c
	}
	cp.Gates = make([]*GoalGate, len(g.goal.Gates))
	for i, gt := range g.goal.Gates {
		gtCopy := *gt
		cp.Gates[i] = &gtCopy
	}
	cp.Subgoals = make([]string, len(g.goal.Subgoals))
	copy(cp.Subgoals, g.goal.Subgoals)
	return &cp
}

// Create 创建新目标。
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

// Update 更新目标状态。
func (g *GoalService) Update(id, revisionStr, action, objective string, maxRounds int, blockedReason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil || g.goal.ID != id {
		return fmt.Errorf("goal: goal %q not found", id)
	}
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
		if g.goal.Phase != GoalBlocked {
			g.goal.Phase = GoalBlocked
			g.goal.BlockedReason = blockedReason
		} else {
			g.goal.RoundsStarted++
			if g.goal.RoundsStarted < g.blockThreshold {
				return fmt.Errorf("goal: blocked condition must persist for %d consecutive rounds (current: %d)", g.blockThreshold, g.goal.RoundsStarted)
			}
			g.goal.BlockedReason = blockedReason
		}
	case "add_gate":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: add_gate requires human authority")
		}
		// args 为 JSON: {command, timeout_seconds?, max_retries?}
		var gate struct {
			Command          string `json:"command"`
			TimeoutSeconds   int    `json:"timeout_seconds"`
			MaxRetries       int    `json:"max_retries"`
		}
		if err := json.Unmarshal([]byte(objective), &gate); err != nil || gate.Command == "" {
			return fmt.Errorf("goal: add_gate requires {command}")
		}
		gateObj := &GoalGate{
			Command:          gate.Command,
			TimeoutSeconds:   gate.TimeoutSeconds,
			MaxRetries:       gate.MaxRetries,
		}
		if gateObj.TimeoutSeconds <= 0 {
			gateObj.TimeoutSeconds = defaultGateTimeout
		}
		if gateObj.MaxRetries <= 0 {
			gateObj.MaxRetries = defaultGateMaxRetry
		}
		g.goal.Gates = append(g.goal.Gates, gateObj)
	case "remove_gate":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: remove_gate requires human authority")
		}
		idx, _ := fmt.Sscanf(objective, "%d", 0)
		_ = idx
		// 简化：从末尾移除（后续可改为按 command 匹配）
		if len(g.goal.Gates) > 0 {
			g.goal.Gates = g.goal.Gates[:len(g.goal.Gates)-1]
		}
	case "add_subgoal":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: add_subgoal requires human authority")
		}
		if objective != "" {
			g.goal.Subgoals = append(g.goal.Subgoals, objective)
		}
	case "clear_subgoals":
		if !g.hasHumanTurn() {
			return fmt.Errorf("goal: clear_subgoals requires human authority")
		}
		g.goal.Subgoals = nil
	default:
		return fmt.Errorf("goal: unknown action %q (expected edit/pause/resume/complete/blocked/add_gate/remove_gate/add_subgoal/clear_subgoals)", action)
	}
	return nil
}

// AddGate 添加门禁命令（非锁内，供 /goal gate add 调用）。
func (g *GoalService) AddGate(command string, timeoutSeconds, maxRetries int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil {
		return fmt.Errorf("goal: no active goal")
	}
	if !g.hasHumanTurn() {
		return fmt.Errorf("goal: add_gate requires human authority")
	}
	gate := &GoalGate{
		Command:          command,
		TimeoutSeconds:   timeoutSeconds,
		MaxRetries:       maxRetries,
	}
	g.goal.Gates = append(g.goal.Gates, gate)
	return nil
}

// RemoveGate 移除最后一个门禁。
func (g *GoalService) RemoveGate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil {
		return fmt.Errorf("goal: no active goal")
	}
	if !g.hasHumanTurn() {
		return fmt.Errorf("goal: remove_gate requires human authority")
	}
	if len(g.goal.Gates) == 0 {
		return fmt.Errorf("goal: no gates to remove")
	}
	g.goal.Gates = g.goal.Gates[:len(g.goal.Gates)-1]
	return nil
}

// AddSubgoal 添加子目标。
func (g *GoalService) AddSubgoal(text string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil {
		return fmt.Errorf("goal: no active goal")
	}
	if !g.hasHumanTurn() {
		return fmt.Errorf("goal: add_subgoal requires human authority")
	}
	g.goal.Subgoals = append(g.goal.Subgoals, text)
	return nil
}

// ClearSubgoals 清除所有子目标。
func (g *GoalService) ClearSubgoals() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goal == nil {
		return fmt.Errorf("goal: no active goal")
	}
	if !g.hasHumanTurn() {
		return fmt.Errorf("goal: clear_subgoals requires human authority")
	}
	g.goal.Subgoals = nil
	return nil
}

// EvaluateGates 运行所有门禁，返回 (allPassed, failedGateIndex, output)。
func (g *GoalService) EvaluateGates(cwd string) (bool, int, string) {
	g.mu.RLock()
	gates := g.goal.Gates
	g.mu.RUnlock()
	for i, gate := range gates {
		passed, _, output := gate.RunGate(cwd)
		if !passed {
			return false, i, output
		}
	}
	return true, -1, ""
}

// RenderJSON 渲染为目标 JSON。
func (g *GoalService) RenderJSON() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.goal == nil {
		return `{"goal": null}`
	}
	b, _ := json.Marshal(g.goal)
	return fmt.Sprintf(`{"goal": %s}`, string(b))
}

// RenderStatus 渲染为目标状态文本（供 /goal status 使用）。
func (g *GoalService) RenderStatus() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.goal == nil {
		return "✗ 当前没有活跃目标。使用 /goal <目标描述> 创建新目标。"
	}
	phaseLabel := map[GoalPhase]string{
		GoalPending:    "待处理",
		GoalInProgress: "进行中",
		GoalPaused:     "已暂停",
		GoalCompleted:  "已完成",
		GoalBlocked:    "已阻塞",
	}
	label := phaseLabel[g.goal.Phase]
	if g.goal.BlockedReason != "" {
		label += fmt.Sprintf("（%s）", g.goal.BlockedReason)
	}
	msg := fmt.Sprintf("🎯 当前目标\n  目标：%s\n  状态：%s\n  轮次：%d",
		g.goal.Objective, label, g.goal.RoundsStarted)
	if g.goal.MaxGoalRounds > 0 {
		msg += fmt.Sprintf("（上限 %d 轮）", g.goal.MaxGoalRounds)
	}
	if g.goal.Contract != nil && !g.goal.Contract.IsEmpty() {
		msg += "\n  ─── 完成契约 ───\n" + g.goal.Contract.RenderBlock()
	}
	if len(g.goal.Gates) > 0 {
		msg += fmt.Sprintf("\n  ─── 质量门禁 (%d 条) ───\n", len(g.goal.Gates))
		for i, gate := range g.goal.Gates {
			msg += fmt.Sprintf("  %d. $ %s\n", i+1, gate.Command)
		}
	}
	if len(g.goal.Subgoals) > 0 {
		msg += fmt.Sprintf("\n  ─── 子目标 (%d 条) ───\n", len(g.goal.Subgoals))
		for i, s := range g.goal.Subgoals {
			msg += fmt.Sprintf("  %d. %s\n", i+1, s)
		}
	}
	return msg
}

// JudgeGoal 调用 judge 函数判断目标状态（外部调用，锁外）。
func (g *GoalService) JudgeGoal(goalText, lastResponse string) (*GoalJudgeResult, error) {
	g.mu.RLock()
	contract := g.goal.Contract
	subgoals := g.goal.Subgoals
	g.mu.RUnlock()
	if g.JudgeFn == nil {
		return nil, fmt.Errorf("goal: judge not configured")
	}
	return g.JudgeFn(goalText, lastResponse, contract, subgoals)
}
