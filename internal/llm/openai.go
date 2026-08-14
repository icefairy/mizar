// Package llm 提供 OpenAI 兼容的 LLM 客户端（可对接璇玑网关/任意兼容端点）。
package llm

import (
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

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model           string    `json:"model"`
	Messages        []chatMsg `json:"messages"`
	Tools           []toolDef `json:"tools,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	Thinking        *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatResp struct {
	Choices []struct {
		Index        int `json:"index"`
		Message      struct {
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
// 参数统一使用 args: string（JSON 字符串），与 agent 的 callRequest 格式对齐。
func pluginsToolToDef(t plugins.Tool) toolDef {
	return toolDef{
		Type: "function",
		Function: funcDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"args": map[string]any{
						"type":        "string",
						"description": "JSON string arguments for the tool",
					},
				},
				"required": []string{"args"},
			},
		},
	}
}

// toolCallsToText 将原生 tool_calls 转为 agent 循环可解析的 JSON 文本。
// agent 的 callParser 期望格式：
//   {"action":"tool","tool":"bash","args":"{\"command\":\"hostname\"}"}
func toolCallsToText(tcs []toolCall) string {
	if len(tcs) == 0 {
		return ""
	}
	tc := tcs[0] // 每次只处理第一个工具调用
	// arguments 已经是 JSON 对象字符串（如 {"command":"hostname"}）
	// 需要用 json.Marshal 把它包成 JSON 字符串值（带引号转义）
	argsEscaped, _ := json.Marshal(tc.Function.Arguments)
	return fmt.Sprintf(`{"action":"tool","tool":"%s","args":%s}`, tc.Function.Name, argsEscaped)
}

// setThinking 根据 ThinkingLevel 设置思考参数
func (c *OpenAI) setThinking(req *chatReq) {
	if send, typ := c.HasThinkingHeader(); send {
		req.Thinking = &struct {
			Type string `json:"type"`
		}{Type: typ}
		if effort := c.ReasoningEffort(); effort != "" {
			req.ReasoningEffort = effort
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
	// 优先处理原生 tool_calls：转为 agent 循环可解析的 JSON 文本
	if len(msg.ToolCalls) > 0 {
		return toolCallsToText(msg.ToolCalls), nil
	}
	return msg.Content, nil
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