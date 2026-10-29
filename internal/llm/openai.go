// Package llm 提供 OpenAI 兼容的 LLM 客户端（可对接璇玑网关/任意兼容端点）。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"time"

	"mizar/internal/agent"
	"mizar/internal/diag"
	"mizar/internal/plugins"
)

// OpenAI 是 OpenAI 兼容客户端。
// 思考等级（ThinkingLevel）：
//
//	"auto"   — 不传 thinking 参数，由模型决定
//	"off"    — thinking: {type: "disabled"}
//	"low"    — thinking: {type: "enabled"}, reasoning_effort: "low"
//	"medium" — thinking: {type: "enabled"}, reasoning_effort: "medium"
//	"high"   — thinking: {type: "enabled"}, reasoning_effort: "high"
type OpenAI struct {
	BaseURL string // 如 http://127.0.0.1:3002/v1
	APIKey  string
	Model   string
	Client  *http.Client
	// 思考等级（auto/off/low/medium/high），空串等价 auto
	ThinkingLevel string
	// Params 运行时对话参数（temperature/top_p/max_tokens，Web UI 设置用）。
	// nil = 不传，保持供应商默认。进程级生效，影响 Chat / ChatWithTools 全部路径。
	Params *ChatParams
	// userAgent 自定义 User-Agent（空=不覆盖，用 Go 默认）。经 SetUserAgent 设置。
	userAgent string
	// 最后一次响应的 token 用量（只读，供 TUI 状态栏展示）
	lastUsage *agent.Usage
}

// ChatParams 运行时对话参数（可选覆盖）。
type ChatParams struct {
	Temperature *float64 `json:"temperature,omitempty"` // 温度 0-2
	TopP        *float64 `json:"top_p,omitempty"`       // 核采样 0-1
	MaxTokens   int      `json:"max_tokens,omitempty"`  // 最大输出 token（0=不传）
}

// SetParams 设置运行时对话参数（nil 清除）。
func (o *OpenAI) SetParams(p *ChatParams) {
	o.Params = p
}

// SetUserAgent 设置自定义 User-Agent（空串 = 恢复 Go 默认）。
func (o *OpenAI) SetUserAgent(ua string) {
	o.userAgent = strings.TrimSpace(ua)
}

// UserAgent 返回当前自定义 User-Agent（空=默认）。
func (o *OpenAI) UserAgent() string {
	return o.userAgent
}

// applyUA 把自定义 User-Agent 写入请求（未设置则不动，保留 Go 默认）。
func (o *OpenAI) applyUA(req *http.Request) {
	if o.userAgent != "" {
		req.Header.Set("User-Agent", o.userAgent)
	}
}

// applyParams 把运行时参数应用到请求（nil 字段不覆盖，保持默认）。
func (o *OpenAI) applyParams(r *chatReq) {
	if o.Params == nil {
		return
	}
	if o.Params.Temperature != nil {
		r.Temperature = o.Params.Temperature
	}
	if o.Params.TopP != nil {
		r.TopP = o.Params.TopP
	}
	if o.Params.MaxTokens > 0 {
		r.MaxTokens = o.Params.MaxTokens
	}
}

// NewOpenAI 创建客户端。
func NewOpenAI(baseURL, apiKey, model string) *OpenAI {
	return &OpenAI{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// ============================================================================
// 原生工具调用（OpenAI function calling）
// ============================================================================

// toolDef OpenAI API 工具定义
type toolDef struct {
	Type     string  `json:"type"`
	Function funcDef `json:"function"`
}

type funcDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// toolCall OpenAI API 响应中的工具调用
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串，如 {"command":"hostname"}
	} `json:"function"`
}

// chatMsg 对话消息。Content 为 string（纯文本）或 []chatContentPart（多模态）。
//
// 工具交换走**原生字段**（对齐 pi agent 与 OpenAI 协议）：
//   - assistant 发起调用 → ToolCalls（tool_calls 数组，含 id/function.name/arguments）
//   - 工具结果 → Role="tool" + ToolCallID（配对的 tool_call_id）
//
// 这层编码见 encodeMessages。
type chatMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wireToolCall OpenAI 原生 tool_calls 元素。
type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // 固定 "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串
	} `json:"function"`
}

// chatContentPart 多模态内容分片（OpenAI 兼容 content 数组）。
type chatContentPart struct {
	Type     string            `json:"type"` // text | image_url | video_url
	Text     string            `json:"text,omitempty"`
	ImageURL *chatPartImageURL `json:"image_url,omitempty"`
	VideoURL *chatPartVideoURL `json:"video_url,omitempty"`
}

type chatPartImageURL struct {
	URL string `json:"url"`
}

type chatPartVideoURL struct {
	URL string `json:"url"`
}

