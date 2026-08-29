package builtins

import (
	"fmt"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// toolExitPlanMode 返回 exit_plan_mode 工具（复刻 dsh plan-mode）。
//
// 行为：
//   - 计划模式未激活 → 返回错误提示
//   - 计划模式激活 → 调用 AskPlanReview 等待用户审批，返回审批结果
//   - 审批通过 → 退出计划模式，返回空字符串（模型继续执行）
//   - 继续规划 → 将反馈注入为 steer 消息，提示模型继续完善
func toolExitPlanMode() plugins.Tool {
	return plugins.Tool{
		Name:        "exit_plan_mode",
		Description: "Submit your completed plan for user review. Call this when you have a finished plan ready. The user will approve it (start executing) or ask you to keep planning with feedback.",
		Run: func(args string) (string, error) {
			if agent.CurrentPlanMode == nil || !agent.CurrentPlanMode.IsActive() {
				return "计划模式未激活。请先使用 /plan 命令进入计划模式。", nil
			}
			planMarkdown := strings.TrimSpace(args)
			if planMarkdown == "" {
				planMarkdown = "（无计划内容，请模型自行组织）"
			}
			review, err := agent.AskPlanReview(planMarkdown)
			if err != nil {
				return fmt.Errorf("plan review: %w", err).Error(), nil
			}
			switch review.Action {
			case "approve":
				agent.CurrentPlanMode.Exit()
				return "✅ 计划已批准。退出计划模式，开始执行。", nil
			case "keep_planning":
				feedback := strings.TrimSpace(review.Feedback)
				if feedback == "" {
					feedback = "请继续完善计划。"
				}
				// 将反馈作为 steer 注入下一轮循环
				if agent.CurrentPlanMode != nil {
					agent.CurrentPlanMode.SetPending(true) // 标记 pending，loop 消费后清除
				}
				return fmt.Sprintf("⚠️ 用户要求继续规划。反馈：%s", feedback), nil
			default:
				return "审批结果未知，继续规划。", nil
			}
		},
	}
}
