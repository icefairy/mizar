// Package agent 实现 GoalLoop：每轮结束后自动 judge 目标进度并续跑。
//
// 复刻 hermes goals.py 的 Ralph loop 模式：
//   - 每轮 LLM 响应后，用辅助模型 judge 目标是否完成
//   - 未完成则追加 continuation prompt，继续下一轮
//   - 用户发消息则抢占并暂停 goal loop
//   - 支持 GoalContract（结构化契约）、QualityGate（门禁）、Subgoal（子目标）
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// ============================================================================
// GoalLoopConfig：goal loop 配置
// ============================================================================

// GoalLoopConfig goal loop 运行参数。
type GoalLoopConfig struct {
	// MaxTurns 最大续跑轮数（0=使用 goal.MaxGoalRounds，-1=无限制）。
	MaxTurns int
	// JudgeTimeout 单次 judge 调用超时（秒，0=30s）。
	JudgeTimeout int
	// MaxConsecutiveParseFailures judge 解析连续失败自动暂停阈值。
	MaxConsecutiveParseFailures int
	// MaxConsecutiveTransportFailures judge API 连续失败自动暂停阈值。
	MaxConsecutiveTransportFailures int
	// ContinuationPrompt 续跑提示模板（可选，默认内置）。
	ContinuationPrompt string
}

// DefaultGoalLoopConfig 返回默认配置。
func DefaultGoalLoopConfig() GoalLoopConfig {
	return GoalLoopConfig{
		MaxTurns:                        0,
		JudgeTimeout:                    30,
		MaxConsecutiveParseFailures:     3,
		MaxConsecutiveTransportFailures: 5,
		ContinuationPrompt:              "",
	}
}

// ============================================================================
// GoalLoop：自动 judge 循环
// ============================================================================

// GoalLoop 管理 goal 的自动推进循环。
type GoalLoop struct {
	gs      *GoalService
	cfg     GoalLoopConfig
	running atomic.Bool
	active  atomic.Bool // 当前是否有 goal loop 在运行
	// 每轮消耗计数
	turnsUsed int
	// 连续失败计数
	parseFailures    int
	transportFailures int
}

// NewGoalLoop 创建 goal loop 控制器。
func NewGoalLoop(gs *GoalService, cfg GoalLoopConfig) *GoalLoop {
	if cfg.MaxConsecutiveParseFailures <= 0 {
		cfg.MaxConsecutiveParseFailures = 3
	}
	if cfg.MaxConsecutiveTransportFailures <= 0 {
		cfg.MaxConsecutiveTransportFailures = 5
	}
	if cfg.JudgeTimeout <= 0 {
		cfg.JudgeTimeout = 30
	}
	return &GoalLoop{gs: gs, cfg: cfg}
}

// IsRunning 返回是否有 goal loop 正在运行。
func (gl *GoalLoop) IsRunning() bool {
	return gl.active.Load()
}

