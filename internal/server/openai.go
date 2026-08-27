package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"mizar/internal/agent"
)

// chatRequest OpenAI ChatCompletion 请求体（兼容子集）。
type chatRequest struct {
	Model     string          `json:"model"`
	Messages  []chatReqMsg    `json:"messages"`
	Stream    bool            `json:"stream,omitempty"`
	MaxTokens *int            `json:"max_tokens,omitempty"`
	Extra     json.RawMessage `json:"-"`
}

type chatReqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse OpenAI ChatCompletion 响应体。
type chatResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []chatChoice    `json:"choices"`
	Usage   *chatUsage      `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMsgResp `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMsgResp struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// handleModels GET /v1/models。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Services.HTTP.Load() {
		errJSON(w, http.StatusServiceUnavailable, "http service disabled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       s.agent.Model(),
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": "mizar",
		}},
	})
}

// handleChat POST /v1/chat/completions。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Services.HTTP.Load() {
		errJSON(w, http.StatusServiceUnavailable, "http service disabled")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		errJSON(w, http.StatusBadRequest, "messages required")
		return
	}
	// stream=true：SSE 流式响应（对齐 OpenAI chat.completion.chunk 格式）
	if req.Stream {
		s.handleChatStream(w, r, &req)
		return
	}
	// 最后一条 user 消息作为 task，其余作为会话历史
	task := ""
	var initial []agent.Message
	for i, m := range req.Messages {
		if i == len(req.Messages)-1 {
			task = m.Content
			continue
		}
		initial = append(initial, agent.Message{Role: m.Role, Content: m.Content})
	}
	if task == "" {
		task = req.Messages[len(req.Messages)-1].Content
	}
	s.agent.Initial = initial

	start := time.Now()
	reply, err := s.agent.Run(task)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "agent error: "+err.Error())
		return
	}
	s.logf("chat ok: model=%s task=%d chars, %s", req.Model, len(task), time.Since(start).Round(time.Millisecond))

	resp := chatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []chatChoice{{
			Index:        0,
			Message:      chatMsgResp{Role: "assistant", Content: reply},
			FinishReason: "stop",
		}},
	}
	writeJSON(w, http.StatusOK, resp)
}

// ============================================================================
// SSE 流式（stream=true）
// ============================================================================

// sseDelta SSE chunk 的 delta 字段（OpenAI 兼容）。
type sseDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // deepseek 系扩展
}

// sseChunk SSE 流式响应块（chat.completion.chunk）。
type sseChunk struct {
	ID      string     `json:"id"`
	Object  string     `json:"object"`
	Created int64      `json:"created"`
	Model   string     `json:"model"`
	Choices []sseChoice `json:"choices"`
}

type sseChoice struct {
	Index        int        `json:"index"`
	Delta        sseDelta   `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

// handleChatStream SSE 流式响应（stream=true）。
// 走完整 agent 工具循环（与 stream=false 一致），但通过 OnLLMStream 回调
// 增量提取最终回复的 text 字段，以 SSE chunk 格式转发。工具调用步的内容不转发。
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request, req *chatRequest) {
	fl, ok := w.(http.Flusher)
	if !ok {
		errJSON(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// 与 handleChat 相同的 task/initial 拆分
	task := ""
	initial := make([]agent.Message, 0, len(req.Messages))
	for i, m := range req.Messages {
		if i == len(req.Messages)-1 {
			task = m.Content
			continue
		}
		initial = append(initial, agent.Message{Role: m.Role, Content: m.Content})
	}
	if task == "" {
		task = req.Messages[len(req.Messages)-1].Content
	}
	s.agent.Initial = initial

	chunkID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	emit := func(d sseDelta, finish string) {
		var fr *string
		if finish != "" {
			fr = &finish
		}
		b, _ := json.Marshal(sseChunk{
			ID:      chunkID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   modelID(s.agent),
			Choices: []sseChoice{{Index: 0, Delta: d, FinishReason: fr}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}

	// 流式回调：OnLLMStream 收到每步 LLM 的 content 分片，提取 reply 的 text 增量转发
	var extr agent.StreamTextExtractor
	var started bool
	s.agent.OnLLMStream = func(step int, d agent.StreamDelta) {
		if d.Content == "" {
			return
		}
		inc := extr.Feed(d.Content)
		if inc == "" {
			return
		}
		if !started {
			started = true
			emit(sseDelta{Role: "assistant"}, "")
		}
		emit(sseDelta{Content: inc}, "")
	}

	start := time.Now()
	reply, err := s.agent.Run(task)
	s.agent.OnLLMStream = nil

	if err != nil {
		// 已开始流式：无法改状态码，发错误 chunk 收尾
		b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": err.Error()}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		return
	}

	if !started {
		// 纯工具任务（无回复文本流）：发送完整回复
		emit(sseDelta{Role: "assistant"}, "")
		emit(sseDelta{Content: reply}, "")
	}
	emit(sseDelta{}, "stop")
	fmt.Fprintf(w, "data: [DONE]\n\n")
	fl.Flush()
	s.logf("chat stream ok: model=%s task=%d chars, %s", req.Model, len(task), time.Since(start).Round(time.Millisecond))
}

// modelID 返回模型标识。
func modelID(a *agent.Agent) string { return a.Model() }
