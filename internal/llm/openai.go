// Package llm 提供 OpenAI 兼容的 LLM 客户端（可对接璇玑网关/任意兼容端点）。
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mizar/internal/agent"
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
	// 最后一次响应的 token 用量（只读，供 TUI 状态栏展示）
	lastUsage *agent.Usage
	// forcedToolCalls 连续强制工具调用的次数：
	// 模型在可用工具时跳过工具调用（直接 reply），此计数器递增；
	// 重置条件：模型实际调用了工具 或 请求未携带工具定义。
	forcedToolCalls int
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
type chatMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
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
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	Tools       []toolDef `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"` // "auto" / "required" / 命名对象
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Thinking    *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
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

// toolCallsToText 将原生 tool_calls 转为 agent 循环可解析的 JSON 文本。
// 新 schema 中 arguments 直接是原始参数（如 {"command":"hostname"}），
// 用 json.Marshal 转成转义后的 JSON 字符串填入 args 字段。
// 期望输出：{"action":"tool","tool":"bash","args":"{\"command\":\"hostname\"}"}
//
// 空 arguments 仍生成工具调用（args=""），由 agent 循环做空参数兜底。
func toolCallsToText(tcs []toolCall) string {
	if len(tcs) == 0 {
		return ""
	}
	tc := tcs[0]
	argsRaw := tc.Function.Arguments
	argsEscaped, _ := json.Marshal(argsRaw)
	return fmt.Sprintf(`{"action":"tool","tool":"%s","args":%s}`, tc.Function.Name, argsEscaped)
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

// maxForcedToolCalls 连续强制工具调用的上限，超过则放弃强制（防止死循环）。
const maxForcedToolCalls = 3

// setToolChoice 根据当前 forcedToolCalls 状态设置 tool_choice。
// 当工具列表非空且已累积强制次数时，设为 "required" 强制模型调用工具；
// 否则不传该字段（模型默认 auto）。
func (o *OpenAI) setToolChoice(req *chatReq, tools []plugins.Tool) {
	if len(tools) > 0 && o.forcedToolCalls >= 1 {
		req.ToolChoice = "required"
	}
}

// updateForcedToolCalls 根据本次响应是否包含工具调用，更新 forcedToolCalls 计数器。
// hadToolCall=true 时重置；hadToolCall=false 时递增（不超过上限）。
func (o *OpenAI) updateForcedToolCalls(hadToolCall bool) {
	if hadToolCall {
		o.forcedToolCalls = 0
	} else {
		if o.forcedToolCalls < maxForcedToolCalls {
			o.forcedToolCalls++
		}
	}
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
	for _, m := range messages {
		req.Messages = append(req.Messages, chatMsg{Role: m.Role, Content: m.Content})
	}
	// 工具定义
	for _, t := range tools {
		req.Tools = append(req.Tools, pluginsToolToDef(t))
	}
	c.setToolChoice(&req, tools)
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
	// 优先处理原生 tool_calls：转为 agent 循环可解析的 JSON 文本
	if len(msg.ToolCalls) > 0 {
		c.updateForcedToolCalls(true)
		return toolCallsToText(msg.ToolCalls), nil
	}
	c.updateForcedToolCalls(false)
	// finish_reason="stop" 且内容为纯文本时，包装成 reply JSON。
	// 若模型已输出控制 JSON（含混合文本中嵌入的 tool/reply JSON），优先透传提取结果，
	// 避免“解释文本 + tool JSON”被整段包成 reply 导致工具不执行、JSON 原样显示。
	if fr == "stop" {
		s := strings.TrimSpace(msg.Content)
		if extracted, ok := agent.ExtractActionJSON(s); ok {
			return extracted, nil
		}
		// 修复：畸形工具调用 JSON（如缺 tool 字段、内层引号未转义）不包装成 reply，
		// 返回原始内容让 agent 循环的 parseCallJSON 走错误处理路径（注入纠正提示）。
		if agent.IsToolCallText(s) {
			return s, nil
		}
		return fmt.Sprintf(`{"action":"reply","text":"%s"}`, escapeJSONString(msg.Content)), nil
	}
	return msg.Content, nil
}

// ============================================================================
// 流式响应（SSE）
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

// streamHTTPClient 流式请求专用 client：不设硬超时——长思考/长回复时
// 单步 LLM 可能持续数分钟，120s 常规超时不够。
var streamHTTPClient = &http.Client{Timeout: 0}

// ChatStream 实现 agent.StreamLLM：流式对话（无 tools 参数）。
func (o *OpenAI) ChatStream(messages []agent.Message, onToken func(agent.StreamDelta)) (string, error) {
	return o.chatStream(messages, nil, onToken)
}

// ChatWithToolsStream 实现 agent.StreamToolCallLLM：流式对话 + 原生工具调用。
func (o *OpenAI) ChatWithToolsStream(messages []agent.Message, tools []plugins.Tool, onToken func(agent.StreamDelta)) (string, error) {
	return o.chatStream(messages, tools, onToken)
}

func (o *OpenAI) chatStream(messages []agent.Message, tools []plugins.Tool, onToken func(agent.StreamDelta)) (string, error) {
	req := chatReq{Model: o.Model, Stream: true}
	for _, m := range messages {
		req.Messages = append(req.Messages, chatMsg{Role: m.Role, Content: m.Content})
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, pluginsToolToDef(t))
	}
	o.setToolChoice(&req, tools)
	o.setThinking(&req)
	reply, err := o.streamCore(&req, true, onToken)
	if err != nil {
		return reply, err
	}
	// 根据响应是否包含工具调用，更新 forcedToolCalls 计数器
	o.updateForcedToolCalls(isToolCallResponse(reply))
	return reply, err
}