// EvaluateAfterTurn 每轮 LLM 响应后调用，决定是否继续 goal loop。
// 返回 (shouldContinue bool, nextPrompt string, err error)。
// nextPrompt 非空时调用方应将其作为 user message 追加到消息列表。
func (gl *GoalLoop) EvaluateAfterTurn(lastResponse string) (bool, string, error) {
	g := gl.gs.Get()
	if g == nil || g.Phase != GoalInProgress {
		gl.active.Store(false)
		return false, "", nil
	}
	if !gl.running.Load() {
		// 首次调用：标记为 active
		gl.active.Store(true)
	}

	// 检查质量门禁
	cwd := "" // TODO: 从外部注入 cwd
	allPassed, failedIdx, output := gl.gs.EvaluateGates(cwd)
	if !allPassed && failedIdx >= 0 {
		gate := g.Gates[failedIdx]
		// 门禁失败：直接返回 continuation prompt，不经过 judge
		gl.active.Store(false)
		prompt := fmt.Sprintf(
			"[Continuing toward your standing goal — quality gate failed]\n"+
				"Goal: %s\n\n"+
				"The quality gate command below must pass before this goal can be declared done:\n"+
				"  $ %s\n"+
				"Exit code: %d\n"+
				"Output (tail):\n```\n%s\n```\n\n"+
				"Fix the underlying problem so this gate passes, then re-run it to confirm. "+
				"Do not declare the goal complete while any gate fails.",
			g.Objective, gate.Command, -1, output)
		return false, prompt, nil
	}

	// 调用 judge
	result, err := gl.gs.JudgeGoal(g.Objective, lastResponse)
	if err != nil {
		gl.transportFailures++
		if gl.transportFailures >= gl.cfg.MaxConsecutiveTransportFailures {
			gl.active.Store(false)
			return false, "", fmt.Errorf("goal judge transport failures exceeded (%d)", gl.cfg.MaxConsecutiveTransportFailures)
		}
		// 临时错误，不影响 loop
		return false, "", nil
	}
	gl.transportFailures = 0

	// 解析 judge 结果
	if result == nil || result.Verdict == "" {
		gl.parseFailures++
		if gl.parseFailures >= gl.cfg.MaxConsecutiveParseFailures {
			gl.active.Store(false)
			return false, "", fmt.Errorf("goal judge parse failures exceeded")
		}
		return false, "", nil
	}
	gl.parseFailures = 0

	switch result.Verdict {
	case "done", "DONE":
		gl.active.Store(false)
		return false, "", nil

	case "wait":
		// 等待异步工作（CI、构建等）
		if result.WaitSecs != nil && *result.WaitSecs > 0 {
			return false, "", fmt.Errorf("goal: waiting %d seconds for async work", *result.WaitSecs)
		}
		if result.WaitPID != nil && *result.WaitPID > 0 {
			return false, "", fmt.Errorf("goal: waiting for pid %d", *result.WaitPID)
		}
		fallthrough

	case "continue", "CONTINUE", "":
		// 继续推进
		gl.turnsUsed++
		maxTurns := gl.cfg.MaxTurns
		if maxTurns <= 0 && g.MaxGoalRounds > 0 {
			maxTurns = g.MaxGoalRounds
		}
		if maxTurns > 0 && gl.turnsUsed >= maxTurns {
			gl.active.Store(false)
			return false, "", fmt.Errorf("goal: turn budget exhausted (%d/%d)", gl.turnsUsed, maxTurns)
		}

		// 构建 continuation prompt
		prompt := gl.buildContinuationPrompt(g)
		return true, prompt, nil

	default:
		return false, "", fmt.Errorf("goal: unknown judge verdict %q", result.Verdict)
	}
}

// buildContinuationPrompt 构建续跑提示。
func (gl *GoalLoop) buildContinuationPrompt(g *Goal) string {
	var sb strings.Builder
	sb.WriteString("[Continuing toward your standing goal]\n")
	sb.WriteString(fmt.Sprintf("Goal: %s\n\n", g.Objective))

	// 注入完成契约
	if g.Contract != nil && !g.Contract.IsEmpty() {
		sb.WriteString("Completion contract:\n")
		sb.WriteString(g.Contract.RenderBlock())
		sb.WriteString("\n")
	}

	// 注入子目标
	if len(g.Subgoals) > 0 {
		sb.WriteString("Additional criteria the user added mid-loop:\n")
		for i, sg := range g.Subgoals {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, sg))
		}
		sb.WriteString("\n")
	}

	if gl.cfg.ContinuationPrompt != "" {
		sb.WriteString(gl.cfg.ContinuationPrompt)
	} else {
		sb.WriteString("Continue working toward this goal. Take the next concrete step.\n")
		sb.WriteString("If you believe the goal is complete, state so explicitly and stop.\n")
		sb.WriteString("If you are blocked and need input from the user, say so clearly and stop.")
	}
	return sb.String()
}

// Pause 暂停 goal loop。
func (gl *GoalLoop) Pause() {
	gl.active.Store(false)
	gl.running.Store(false)
	gl.turnsUsed = 0
}

// Resume 恢复 goal loop。
func (gl *GoalLoop) Resume() {
	gl.running.Store(true)
}

// Reset 重置 loop 状态（新任务开始时调用）。
func (gl *GoalLoop) Reset() {
	gl.active.Store(false)
	gl.running.Store(false)
	gl.turnsUsed = 0
	gl.parseFailures = 0
	gl.transportFailures = 0
}

// Stats 返回 loop 当前统计。
func (gl *GoalLoop) Stats() string {
	g := gl.gs.Get()
	if g == nil {
		return "无活跃 goal"
	}
	return fmt.Sprintf(
		"goal=%q phase=%s turns_used=%d parse_fails=%d transport_fails=%d gates=%d subgoals=%d",
		g.Objective, g.Phase, gl.turnsUsed,
		gl.parseFailures, gl.transportFailures,
		len(g.Gates), len(g.Subgoals),
	)
}

