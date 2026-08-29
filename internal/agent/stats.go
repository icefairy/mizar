package agent

import "fmt"

// Usage 模型 token 用量（从 LLM 响应提取，供 TUI 状态栏展示）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

// UsageTracker 可选接口：如果 LLM 实现此接口，Agent 会记录每次调用的用量。
type UsageTracker interface {
	LastUsage() *Usage
}

// StatsSession 会话级统计（由 Agent 维护，TUI 读取）
type StatsSession struct {
	CumulativePromptTokens     int
	CumulativeCompletionTokens int
	CumulativeTotalTokens      int
	CumulativeCachedTokens     int
	LastPromptTokens      int
	LastCompletionTokens  int
	LastTotalTokens       int
	LastCachedTokens      int
	LastResponseDuration  float64
	LastSpeedTokensPerSec float64
}

// CompactionInfo 压缩状态
type CompactionInfo struct {
	EstCurrentTokens int
	ContextWindow    int
	ReserveTokens    int
	CompactionLimit  int
	ProximityPct     float64
}

// ToolUsageEntry 工具使用条目（放在 agent 包避免循环 import）。
type ToolUsageEntry struct {
	Name  string
	Count int
}

// StatsDataRef 会话统计引用。
type StatsDataRef struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	EstCostUSD       float64
	RunCount         int
	AvgTurns         float64
	TopTools         []ToolUsageEntry
}

// ToInsightsData 转换为 insights 工具所需的接口。
func (s *StatsDataRef) ToInsightsData() insightsDataWrapper {
	return insightsDataWrapper{s}
}

type insightsDataWrapper struct{ s *StatsDataRef }

func (w insightsDataWrapper) TotalPromptTokens() int   { return w.s.PromptTokens }
func (w insightsDataWrapper) TotalCompletionTokens() int { return w.s.CompletionTokens }
func (w insightsDataWrapper) TotalTokens() int         { return w.s.TotalTokens }
func (w insightsDataWrapper) CachedTokens() int        { return w.s.CachedTokens }
func (w insightsDataWrapper) EstCostUSD() float64      { return w.s.EstCostUSD }
func (w insightsDataWrapper) RunCount() int            { return w.s.RunCount }
func (w insightsDataWrapper) AvgTurns() float64        { return w.s.AvgTurns }
func (w insightsDataWrapper) TopToolsText() []string {
	out := make([]string, len(w.s.TopTools))
	for i, t := range w.s.TopTools {
		out[i] = fmt.Sprintf("%s: %d calls", t.Name, t.Count)
	}
	return out
}
