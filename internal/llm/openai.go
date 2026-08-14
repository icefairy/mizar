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
)

// OpenAI 是 OpenAI 兼容客户端。
// 思考等级（ThinkingLevel）：
//   "auto"   — 不传 thinking 参数，由模型决定
//   "off"    — thinking: {type: "disabled"}
//   "low"    — thinking: {type: "enabled"}, reasoning_effort: "low"
//   "medium" — thinking: {type: "enabled"}, reasoning_effort: "medium"
//   "high"   — thinking: {type: "enabled"}, reasoning_effort: "high"
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

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model           string    `json:"model"`
	Messages        []chatMsg `json:"messages"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	Thinking        *struct {
		Type string `json:"type"`
	} `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // openai beta: low/medium/high
}

// SummarizeMessages 生成会话摘要（供 Compactor 使用）。
func (o *OpenAI) ModelName() string {
	return o.Model
}

// SummarizeMessages 生成会话摘要（供 Compactor 使用）。
// 缓存友好设计（参照 pi compaction）：摘要请求是独立的一次性请求，
// 其 prompt 前缀与主对话不同，天然不会污染/命中主对话的 prefix cache。
// 注意：这里显式使用随机 system 头，防止摘要请求自身反复命中同一缓存
// 造成无意义开销（摘要每次内容都不同，缓存无复用价值）。
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

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Usage *agent.Usage `json:"usage,omitempty"`
}

// OpenAI 是 OpenAI 兼容客户端。

const (
	thinkingAuto   = "auto"
	thinkingOff    = "off"
	thinkingLow    = "low"
	thinkingMedium = "medium"
	thinkingHigh   = "high"
)

var thinkingOrder = []string{thinkingOff, thinkingLow, thinkingMedium, thinkingHigh, thinkingAuto}

// ThinkingEnabled 返回思考等级描述字符串
func (o *OpenAI) ThinkingEnabled() string {
	if o.ThinkingLevel == "" {
		return thinkingAuto
	}
	return o.ThinkingLevel
}

// SetThinkingLevel 设置思考等级（兼容 main.go 命名）
func (o *OpenAI) SetThinkingLevel(level string) {
	o.SetThinking(level)
}

// SetThinking 设置思考等级（auto/off/low/medium/high）
func (o *OpenAI) SetThinking(level string) {
	o.ThinkingLevel = level
}

// ToggleThinking 在 auto/off/low/medium/high 之间循环切换
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

// HasThinkingHeader 返回是否应当发送 thinking 请求头
func (o *OpenAI) HasThinkingHeader() (bool, string) {
	// nil/auto = 不传；off = disabled；low/medium/high = enabled + reasoning_effort
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

// ReasoningEffort 返回 reasoning_effort 值（仅 enabled 时返回）
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

// LastUsage 返回最后一次响应的 token 用量
func (o *OpenAI) LastUsage() *agent.Usage {
	return o.lastUsage
}

// Chat 实现 agent.LLM 接口。
func (c *OpenAI) Chat(messages []agent.Message) (string, error) {
	req := chatReq{Model: c.Model}
	for _, m := range messages {
		req.Messages = append(req.Messages, chatMsg{Role: m.Role, Content: m.Content})
	}
	// 思考模式：根据 ThinkingLevel 设置 thinking + reasoning_effort
	if send, typ := c.HasThinkingHeader(); send {
		req.Thinking = &struct {
			Type string `json:"type"`
		}{Type: typ}
		if effort := c.ReasoningEffort(); effort != "" {
			req.ReasoningEffort = effort
		}
	}
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
	c.lastUsage = out.Usage // 记录用量供 TUI 展示
	return out.Choices[0].Message.Content, nil
}

// FromMessages 不再需要，agent.Message 直接使用。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