// ============================================================================
// JudgePrompt 模板（复刻 hermes JUDGE_SYSTEM_PROMPT）
// ============================================================================

// BuildJudgeSystemPrompt 构建 judge 的系统提示词。
func BuildJudgeSystemPrompt() string {
	return `You are a strict judge evaluating whether an autonomous agent has achieved a user's stated goal.
You receive the goal text, the agent's most recent response, and optionally a completion contract.
Decide one of three verdicts:

DONE — the goal is fully satisfied:
- The response explicitly confirms the goal was completed, OR
- The response clearly shows the final deliverable was produced, OR
- The response explains the goal is unachievable / blocked / needs user input.

WAIT — the goal is NOT done, but the next step is to wait for async work:
- A background process is still running AND the response shows the agent is waiting on its result.
- The agent says it is rate-limited / backing off / must wait a fixed period.

CONTINUE — not done, and there is a concrete next step the agent can take right now.
This is the default when in doubt.

Reply ONLY with a single JSON object on one line:
{"verdict": "done", "reason": "<one sentence>"}
{"verdict": "continue", "reason": "<one sentence>"}
{"verdict": "wait", "wait_for_seconds": <int>, "reason": "<one sentence>"}
{"verdict": "wait", "wait_on_pid": <int>, "reason": "<one sentence>"}

If a completion contract is provided, evaluate DONE strictly against the Verification criterion.
Require concrete evidence (command output, file contents, test result) — not claims like "done".`
}

// BuildJudgeUserPrompt 构建 judge 的用户提示词。
func BuildJudgeUserPrompt(goalText, lastResponse string, contract *GoalContract, subgoals []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Goal:\n%s\n\n", goalText))
	if contract != nil && !contract.IsEmpty() {
		sb.WriteString("Completion contract (authoritative definition of done):\n")
		sb.WriteString(contract.RenderBlock())
		sb.WriteString("\n\n")
	}
	if len(subgoals) > 0 {
		sb.WriteString("Additional criteria (ALL must be satisfied):\n")
		for i, sg := range subgoals {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, sg))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("Agent's most recent response:\n%s\n\n", lastResponse))
	sb.WriteString("Decision: Is the goal satisfied — done, continue, or wait?")
	return sb.String()
}

// ParseJudgeResult 解析 judge 的 JSON 输出。
func ParseJudgeResult(text string) (*GoalJudgeResult, error) {
	text = strings.TrimSpace(text)
	// 找第一个 { 到最后一个 }
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found in judge output")
	}
	jsonStr := text[start : end+1]
	var result GoalJudgeResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse judge JSON: %w", err)
	}
	// 归一化 verdict
	result.Verdict = strings.ToLower(result.Verdict)
	switch result.Verdict {
	case "done", "true":
		result.Verdict = "done"
	case "continue", "false", "":
		result.Verdict = "continue"
	case "wait":
		// 保留
	default:
		result.Verdict = "continue"
	}
	return &result, nil
}

// ============================================================================
// AuxiliaryJudgeFn：默认 judge 实现（复用主 LLM）
// ============================================================================

// AuxiliaryLLM 辅助模型接口（judge 调用用）。
type AuxiliaryLLM interface {
	Chat(messages []Message) (string, error)
}

// BuildDefaultJudgeFn 构建默认 judge 函数，使用传入的 AuxiliaryLLM。
// 如果 auxLLM 为 nil，返回 nil（judge 不启用）。
func BuildDefaultJudgeFn(auxLLM AuxiliaryLLM) func(string, string, *GoalContract, []string) (*GoalJudgeResult, error) {
	if auxLLM == nil {
		return nil
	}
	return func(goalText, lastResponse string, contract *GoalContract, subgoals []string) (*GoalJudgeResult, error) {
		messages := []Message{
			{Role: RoleSystem, Content: BuildJudgeSystemPrompt()},
			{Role: RoleUser, Content: BuildJudgeUserPrompt(goalText, lastResponse, contract, subgoals)},
		}
		reply, err := auxLLM.Chat(messages)
		if err != nil {
			return nil, fmt.Errorf("judge call: %w", err)
		}
		return ParseJudgeResult(reply)
	}
}