type chatReq struct {
	Model         string         `json:"model"`
	Messages      []chatMsg      `json:"messages"`
	Tools         []toolDef      `json:"tools,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	Thinking      *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// streamOptions OpenAI 流式扩展参数。
// IncludeUsage=true 时服务端会在流末尾额外下发一个带 usage 的 chunk
// （OpenAI 标准：usage chunk 无 choices，紧随 [DONE] 之前），
// 使 TUI/Server 能在流内拿到精确 tokens/缓存用量，无需等整次请求结束。
// 部分兼容网关/旧版端点不接受该字段（400），streamCoreCtx 会降级重试。
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatResp struct {
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Usage *agent.Usage `json:"usage,omitempty"`
}

// pluginsToolToDef 将 plugins.Tool 转为 OpenAI tools 定义。
// 参数 schema 优先从 Tool.Description 解析（ArgsSchema），
// 解析失败时兜底 additionalProperties: true。
func pluginsToolToDef(t plugins.Tool) toolDef {
	params := t.ArgsSchema()
	if params == nil {
		params = map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
	}
	return toolDef{
		Type: "function",
		Function: funcDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}
}

// reasonMaxLen reason 字段的上限（字符）。超出时截断：
// reason 会进入协议 JSON 与会话历史，模型写一大段分析时既浪费 token、
// 又让弱模型更容易在这个转义字符串里写畸形 JSON。
const reasonMaxLen = 500

// callSpec 控制协议里的单个工具调用（文本协议形态）。
// Args 是**原始参数字符串**（如 `{"command":"ls"}`），不是解析后的对象——
// agent 循环把它原样交给工具实现自行 Unmarshal，与单数形态的历史行为一致。
// ID 为原生 tool_call id（有则透传，用于回填 tool_call_id；无则由循环合成）。
type callSpec struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	ID   string `json:"id,omitempty"`
}

// callRequestWire 控制协议 JSON 的传输形态。
//
// calls 是权威的多调用列表（按顺序执行）；tool/args 冗余携带首个调用，
// 让只认旧单数字段的读取方（如 subagent）继续工作。
type callRequestWire struct {
	Action string     `json:"action"`
	Calls  []callSpec `json:"calls,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Args   string     `json:"args,omitempty"`
	Reason string     `json:"reason,omitempty"`
	CallID string     `json:"callId,omitempty"`
}