// AIChatStream 插件直连对话（流式）：ai_chat_stream 宿主函数的底层实现。
// 参数与 AIChat 相同；onToken 逐分片收到 Thinking/Content。
// 阻塞直到流结束，返回完整回复文本（不包装 agent 控制 JSON）。
func (o *OpenAI) AIChatStream(req AIChatRequest, onToken func(agent.StreamDelta)) (string, error) {
	r := chatReq{Model: o.Model, Stream: true}
	if req.Model != "" {
		r.Model = req.Model
	}
	r.Messages = buildAIChatMessages(req)
	if req.MaxTokens > 0 {
		r.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		r.Temperature = *req.Temperature
	}
	if req.Thinking != "" {
		o.setThinkingLevel(&r, req.Thinking)
	} else {
		o.setThinking(&r)
	}
	return o.streamCore(&r, false, onToken)
}

// streamCore 流式核心：SSE 逐行解析，逐分片回调 onToken。
// wrapReply=true 时按 agent 循环协议包装（reply JSON / 工具调用 JSON）；
// wrapReply=false 时直接返回原始 content（插件直连用）。
func (o *OpenAI) streamCore(req *chatReq, wrapReply bool, onToken func(agent.StreamDelta)) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest("POST", o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := streamHTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var content, thinking strings.Builder
	toolCalls := map[int]*toolCall{} // index → 累积中的工具调用
	var toolOrder []int
	var lastFinishReason string

	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				break
			}
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
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 容忍非标准行
		}
		if chunk.Error != nil {
			return "", fmt.Errorf("llm stream error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			o.lastUsage = chunk.Usage
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

	// 原生工具调用优先：合并后的分片转成 agent 循环可解析的 JSON 控制文本
	if len(toolOrder) > 0 {
		tcs := make([]toolCall, 0, len(toolOrder))
		for _, idx := range toolOrder {
			tcs = append(tcs, *toolCalls[idx])
		}
		return toolCallsToText(tcs), nil
	}
	// 按协议包装回复（仅 agent 循环路径 wrapReply=true）。
	// 若模型输出了控制 JSON（含"解释文本 + tool JSON"混合形态），优先透传提取结果；
	// 否则包成 reply（此时纯文本展示，工具调用已在上层处理）。
	if wrapReply && lastFinishReason == "stop" {
		s := strings.TrimSpace(content.String())
		if extracted, ok := agent.ExtractActionJSON(s); ok {
			return extracted, nil
		}
		// 修复：模型输出为工具调用 JSON 但 ExtractActionJSON 提取失败
		// （如缺 tool 字段、内层引号未转义等畸形 JSON），说明模型想调工具但格式错误。
		// 此时不应包装成 reply 让用户看到原始 JSON，而是返回原始内容让 agent 循环的
		// parseCallJSON 走错误处理路径（注入纠正提示让模型重试）。
		if agent.IsToolCallText(s) {
			return s, nil
		}
		return fmt.Sprintf(`{"action":"reply","text":"%s"}`, escapeJSONString(content.String())), nil
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
func (c *OpenAI) Chat(messages []agent.Message) (string, error) {
	req := chatReq{Model: c.Model}
	for _, m := range messages {
		req.Messages = append(req.Messages, chatMsg{Role: m.Role, Content: m.Content})
	}
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
		r.Temperature = *req.Temperature
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
// 使用随机 system 头防止自身反复命中缓存（摘要每次内容不同，缓存无复用价值）。
func (o *OpenAI) SummarizeMessages(msgs []agent.Message, maxTokens int) (string, error) {
	var sb bytes.Buffer
	sb.WriteString(agent.SummaryPrompt)
	sb.WriteString("\n\n--- conversation ---\n")
	for _, m := range msgs {
		sb.WriteString("[" + m.Role + "]\n" + m.Content + "\n")
	}
	req := chatReq{
		Model: o.Model,
		Messages: []chatMsg{
			{Role: "system", Content: "You are a conversation summarizer. Produce the structured summary exactly as instructed."},
			{Role: "user", Content: sb.String()},
		},
		MaxTokens: maxTokens,
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest("POST", strings.TrimSuffix(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
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

// isToolCallResponse 判断响应是否为工具调用 JSON（{"action":"tool",...}）。
func isToolCallResponse(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, `{"action":"tool"`)
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
