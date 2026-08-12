package agent

// 消息角色（与 OpenAI 协议对齐，但工具结果用 Kind 区分——我们使用文本 JSON 调用协议，
// 不依赖原生 tool role；用 Kind 标记是为了压缩切点识别与缓存前缀管理）。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// 消息类型标记（Kind 字段）
const (
	KindNormal    = ""        // 普通 user/assistant 消息
	KindToolCall  = "tool_call"   // assistant 发出的工具调用 JSON
	KindToolResult = "tool_result" // 工具执行结果（role=user）
	KindSummary   = "summary"  // 压缩生成的会话摘要（role=user）
)

// Message 一条对话消息。
// Kind 用于压缩切点识别：KindToolResult 的消息绝不能作为切点被丢弃
// （它必须跟随其对应的 tool_call），KindSummary 是压缩产物。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Kind    string `json:"kind,omitempty"`
	// 可选：工具调用元数据（用于 tool_call 消息）
	ToolName string `json:"toolName,omitempty"`
	ToolArgs string `json:"toolArgs,omitempty"`
}

// IsCutPoint 判断该消息是否可作为压缩切点。
// 规则（参照 pi compaction）：
//   - 工具结果不能切（必须跟随 tool_call）
//   - 摘要消息不是切点（它是压缩产物本身）
//   - 只有 user/assistant 的普通消息可切
func (m Message) IsCutPoint() bool {
	switch m.Kind {
	case KindToolResult, KindSummary:
		return false
	}
	return m.Role == RoleUser || m.Role == RoleAssistant
}
