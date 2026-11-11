// Package agent 实现计划模式（复刻 deepseek-harness plan-mode）。
//
// 计划模式：Agent 进入后，模型先探索设计并通过 exit_plan_mode 提交计划，
// 用户审批后退出计划模式并继续执行。
//
// 使用方式：
//   - /plan [guidance] — 进入计划模式（可选附带指令）
//   - /plan off — 直接退出计划模式
//   - exit_plan_mode 工具 — 提交计划等待用户审批
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// PlanMode 计划模式状态。
type PlanMode struct {
	mu       sync.RWMutex
	active   bool
	pending  bool   // 请求进入但尚未生效（当前有 turn 在执行）
	guidance string // 规划阶段的系统提示词片段
	// ver 每次 Enter/Exit 自增：system prompt 带缓存，模式变化必须让它失效，
	// 否则退出计划模式后模型仍按「先出方案」的老提示运行。
	ver atomic.Uint64
}

// NewPlanMode 创建空的计划模式控制器。
func NewPlanMode() *PlanMode {
	return &PlanMode{}
}

// Enter 请求进入计划模式（立即生效或 pending）。
// guidance 为空时使用默认引导语。
func (p *PlanMode) Enter(guidance string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if guidance == "" {
		guidance = defaultPlanGuidance
	}
	p.guidance = guidance
	p.active = true
	p.pending = false
	p.ver.Add(1)
}

// Exit 直接退出计划模式。
func (p *PlanMode) Exit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = false
	p.pending = false
	p.ver.Add(1)
}

// Version 返回模式状态版本号（供 system prompt 缓存失效判断）。
func (p *PlanMode) Version() uint64 {
	if p == nil {
		return 0
	}
	return p.ver.Load()
}

// IsActive 当前是否处于活跃计划模式。
func (p *PlanMode) IsActive() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.active && !p.pending
}

// IsPending 是否有待处理的进入请求（用于 loop pre-step 检查）。
func (p *PlanMode) IsPending() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pending
}

// SetPending 在 turn 内标记 pending（loop 在 pre-step 消费）。
func (p *PlanMode) SetPending(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = v
}

// Guidance 返回当前引导语。
func (p *PlanMode) Guidance() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.guidance
}

// SystemSection 返回注入 system prompt 的文本块（空字符串表示未激活）。
func (p *PlanMode) SystemSection() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.active {
		return ""
	}
	return "\n\n## 计划模式（Plan Mode）\n" + p.guidance +
		"\n当你完成计划时，调用 exit_plan_mode 工具提交计划供用户审批。" +
		"\n（不需要计划时，用户可输入 /plan off 或 /plan exit 直接退出，不必等模型调用工具。）"
}

const defaultPlanGuidance = "你正处于计划模式。请先探索和设计方案，通过 exit_plan_mode 提交完整的执行计划，等待用户审批后再开始执行。不要急于调用工具——先理解任务、列出关键决策点、评估风险，然后给出结构化的计划。"

// IsModeExitArg 判断模式命令的参数是否表示「退出」。
//
// 用户对「怎么退出」的直觉写法不统一，实测会输入 exit / off / stop / quit / 退出 / 关闭
// 等等；统一在这里收口，避免每个模式命令各写一套（漏掉一种就变成「又进入了」）。
func IsModeExitArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "exit", "off", "stop", "quit", "end", "close",
		"退出", "关闭", "结束", "取消":
		return true
	}
	return false
}

// IsModeEnterArg 判断参数是否是无实义的「进入」词（不当作自定义指引）。
func IsModeEnterArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "on", "enter", "start", "进入", "开启":
		return true
	}
	return false
}

// PlanReview 用户审批 exit_plan_mode 的结果。
type PlanReview struct {
	Action   string // "approve" | "keep_planning"
	Feedback string // keep_planning 时的额外反馈
}

// AskPlanReview 通过 ask_user_question 工具触发用户审批。
// 返回 nil 表示用户选择了 approve；返回 error 表示超时或其他错误。
// feedback 非空时表示 keep_planning，内容为反馈。
func AskPlanReview(planMarkdown string) (*PlanReview, error) {
	// 降级路径：无交互接口时直接返回 approve（计划模式在 CLI 模式等价于静默批准）
	if agentAskPlanUser == nil {
		return &PlanReview{Action: "approve"}, nil
	}
	questions := fmt.Sprintf(`[{
		"id": "review",
		"question": "以下计划请审批：\n\n%s\n\n请选择：",
		"header": "审批计划",
		"options": [
			{"label": "Approve（批准并开始执行）", "description": "按此计划执行，退出计划模式"},
			{"label": "Keep planning（继续规划）", "description": "提供反馈让模型继续完善计划"}
		],
		"multi_select": false
	}]`, truncateString(planMarkdown, 2000))
	answers, err := agentAskPlanUser(questions)
	if err != nil {
		return nil, fmt.Errorf("ask plan review: %w", err)
	}
	// 解析答案（格式与 ask_user_question 一致）
	var ans []struct {
		ID     string   `json:"id"`
		Value  string   `json:"value"`
		Values []string `json:"values,omitempty"`
	}
	if err := jsonUnmarshal(answers, &ans); err != nil {
		return nil, fmt.Errorf("parse plan review answer: %w", err)
	}
	for _, a := range ans {
		if a.ID == "review" {
			switch {
			case strings.Contains(a.Value, "Approve") || strings.Contains(a.Value, "approve"):
				return &PlanReview{Action: "approve"}, nil
			case strings.Contains(a.Value, "Keep") || strings.Contains(a.Value, "keep"):
				return &PlanReview{Action: "keep_planning", Feedback: a.Value}, nil
			default:
				return &PlanReview{Action: "keep_planning", Feedback: a.Value}, nil
			}
		}
	}
	return &PlanReview{Action: "approve"}, nil
}

// agentAskPlanUser 全局回调：由 TUI 设置，弹出计划审批界面。
var agentAskPlanUser func(questions string) (string, error)

// CurrentPlanMode 全局可访问的计划模式实例（由 main.go 在 Agent 创建后设置）。
// builtins 包通过此变量访问当前活跃的 PlanMode，避免 import cycle。
var CurrentPlanMode *PlanMode

// SetPlanAskUser 设置计划审批的用户交互回调。
func SetPlanAskUser(fn func(string) (string, error)) {
	agentAskPlanUser = fn
}

// jsonUnmarshal 解析用户审批答案。早前这里是个永远返回错误的占位实现，
// 导致「用户选了 Approve 却总被当成解析失败」——改动只需委托标准库。
func jsonUnmarshal(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("解析审批答案失败: %w (原始内容: %.120s)", err, s)
	}
	return nil
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