// toolCallsToText 将原生 tool_calls 转为 agent 循环可解析的 JSON 文本。
// 新 schema 中 arguments 直接是原始参数（如 {"command":"hostname"}），
// 用 json.Marshal 转成转义后的 JSON 字符串填入 calls[].args。
//
// 多调用：模型一轮可以合法地并行发多个工具调用（原生 tool_calls 本就是数组）。
// 这里全部输出到 calls 数组，由 agent 循环**按顺序执行**；同时把首个同时填入
// 单数字段（tool/args），让只认旧字段的读取方（如 subagent 的解析）仍能工作。
//
// reason 为模型同一轮的 content（不是 tools schema 里的参数），非空则并入
// 顶层 reason 字段，使 agent 循环能把它存入历史并在 UI 展示。
func toolCallsToText(tcs []toolCall, reason string) string {
	if len(tcs) == 0 {
		return ""
	}
	calls := make([]callSpec, 0, len(tcs))
	for _, tc := range tcs {
		calls = append(calls, callSpec{Tool: tc.Function.Name, Args: tc.Function.Arguments, ID: tc.ID})
	}
	out := callRequestWire{Action: "tool", Calls: calls, Tool: calls[0].Tool, Args: calls[0].Args, CallID: calls[0].ID}
	if r := strings.TrimSpace(reason); r != "" {
		// truncate 按 rune 截断（不会切断多字节汉字）并自带省略号
		out.Reason = truncate(r, reasonMaxLen)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// setThinking 根据 ThinkingLevel 设置思考参数
func (c *OpenAI) setThinking(req *chatReq) {
	c.setThinkingLevel(req, c.ThinkingLevel)
}

// setThinkingLevel 用指定等级设置思考参数（插件 ai_chat 用）。
// level 取值: auto/off/low/medium/high。auto 和空串不传 thinking 参数。
func (c *OpenAI) setThinkingLevel(req *chatReq, level string) {
	if send, typ := c.hasThinking(level); send {
		req.Thinking = &struct {
			Type string `json:"type"`
		}{Type: typ}
		if effort := c.reasoningEffort(level); effort != "" {
			req.ReasoningEffort = effort
		}
	}
}

// hasThinking 判断某等级是否发送 thinking 参数并返回 type。
func (c *OpenAI) hasThinking(level string) (bool, string) {
	switch level {
	case "", thinkingAuto:
		return false, ""
	case thinkingOff:
		return true, "disabled"
	case thinkingLow, thinkingMedium, thinkingHigh:
		return true, "enabled"
	}
	return false, ""
}

// reasoningEffort 返回某等级对应的 reasoning_effort。
func (c *OpenAI) reasoningEffort(level string) string {
	switch level {
	case thinkingLow:
		return "low"
	case thinkingMedium:
		return "medium"
	case thinkingHigh:
		return "high"
	}
	return ""
}

// ============================================================================
// 核心方法
// ============================================================================

// ChatWithTools 实现 agent.ToolCallLLM 接口：发送消息 + 原生工具定义。
// 若模型返回原生 tool_calls，自动转为 agent 循环可解析的 JSON 文本；
// 否则返回普通文本回复。
// 消息在请求中保持文本格式（不改历史格式），仅额外发送 tools 参数。
func (c *OpenAI) ChatWithTools(messages []agent.Message, tools []plugins.Tool) (string, error) {
	req := chatReq{Model: c.Model}
	req.Messages = encodeMessages(messages)
	c.setThinking(&req)

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequest("POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.applyUA(httpReq)
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices")
	}
	c.lastUsage = out.Usage

	msg := out.Choices[0].Message
	fr := out.Choices[0].FinishReason
	// 优先处理原生 tool_calls：转为 agent 循环可解析的 JSON 文本（附带模型说明文字作为 reason）
	if len(msg.ToolCalls) > 0 {
		return toolCallsToText(msg.ToolCalls, msg.Content), nil
	}
	// 统一出口：无论 finish_reason 是 stop / length / content_filter / 缺失，
	// 只要 agent 循环路径（本方法就是）拿到非空内容，就必须给出可被 parseCallJSON
	// 消化的输出（详情见 wrapReplyJSON）。原先只在 fr=="stop" 时包装，其余直接返回
	// 裸文本 → 循环解析失败白耗一轮。
	if fr != "stop" && strings.TrimSpace(msg.Content) != "" {
		diag.Logf("llm", "非流式 finish_reason=%q 非 stop，内容已按协议归一化", fr)
	}
	if wrapped := wrapReplyJSON(msg.Content); wrapped != "" {
		return wrapped, nil
	}
	return msg.Content, nil
}

// wrapReplyJSON 把模型的原始输出转为 agent 循环可解析的控制协议 JSON。
//
// 这是【唯一】的不变式：只要走 agent 循环路径（调用方传 wrapReply=true），
// 返回的就必须是 parseCallJSON 能消化的东西 —— XML 或含 action 字段的 JSON；
// 否则循环会判「无法识别的控制指令」→ 注入纠正提示白耗一轮（弱模型/输出被截断时
// 常见，表现为任务莫名停顿）。finish_reason 只是诊断信息（stop / length /
// content_filter / 缺失），不参与该判断。
//
// 三种情形，按优先级：
//  1. 模型已输出可提取的控制 JSON（含“解释文本 + tool JSON”混合形态）→ 原样透传；
//     tool JSON 必须透传，否则包成 reply 会让工具不执行、JSON 原样显示给用户。
//  2. 识别为【畸形】工具调用 JSON（缺 tool 字段、内层引号未转义等）→ 也原样返回，
//     让 parseCallJSON 走错误处理路径注入针对性纠正提示，比包成 reply 更能帮助自纠。
//  3. 其余非空内容 → 包装为 reply。
//
// 内容为空时返回空串，由调用方决定后续（保持各自原有的返回语义）。
func wrapReplyJSON(content string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}
	if extracted, ok := agent.ExtractActionJSON(s); ok {
		return extracted
	}
	if agent.IsToolCallText(s) {
		return s
	}
	return fmt.Sprintf(`{"action":"reply","text":"%s"}`, escapeJSONString(content))
}

// encodeMessages 把 agent.Message 列表编码为 OpenAI 兼容的 chatMsg 列表。
//
// 这是修复「模型退化为纯文本」的关键：旧实现把所有消息一律压成
// chatMsg{Role: m.Role, Content: m.Content}，于是工具调用变成 assistant 的
// 文本 {"action":"tool",...}、工具结果变成 user 的文本 "工具结果: {...}"。
// 模型看到的历史是「有人一直在念 JSON」，它会**模仿这种模式**——用文字描述
// 而不是真的发 tool_call（实测：长会话后 finish="stop" toolCalls=0、
// 内容里写 <tool name=...>）。
//
// pi agent 的做法是全程用原生字段（convertToLlm 原样透传 toolResult，
// provider 层再转成 role:"tool" + tool_call_id），本函数与之对齐：
//   - assistant + KindToolCall → content(前导文本) + tool_calls[]
//   - KindToolResult            → role:"tool" + tool_call_id + 结果正文
func encodeMessages(msgs []agent.Message) []chatMsg {
	out := make([]chatMsg, 0, len(msgs))
	// OpenAI 严格要求 role:"tool" 必须回应前面的 tool_calls，且一个 tool_call 只能被
	// 回应一次；反向地，发出了 tool_calls 就必须有结果，否则报 400
	// 「tool calls and tool results do not match」（2026-10-16 实测 codebuddy）。
	// 因此两个方向都要对齐，用同一套配对规则（见 pairCalls）：
	//   - 孤儿结果（历史被截断 / 旧会话缺 ID）→ 降级为普通 user 文本（保留信息）
	//   - 孤儿调用（结果从未落盘 / 尾部被截断）→ 不声明（见 encodeToolCallMsg）
	paired := pairedCallIDs(msgs)
	// open 为未配对调用的 FIFO 队列：旧格式（tool_result 无 ToolCallID）按顺序回退配对。
	var open []string
	for _, m := range msgs {
		switch m.Kind {
		case agent.KindToolCall:
			encoded, ok := encodeToolCallMsg(m, paired)
			if !ok {
				continue // 所有调用都无结果回应：丢弃（避免发出孤儿 tool_calls）
			}
			for _, tc := range encoded.ToolCalls {
				open = append(open, tc.ID)
			}
			out = append(out, encoded)
		case agent.KindToolResult:
			if id, ok := takePaired(&open, m.ToolCallID); ok {
				out = append(out, chatMsg{
					Role:       "tool",
					Content:    toolResultBody(m.Content),
					ToolCallID: id,
				})
				continue
			}
			// 孤儿结果：降级为普通 user 消息（保留信息，避免 API 拒绝）
			out = append(out, chatMsg{Role: agent.RoleUser, Content: toolResultBody(m.Content)})
		default:
			out = append(out, chatMsg{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

// takePaired 从待配对队列中取出被回应的 call id。
// want 非空时精确匹配（新格式）；want 为空时取队首（旧格式 tool_result 缺 ToolCallID，
// 按出现顺序回退配对——工具交换在历史里总是成对按序出现的）。
// 返回 ok=false 表示无对应调用（孤儿结果，调用方降级为普通文本）。
func takePaired(open *[]string, want string) (string, bool) {
	q := *open
	if len(q) == 0 {
		return "", false
	}
	if want == "" {
		*open = q[1:]
		return q[0], true
	}
	for i, id := range q {
		if id == want {
			*open = append(q[:i:i], q[i+1:]...)
			return id, true
		}
	}
	return "", false
}

// pairedCallIDs 预扫描消息列表，返回「会收到工具结果回应」的 tool_call id 集合。
//
// 语义与 encodeMessages 的配对逻辑完全一致（同一 id 只允许被回应一次，按出现顺序
// 配对，旧格式缺 ID 的结果按队首回退），因此可用它决定某个 tool_call 是否该被写入请求。
func pairedCallIDs(msgs []agent.Message) map[string]bool {
	paired := map[string]bool{}
	var open []string
	for _, m := range msgs {
		switch m.Kind {
		case agent.KindToolCall:
			for _, c := range toolCallRefs(m) {
				if c.ID != "" {
					open = append(open, c.ID)
				}
			}
		case agent.KindToolResult:
			if id, ok := takePaired(&open, m.ToolCallID); ok {
				paired[id] = true
			}
		}
	}
	return paired
}

// toolCallRefs 还原一条 tool_call 消息声明的调用列表：
// 优先原生 ToolCalls 字段；旧会话回退到 ToolName/ToolArgs 或 Content 里的控制 JSON。
// 无法还原时返回 nil。ID 缺失时用 fallbackCallID 合成（与会话内结果配对保持一致）。
func toolCallRefs(m agent.Message) []agent.ToolCallRef {
	if len(m.ToolCalls) > 0 {
		return m.ToolCalls
	}
	name, args := m.ToolName, m.ToolArgs
	if name == "" {
		var req struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
		}
		if json.Unmarshal([]byte(m.Content), &req) == nil {
			name, args = req.Tool, req.Args
		}
	}
	if name == "" {
		return nil
	}
	return []agent.ToolCallRef{{ID: fallbackCallID(m), Name: name, Args: args}}
}

// encodeToolCallMsg 把工具调用消息编码为 assistant + tool_calls。
// Content 只放模型的前导说明文字（没有则为空），不再塞整个控制 JSON。
//
// paired 为「会收到结果回应」的 call id 集合（见 pairedCallIDs）：只保留其中
// 的调用 —— 发出无结果的 tool_calls 会让上游 400。过滤后若一个调用都不剩，
// 返回 ok=false，由调用方丢弃该消息（它已无任何可表达的内容）。
func encodeToolCallMsg(m agent.Message, paired map[string]bool) (chatMsg, bool) {
	calls := toolCallRefs(m)
	if len(calls) == 0 {
		// 连调用名都无法还原：消息本身只是文本，降级为普通 assistant 文本。
		return chatMsg{Role: agent.RoleAssistant, Content: m.Content}, true
	}
	wires := make([]wireToolCall, 0, len(calls))
	for _, c := range calls {
		if !paired[c.ID] {
			continue // 该调用没有对应结果：不得声明
		}
		var w wireToolCall
		w.ID = c.ID
		w.Type = "function"
		w.Function.Name = c.Name
		w.Function.Arguments = c.Args
		wires = append(wires, w)
	}
	if len(wires) == 0 {
		// 没有任何调用可声明（无结果回应 / 无法还原）：降级为普通 assistant 文本。
		// 空文本（或纯控制 JSON）降级后无信息量，丢弃（ok=false）。
		if body := m.Content; body != "" && !isControlJSON(body) {
			return chatMsg{Role: agent.RoleAssistant, Content: body}, true
		}
		return chatMsg{}, false
	}
	body := m.Content
	if isControlJSON(body) {
		body = "" // 控制 JSON 不是给人/模型看的说明文字，不必回灌
	}
	return chatMsg{Role: agent.RoleAssistant, Content: body, ToolCalls: wires}, true
}

// fallbackCallID 为旧会话里缺 ID 的调用生成稳定 ID（同一会话重复启动保持一致）。
func fallbackCallID(m agent.Message) string {
	h := fnv.New32a()
	h.Write([]byte(m.ToolName))
	h.Write([]byte{0})
	h.Write([]byte(m.ToolArgs))
	return fmt.Sprintf("call_%08x", h.Sum32())
}

// isControlJSON 判断文本是否为 agent 控制协议 JSON（含 action 字段）。
// 这类文本不应作为 assistant 的 content 回灌给模型（它是协议不是语言）。
func isControlJSON(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") {
		return false
	}
	var probe struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
	}
	if json.Unmarshal([]byte(t), &probe) != nil {
		return false
	}
	return probe.Action != "" || probe.Tool != ""
}

// toolResultBody 从会话里的「工具结果: {...}」文本中取出真正的结果正文。
// 原生 tool 消息的 content 应只有结果本身，不含协议包装。
func toolResultBody(content string) string {
	const prefix = "工具结果: "
	s := content
	if strings.HasPrefix(s, prefix) {
		s = s[len(prefix):]
	}
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") {
		var tr struct {
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		if json.Unmarshal([]byte(t), &tr) == nil {
			if tr.Error != "" {
				return tr.Output + "\n[error] " + tr.Error
			}
			return tr.Output
		}
	}
	return s
}

// ChatWithToolsCtx 实现 agent.LLMWithContext 接口：发送消息 + 原生工具定义，
// 支持通过 context 取消（ESC 中止时立即中断在途请求，无需等待 LLM 自然返回）。
func (c *OpenAI) ChatWithToolsCtx(ctx context.Context, messages []agent.Message, tools []plugins.Tool) (string, error) {
	req := chatReq{Model: c.Model}
	req.Messages = encodeMessages(messages)
	// 工具定义
	for _, t := range tools {
		req.Tools = append(req.Tools, pluginsToolToDef(t))
	}
	c.setThinking(&req)
	c.applyParams(&req)

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.applyUA(httpReq)
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// 使用 streamHTTPClient（无硬超时），依靠 ctx 支持取消
	resp, err := streamHTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices")
	}
	c.lastUsage = out.Usage

	msg := out.Choices[0].Message
	fr := out.Choices[0].FinishReason
	// 优先处理原生 tool_calls：转为 agent 循环可解析的 JSON 文本（附带模型说明文字作为 reason）
	if len(msg.ToolCalls) > 0 {
		return toolCallsToText(msg.ToolCalls, msg.Content), nil
	}
	// 统一出口：无论 finish_reason 是 stop / length / content_filter / 缺失，
	// 只要 agent 循环路径（本方法就是）拿到非空内容，就必须给出可被
	// parseCallJSONs 消化的输出（详情见 wrapReplyJSON）。
	if fr != "stop" && strings.TrimSpace(msg.Content) != "" {
		diag.Logf("llm", "非流式 finish_reason=%q 非 stop 且无工具调用，内容已按协议归一化", fr)
	}
	if wrapped := wrapReplyJSON(msg.Content); wrapped != "" {
		return wrapped, nil
	}
	return msg.Content, nil
}

// ============================================================================
// 流式响应（SSE）
// ============================================================================
// ============================================================================

// streamToolCall 流式 tool_calls 分片（OpenAI SSE 的 delta.tool_calls）。
// 同一 index 的多个分片按字段增量累积（name/arguments 分多片到达）。
type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamDelta SSE chunk 中的 delta 字段。
// 兼容 thinking 的两种字段名：reasoning（通用）与 reasoning_content（deepseek 系）。
type streamDelta struct {
	Content          string           `json:"content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []streamToolCall `json:"tool_calls,omitempty"`
}

// streamChunk 单个 SSE 数据块。
type streamChunk struct {
	Choices []struct {
		Index        int         `json:"index"`
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *agent.Usage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Thinking 返回当前分片中的思考文本（兼容 reasoning_content / reasoning）。
func (d streamDelta) Thinking() string {
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}
	return d.Reasoning
}

// isStreamOptionsReject 判断 400 响应是否由 stream_options / include_usage 参数
// 不被服务端支持引起（用于去掉该参数后降级重试，向前兼容）。
func isStreamOptionsReject(raw []byte) bool {
	s := string(raw)
	return strings.Contains(s, "stream_options") ||
		strings.Contains(s, "include_usage") ||
		strings.Contains(s, "unknown parameter") ||
		strings.Contains(s, "Unsupported parameter")
}

// streamHTTPClient 流式请求专用 client：不设硬超时——长思考/长回复时
// 单步 LLM 可能持续数分钟，120s 常规超时不够。
var streamHTTPClient = &http.Client{Timeout: 0}

// ChatStream 实现 agent.StreamLLM：流式对话（无 tools 参数，不带取消上下文）。
func (o *OpenAI) ChatStream(messages []agent.Message, onToken func(agent.StreamDelta)) (string, error) {
	return o.chatStream(context.Background(), messages, nil, onToken)
}

// ChatWithToolsStream 实现 agent.StreamToolCallLLM：流式对话 + 原生工具调用。
// 不带取消上下文（Background）。
func (o *OpenAI) ChatWithToolsStream(messages []agent.Message, tools []plugins.Tool, onToken func(agent.StreamDelta)) (string, error) {
	return o.chatStream(context.Background(), messages, tools, onToken)
}

// ChatWithToolsStreamCtx 实现 agent.CancellableStreamToolCallLLM：
// 流式对话 + 原生工具调用，HTTP 请求绑定 ctx（ESC 取消时立即中断在途流读取，不再阻塞到流自然结束）。
func (o *OpenAI) ChatWithToolsStreamCtx(ctx context.Context, messages []agent.Message, tools []plugins.Tool, onToken func(agent.StreamDelta)) (string, error) {
	return o.chatStream(ctx, messages, tools, onToken)
}

func (o *OpenAI) chatStream(ctx context.Context, messages []agent.Message, tools []plugins.Tool, onToken func(agent.StreamDelta)) (string, error) {
	req := chatReq{Model: o.Model, Stream: true, StreamOptions: &streamOptions{IncludeUsage: true}}
	req.Messages = encodeMessages(messages)
	for _, t := range tools {
		req.Tools = append(req.Tools, pluginsToolToDef(t))
	}
	o.setThinking(&req)
	o.applyParams(&req)
	reply, err := o.streamCoreCtx(ctx, &req, true, onToken)
	if err != nil {
		return reply, err
	}
	return reply, err
}

// AIChatStream 插件直连对话（流式）：ai_chat_stream 宿主函数的底层实现。
// 参数与 AIChat 相同；onToken 逐分片收到 Thinking/Content。
// 阻塞直到流结束，返回完整回复文本（不包装 agent 控制 JSON）。
func (o *OpenAI) AIChatStream(req AIChatRequest, onToken func(agent.StreamDelta)) (string, error) {
	r := chatReq{Model: o.Model, Stream: true, StreamOptions: &streamOptions{IncludeUsage: true}}
	if req.Model != "" {
		r.Model = req.Model
	}
	r.Messages = buildAIChatMessages(req)
	if req.MaxTokens > 0 {
		r.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		r.Temperature = req.Temperature
	}
	if req.Thinking != "" {
		o.setThinkingLevel(&r, req.Thinking)
	} else {
		o.setThinking(&r)
	}
	return o.streamCore(&r, false, onToken)
}

// streamCore 流式核心：SSE 逐行解析，逐分片回调 onToken（不带取消上下文）。
func (o *OpenAI) streamCore(req *chatReq, wrapReply bool, onToken func(agent.StreamDelta)) (string, error) {
	return o.streamCoreCtx(context.Background(), req, wrapReply, onToken)
}

// streamCoreCtx 流式核心：SSE 逐行解析，逐分片回调 onToken。
// wrapReply=true 时按 agent 循环协议包装（reply JSON / 工具调用 JSON）；
// wrapReply=false 时直接返回原始 content（插件直连用）。
// ctx 非空时将其绑定到 HTTP 请求：取消（ESC 中止）会中断在途流读取，函数立即返回。
func (o *OpenAI) streamCoreCtx(ctx context.Context, req *chatReq, wrapReply bool, onToken func(agent.StreamDelta)) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	// 发送请求：默认带 stream_options(include_usage) 以在流内获取精确用量；
	// 个别网关/旧版端点不支持下发 usage chunk（400），自动去掉该字段重试一次，不影响兼容。
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		o.applyUA(httpReq)
		if o.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
		}
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err = streamHTTPClient.Do(httpReq)
		if err != nil {
			return "", fmt.Errorf("http: %w", err)
		}
		if resp.StatusCode != 200 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			// 400 且因 stream_options 参数被拒：去掉后原样重发一次
			if attempt == 0 && resp.StatusCode == 400 && req.StreamOptions != nil && isStreamOptionsReject(raw) {
				req.StreamOptions = nil
				if body, err = json.Marshal(req); err != nil {
					return "", err
				}
				continue
			}
			return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
		}
		break
	}
	defer resp.Body.Close()

	var content, thinking strings.Builder
	toolCalls := map[int]*toolCall{} // index → 累积中的工具调用
	var toolOrder []int
	var lastFinishReason string

	br := bufio.NewReader(resp.Body)
	var chunks int
	for {
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				break
			}
			diag.Logf("llm", "流读取中断: chunks=%d err=%v", chunks, err)
			return "", fmt.Errorf("stream read: %w", err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue // 忽略 SSE 注释/心跳行
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		chunks++
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 容忍非标准行
		}
		if chunk.Error != nil {
			return "", fmt.Errorf("llm stream error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			o.lastUsage = chunk.Usage
			// 流内精确用量分片（stream_options.include_usage 启用时由服务端下发）：
			// 透传给 UI/Server，使其能在流末尾即时更新 tokens/缓存统计。
			if onToken != nil {
				onToken(agent.StreamDelta{Usage: chunk.Usage})
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		// 记录最后一个 chunk 的 finish_reason，用于判断模型是否正常结束
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
			lastFinishReason = chunk.Choices[0].FinishReason
		}
		d := chunk.Choices[0].Delta
		if th := d.Thinking(); th != "" {
			thinking.WriteString(th)
			if onToken != nil {
				onToken(agent.StreamDelta{Thinking: th})
			}
		}
		if d.Content != "" {
			content.WriteString(d.Content)
			if onToken != nil {
				onToken(agent.StreamDelta{Content: d.Content})
			}
		}
		for _, tc := range d.ToolCalls {
			merged := toolCalls[tc.Index]
			if merged == nil {
				merged = &toolCall{Type: tc.Type}
				toolCalls[tc.Index] = merged
				toolOrder = append(toolOrder, tc.Index)
			}
			if tc.ID != "" {
				merged.ID = tc.ID
			}
			if tc.Type != "" {
				merged.Type = tc.Type
			}
			if tc.Function.Name != "" {
				merged.Function.Name = tc.Function.Name
			}
			merged.Function.Arguments += tc.Function.Arguments
		}
	}

	// 日志：流结束状态（chunks/finish_reason）——排查「流突然结束/被截断」的关健证据。
	// 正常应为 chunks>0 且 finish_reason=stop/tool_calls；若 chunks 少且无 finish_reason，
	// 说明上游/网络提前断开（典型症状：任务看起来“自己停了”）。
	diag.Logf("llm", "流结束: chunks=%d finish=%q content=%d thinking=%d toolCalls=%d",
		chunks, lastFinishReason, content.Len(), thinking.Len(), len(toolOrder))

	// 原生工具调用优先：合并后的分片转成 agent 循环可解析的 JSON 控制文本（附带说明文字作为 reason）
	if len(toolOrder) > 0 {
		tcs := make([]toolCall, 0, len(toolOrder))
		for _, idx := range toolOrder {
			tcs = append(tcs, *toolCalls[idx])
		}
		return toolCallsToText(tcs, content.String()), nil
	}
	// 按协议归一化（仅 agent 循环路径 wrapReply=true）：无论 finish_reason 是 stop /
	// length / content_filter / 缺失，只要拿到非空内容，返回的就必须是 parseCallJSON
	// 能消化的东西（详情见 wrapReplyJSON）。原先只在 stop 时包装，其余直接返回裸
	// 文本 → 循环解析失败白耗一轮。
	// wrapReply=false（插件直连）必须拿到原始 content，一律不包装。
	if wrapReply {
		if lastFinishReason != "stop" && strings.TrimSpace(content.String()) != "" {
			diag.Logf("llm", "流 finish_reason=%q 非 stop 且无工具调用，内容已按协议归一化", lastFinishReason)
		}
		if wrapped := wrapReplyJSON(content.String()); wrapped != "" {
			return wrapped, nil
		}
	}
	return content.String(), nil
}

// ============================================================================
// 插件直连对话（ai_chat / ai_chat_stream）
// ============================================================================

// AIChatRequest 插件 ai_chat / ai_chat_stream 请求参数。
// 复用主程序已配置的 LLM 通道（BaseURL/APIKey/默认模型），可覆盖模型与参数。
type AIChatRequest struct {
	Model       string        `json:"model,omitempty"`       // 覆盖默认模型（空 = 用当前模型）
	System      string        `json:"system,omitempty"`      // 系统提示词（追加到 messages 最前）
	Messages    []AIChatMsg   `json:"messages,omitempty"`    // 对话消息（role: system/user/assistant）
	Temperature *float64      `json:"temperature,omitempty"` // 温度（0-2，nil = 不传）
	MaxTokens   int           `json:"max_tokens,omitempty"`  // 最大输出 token（0 = 不传）
	Thinking    string        `json:"thinking,omitempty"`    // 思考等级: auto/off/low/medium/high
	Images      []AIChatImage `json:"images,omitempty"`      // 图片（url 或 base64），附加到最后一条 user 消息
	Video       string        `json:"video,omitempty"`       // 视频 url，附加到最后一条 user 消息
}

// AIChatMsg 对话消息。
type AIChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AIChatImage 图片输入：URL 与 Base64 二选一。
type AIChatImage struct {
	URL    string `json:"url,omitempty"`    // 图片 URL（http/https 或 data: URI）
	Base64 string `json:"base64,omitempty"` // base64 编码的图片字节
	MIME   string `json:"mime,omitempty"`   // 图片 MIME（默认 image/jpeg；data: URI 场景可省）
}

// AIChatStreamDelta 流式分片（回调给插件 JS）。
type AIChatStreamDelta struct {
	Thinking string `json:"thinking,omitempty"` // 思考过程分片
	Content  string `json:"content,omitempty"`  // 回复内容分片
}

// buildAIChatMessages 组装 ai_chat 消息列表：system 提示词 + 多模态 content parts。
// 图片/视频以 OpenAI 兼容的 content 数组形式附加到最后一条 user 消息；
// 若没有 user 消息则自动补一条空 user 消息承载媒体。
func buildAIChatMessages(req AIChatRequest) []chatMsg {
	hasMedia := len(req.Images) > 0 || req.Video != ""
	msgs := make([]chatMsg, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: req.System})
	}
	lastUser := -1
	for i, m := range req.Messages {
		msgs = append(msgs, chatMsg{Role: m.Role, Content: m.Content})
		if m.Role == "user" {
			lastUser = i
		}
	}
	if !hasMedia {
		return msgs
	}
	// 先完整构建 media parts（text + images + video），再一次性挂到消息上，
	// 避免切片在 append 扩容后与已存储 content 失去共享。
	parts := make([]chatContentPart, 0, 1+len(req.Images)+1)
	if lastUser >= 0 {
		parts = append(parts, chatContentPart{Type: "text", Text: req.Messages[lastUser].Content})
	}
	for _, img := range req.Images {
		url := img.URL
		if url == "" && img.Base64 != "" {
			mime := img.MIME
			if mime == "" {
				mime = "image/jpeg"
			}
			url = "data:" + mime + ";base64," + img.Base64
		}
		if url != "" {
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatPartImageURL{URL: url}})
		}
	}
	if req.Video != "" {
		parts = append(parts, chatContentPart{Type: "video_url", VideoURL: &chatPartVideoURL{URL: req.Video}})
	}
	if lastUser >= 0 {
		// 原 user 消息被替换为多模态 content
		idx := lastUser
		if req.System != "" {
			idx++
		}
		msgs[idx] = chatMsg{Role: "user", Content: parts}
	} else {
		msgs = append(msgs, chatMsg{Role: "user", Content: parts})
	}
	return msgs
}

// Chat 实现 agent.LLM 接口。不发送 tools 参数，模型需从 system prompt 解析工具。
// Chat 实现基础的 LLM 接口：纯对话，不传 tools、非流式、**不包装控制协议 JSON**。
//
// 它只适合【期望拿到模型原始文本】的场景：
//   - AuxLLM 内部调用：GoalLoop judge（期望 JSON 判定结果）、BackgroundReview（期望 JSON review）
//   - 插件 llm_chat 宿主函数
//
// 不适合 agent 主循环：主循环需要 reply/tool 控制协议。不过 loop.go 的接口分派会优先
// 命中 ChatWithToolsCtx / ChatWithToolsStreamCtx 等更具体的接口，实际不会走到这里
// （真走到了，循环会因为拿不到控制 JSON 而注入“解析失败”提示——排查时看这里）。
func (c *OpenAI) Chat(messages []agent.Message) (string, error) {
	req := chatReq{Model: c.Model}
	req.Messages = encodeMessages(messages)
	c.setThinking(&req)
	c.applyParams(&req)

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest("POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.applyUA(httpReq)
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices")
	}
	c.lastUsage = out.Usage
	return out.Choices[0].Message.Content, nil
}

// AIChat 插件直连对话（非流式）：ai_chat 宿主函数的底层实现。
// 复用主程序已配置的 LLM 通道，支持覆盖模型 / 温度 / max_tokens / 系统提示词 /
// 思考等级 / 图片（url、base64）/ 视频（url）。返回 assistant 纯文本（不包装控制 JSON）。
func (o *OpenAI) AIChat(req AIChatRequest) (string, error) {
	r := chatReq{Model: o.Model}
	if req.Model != "" {
		r.Model = req.Model
	}
	r.Messages = buildAIChatMessages(req)
	if req.MaxTokens > 0 {
		r.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		r.Temperature = req.Temperature
	}
	if req.Thinking != "" {
		o.setThinkingLevel(&r, req.Thinking)
	} else {
		o.setThinking(&r)
	}

	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest("POST", o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	o.applyUA(httpReq)
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.Client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out chatResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices")
	}
	o.lastUsage = out.Usage
	return out.Choices[0].Message.Content, nil
}

// ============================================================================
// 消息摘要（Compactor 用）
// ============================================================================

// SummarizeMessages 生成会话摘要（供 Compactor 使用）。
// 输入由 agent.BuildSummaryInput 构造：工具调用序列化成单行 + 累计文件清单
// （对齐 pi serializeConversation + Cumulative File Tracking），而不是直接拼原始 Content。
func (o *OpenAI) SummarizeMessages(msgs []agent.Message, maxTokens int) (string, error) {
	sb := agent.BuildSummaryInput(msgs)
	req := chatReq{
		Model: o.Model,
		Messages: []chatMsg{
			{Role: "system", Content: "You are a conversation summarizer. Produce the structured summary exactly as instructed."},
			{Role: "user", Content: sb},
		},
		MaxTokens: maxTokens,
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest("POST", strings.TrimSuffix(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	o.applyUA(httpReq)
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	httpReq.Header.Set("X-Mizar-Request", "summarize")
	resp, err := o.Client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, string(respBody))
	}
	var cr chatResp
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return cr.Choices[0].Message.Content, nil
}

// ============================================================================
// 思考等级（ThinkingLevel）
// ============================================================================

const (
	thinkingAuto   = "auto"
	thinkingOff    = "off"
	thinkingLow    = "low"
	thinkingMedium = "medium"
	thinkingHigh   = "high"
)

var thinkingOrder = []string{thinkingOff, thinkingLow, thinkingMedium, thinkingHigh, thinkingAuto}

func (o *OpenAI) ThinkingEnabled() string {
	if o.ThinkingLevel == "" {
		return thinkingAuto
	}
	return o.ThinkingLevel
}

func (o *OpenAI) SetThinkingLevel(level string) {
	o.SetThinking(level)
}

func (o *OpenAI) SetThinking(level string) {
	o.ThinkingLevel = level
}

func (o *OpenAI) ToggleThinking() string {
	for i, l := range thinkingOrder {
		if o.ThinkingLevel == l {
			idx := (i + 1) % len(thinkingOrder)
			o.ThinkingLevel = thinkingOrder[idx]
			return o.ThinkingLevel
		}
	}
	o.ThinkingLevel = thinkingLow
	return o.ThinkingLevel
}

func (o *OpenAI) HasThinkingHeader() (bool, string) {
	switch o.ThinkingLevel {
	case "":
		return false, ""
	case thinkingOff:
		return true, "disabled"
	case thinkingLow, thinkingMedium, thinkingHigh:
		return true, "enabled"
	case thinkingAuto:
		return false, ""
	}
	return false, ""
}

func (o *OpenAI) ReasoningEffort() string {
	send, typ := o.HasThinkingHeader()
	if send && typ == "enabled" {
		switch o.ThinkingLevel {
		case thinkingLow:
			return "low"
		case thinkingMedium:
			return "medium"
		case thinkingHigh:
			return "high"
		}
	}
	return ""
}

func (o *OpenAI) LastUsage() *agent.Usage {
	return o.lastUsage
}

func (o *OpenAI) ModelName() string {
	return o.Model
}

// SetModel 运行时切换模型（Web UI 用）。线程安全由调用方保证（进程级设置，低频）。
func (o *OpenAI) SetModel(m string) {
	if m != "" {
		o.Model = m
	}
}

// Thinking 返回当前思考等级。
func (o *OpenAI) Thinking() string {
	return o.ThinkingLevel
}

// Info 返回 baseURL 与 apiKey（Web UI 拉模型清单用）。
func (o *OpenAI) Info() (string, string) {
	return o.BaseURL, o.APIKey
}

// ============================================================================
// 辅助
// ============================================================================

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// 仅处理必须转义的字符（"、\、控制字符），不转义 Unicode。
func escapeJSONString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
