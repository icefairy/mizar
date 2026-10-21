package agent

import "time"

// 消息角色（与 OpenAI 协议对齐，但工具结果用 Kind 区分——我们使用文本 JSON 调用协议，
// 不依赖原生 tool role；用 Kind 标记是为了压缩切点识别与缓存前缀管理）。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// 消息类型标记（Kind 字段）
const (
	KindNormal     = ""            // 普通 user/assistant 消息
	KindToolCall   = "tool_call"   // assistant 发出的工具调用 JSON
	KindToolResult = "tool_result" // 工具执行结果（role=user）
	KindSummary    = "summary"     // 压缩生成的会话摘要（role=user）
)

// Message 一条对话消息。
// Kind 用于压缩切点识别：KindToolResult 的消息绝不能作为切点被丢弃
// （它必须跟随其对应的 tool_call），KindSummary 是压缩产物。
//
// 工具交换的**权威表达**是原生字段：
//   - assistant 的调用：ToolCalls（含 ID/Name/Args），对应 OpenAI 的 tool_calls
//   - 工具结果：ToolCallID（指向被回答的调用），编码为 role:"tool" + tool_call_id
//
// 这是 pi agent 的做法（全链用原生协议），也是模型训练时见过的唯一形态。
// Content/ToolName/ToolArgs 仅用于人类可读展示与持久化的旧格式兼容。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Kind    string `json:"kind,omitempty"`
	// 可选：工具调用元数据（用于 tool_call 消息）
	ToolName string `json:"toolName,omitempty"`
	ToolArgs string `json:"toolArgs,omitempty"`

	// ToolCalls assistant 消息发起的工具调用（原生 tool_calls）。
	// 一轮多个调用就放多个元素。
	ToolCalls []ToolCallRef `json:"toolCalls,omitempty"`
	// ToolCallID 工具结果所属的调用 ID（KindToolResult 时用于配对）。
	ToolCallID string `json:"toolCallId,omitempty"`

	// Timestamp 消息创建时间（RFC3339，毫秒精度）。
	// 由 session.Store.Append 落盘时自动补写（未设置才填），
	// 因此内存中的 Initial 内容（会被回灌给 LLM）保持干净——这里刻意不做
	// json:"-"：它是一个持久化字段，只是不进 LLM 请求编码。
	Timestamp string `json:"timestamp,omitempty"`
}

// nowTimestamp 返回当前时间的 RFC3339 毫秒串（本地时区）。
func nowTimestamp() string { return time.Now().Format("2006-01-02T15:04:05.000-07:00") }

// EnsureTimestamp 未设置时间戳时补写当前时间，返回是否发生写入。
func (m *Message) EnsureTimestamp() bool {
	if m.Timestamp != "" {
		return false
	}
	m.Timestamp = nowTimestamp()
	return true
}

// ToolCallRef 一次工具调用的引用（对应 OpenAI tool_calls[] 的 id/name/arguments）。
type ToolCallRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
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
