// Package builtins 实现 session_insights 工具。
package builtins

import (
	"fmt"
	"strings"

	"mizar/internal/plugins"
)

// InsightsData 统计数据来源接口。
type InsightsData interface {
	TotalPromptTokens() int
	TotalCompletionTokens() int
	TotalTokens() int
	CachedTokens() int
	EstCostUSD() float64
	RunCount() int
	AvgTurns() float64
	TopToolsText() []string // 简化：直接返回文本行
}

// InsightsTool 返回 session_insights 工具。
func InsightsTool(data InsightsData) plugins.Tool {
	return plugins.Tool{
		Name:        "session_insights",
		Description: "Show session statistics: token usage, cost estimate, tool usage patterns.",
		Run: func(args string) (string, error) {
			_ = args
			if data == nil {
				return "⚠ 统计未初始化", nil
			}
			var sb strings.Builder
			sb.WriteString("📊 Session Insights\n")
			sb.WriteString(fmt.Sprintf("  Total tokens:     %d\n", data.TotalTokens()))
			sb.WriteString(fmt.Sprintf("  Prompt tokens:    %d\n", data.TotalPromptTokens()))
			sb.WriteString(fmt.Sprintf("  Completion tokens:%d\n", data.TotalCompletionTokens()))
			if data.CachedTokens() > 0 {
				sb.WriteString(fmt.Sprintf("  Cached tokens:    %d\n", data.CachedTokens()))
			}
			sb.WriteString(fmt.Sprintf("  Est. cost:        $%.4f\n", data.EstCostUSD()))
			sb.WriteString(fmt.Sprintf("  Runs:             %d\n", data.RunCount()))
			sb.WriteString(fmt.Sprintf("  Avg turns/run:    %.1f\n", data.AvgTurns()))
			for _, line := range data.TopToolsText() {
				sb.WriteString("  " + line + "\n")
			}
			return sb.String(), nil
		},
	}
}
