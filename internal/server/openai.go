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

// modelID 返回模型标识。
func modelID(a *agent.Agent) string { return a.Model() }
