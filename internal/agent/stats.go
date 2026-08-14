package agent

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
	// 累计 token
	CumulativePromptTokens    int
	CumulativeCompletionTokens int
	CumulativeTotalTokens     int
	CumulativeCachedTokens    int
	// 最近一次响应
	LastPromptTokens     int
	LastCompletionTokens int
	LastTotalTokens      int
	LastCachedTokens     int
	LastResponseDuration float64 // 秒
	LastSpeedTokensPerSec float64
}

// CompactionInfo 压缩状态
type CompactionInfo struct {
	EstCurrentTokens int
	ContextWindow    int
	ReserveTokens    int
	CompactionLimit  int // ContextWindow - ReserveTokens
	ProximityPct     float64 // 当前 / 压缩阈值
}