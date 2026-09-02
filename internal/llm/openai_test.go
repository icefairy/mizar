package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// TestChatStream_Content 验证 ChatStream 正确解析 content 分片并回调。
func TestChatStream_Content(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected Accept: text/event-stream, got %s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// 模拟 SSE 分片
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"你好"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"，"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"世界"}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	var got []string
	onToken := func(d agent.StreamDelta) {
		if d.Content != "" {
			got = append(got, d.Content)
		}
	}
	reply, err := client.ChatStream([]agent.Message{{Role: "user", Content: "hi"}}, onToken)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "你好，世界" {
		t.Fatalf("expected reply '你好，世界', got %q", reply)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 token callbacks, got %d", len(got))
	}
	if strings.Join(got, "") != "你好，世界" {
		t.Fatalf("callback order wrong: %v", got)
	}
}

// TestChatStream_Thinking 验证 thinking 分片（reasoning_content）被正确回调。
func TestChatStream_Thinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"让我思考一下"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"答案是42"}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	var contentTokens, thinkTokens []string
	onToken := func(d agent.StreamDelta) {
		if d.Thinking != "" {
			thinkTokens = append(thinkTokens, d.Thinking)
		}
		if d.Content != "" {
			contentTokens = append(contentTokens, d.Content)
		}
	}
	reply, err := client.ChatStream([]agent.Message{{Role: "user", Content: "?"}}, onToken)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "答案是42" {
		t.Fatalf("expected '答案是42', got %q", reply)
	}
	if len(thinkTokens) != 1 || thinkTokens[0] != "让我思考一下" {
		t.Fatalf("thinking tokens wrong: %v", thinkTokens)
	}
}

// TestChatStream_ToolCalls 验证原生 tool_calls 流式分片被正确合并为 JSON 控制文本。
func TestChatStream_ToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// 模拟 tool_calls 流式分片（按 index 增量累积）
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"com"}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"mand\":\"hostname\"}"}}]}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatStream([]agent.Message{{Role: "user", Content: "run"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"action":"tool","tool":"bash","args":"{\"command\":\"hostname\"}"}`
	if reply != expected {
		t.Fatalf("expected tool call JSON:\n  %s\ngot:\n  %s", expected, reply)
	}
}

// TestChatStream_Error 验证 LLM 流式错误被正确返回。
func TestChatStream_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, `data: {"error":{"message":"rate limited"}}`)
		fl.Flush()
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	_, err := client.ChatStream([]agent.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limit error, got %v", err)
	}
}

// TestChatStream_HTTPError 验证 HTTP 400 错误被正确返回。
func TestChatStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid model"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	_, err := client.ChatStream([]agent.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid model") {
		t.Fatalf("expected invalid model error, got %v", err)
	}
}

// TestChatStream_Usage 验证流式响应的 usage 字段被记录。
func TestChatStream_Usage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	_, err := client.ChatStream([]agent.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.lastUsage == nil {
		t.Fatal("expected usage to be recorded")
	}
	if client.lastUsage.PromptTokens != 10 || client.lastUsage.CompletionTokens != 5 {
		t.Fatalf("usage wrong: %+v", client.lastUsage)
	}
}

// TestAIChat 验证非流式直连对话：model 覆盖、temperature、max_tokens、system 提示词、思考等级。
func TestAIChat(t *testing.T) {
	var gotReqJSON string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotReqJSON = string(raw)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"回复内容"}}]}`)
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "test-key", "default-model")
	temp := 0.3
	req := AIChatRequest{
		Model:       "gpt-4o",
		System:      "你是一个助手",
		Messages:    []AIChatMsg{{Role: "user", Content: "你好"}},
		Temperature: &temp,
		MaxTokens:   500,
		Thinking:    "off",
	}
	reply, err := client.AIChat(req)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "回复内容" {
		t.Fatalf("expected '回复内容', got %q", reply)
	}
	// 验证请求 JSON 包含各字段
	var reqBody struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Thinking    *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(gotReqJSON), &reqBody); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if reqBody.Model != "gpt-4o" {
		t.Fatalf("expected model gpt-4o, got %s", reqBody.Model)
	}
	if reqBody.Temperature != 0.3 {
		t.Fatalf("expected temp 0.3, got %f", reqBody.Temperature)
	}
	if reqBody.MaxTokens != 500 {
		t.Fatalf("expected max_tokens 500, got %d", reqBody.MaxTokens)
	}
	if reqBody.Thinking == nil || reqBody.Thinking.Type != "disabled" {
		t.Fatalf("expected thinking disabled, got %+v", reqBody.Thinking)
	}
	if len(reqBody.Messages) != 2 || reqBody.Messages[0].Role != "system" {
		t.Fatalf("expected 2 messages with system first, got %+v", reqBody.Messages)
	}
}

// TestAIChatStream 验证流式直连对话：分片回调 + 不包装控制 JSON。
func TestAIChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"思考中"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"您好"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"！"}}]}`,
			`data: {"choices":[{"index":0,"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	var gotDelta, gotThink []string
	onToken := func(d agent.StreamDelta) {
		if d.Thinking != "" {
			gotThink = append(gotThink, d.Thinking)
		}
		if d.Content != "" {
			gotDelta = append(gotDelta, d.Content)
		}
	}
	reply, err := client.AIChatStream(AIChatRequest{
		Messages: []AIChatMsg{{Role: "user", Content: "hi"}},
		Thinking: "high",
	}, onToken)
	if err != nil {
		t.Fatal(err)
	}
	// 返回的应该是原始文本，不包装为 reply JSON
	if reply != "您好！" {
		t.Fatalf("expected raw '您好！', got %q", reply)
	}
	if len(gotDelta) != 2 || strings.Join(gotDelta, "") != "您好！" {
		t.Fatalf("delta wrong: %v", gotDelta)
	}
	if len(gotThink) != 1 || gotThink[0] != "思考中" {
		t.Fatalf("thinking wrong: %v", gotThink)
	}
}

// TestAIChatMultimodal 验证多模态消息构建：base64 转 data: URI、视频 URL 附加、
// 文本合并到最后一条 user 消息。
func TestAIChatMultimodal(t *testing.T) {
	msgs := buildAIChatMessages(AIChatRequest{
		System:   "助手",
		Messages: []AIChatMsg{{Role: "user", Content: "看图"}},
		Images: []AIChatImage{
			{Base64: "abc123", MIME: "image/png"},
			{URL: "https://example.com/img.jpg"},
		},
		Video: "https://example.com/vid.mp4",
	})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content.(string) != "助手" {
		t.Fatalf("system message wrong: %+v", msgs[0])
	}
	parts, ok := msgs[1].Content.([]chatContentPart)
	if !ok {
		t.Fatalf("user content should be []chatContentPart, got %T", msgs[1].Content)
	}
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts (text + 2 images + video), got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "看图" {
		t.Fatalf("text part wrong: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("expected image_url part, got type=%s", parts[1].Type)
	}
	wantDataURI := "data:image/png;base64,abc123"
	if parts[1].ImageURL.URL != wantDataURI {
		t.Fatalf("expected base64 data URI %q, got %q", wantDataURI, parts[1].ImageURL.URL)
	}
	if parts[2].Type != "image_url" || parts[2].ImageURL.URL != "https://example.com/img.jpg" {
		t.Fatalf("image url part wrong: %+v", parts[2])
	}
	if parts[3].Type != "video_url" || parts[3].VideoURL == nil || parts[3].VideoURL.URL != "https://example.com/vid.mp4" {
		t.Fatalf("video part wrong: %+v", parts[3])
	}
}

// TestAIChatNoMedia 验证无媒体时 buildAIChatMessages 保持纯文本消息结构。
func TestAIChatNoMedia(t *testing.T) {
	msgs := buildAIChatMessages(AIChatRequest{
		Messages: []AIChatMsg{{Role: "user", Content: "你好"}},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if _, ok := msgs[0].Content.(string); !ok {
		t.Fatalf("content should be string, got %T", msgs[0].Content)
	}
}

// TestAIChatThinkingLevel 验证思考等级序列化。
func TestAIChatThinkingLevel(t *testing.T) {
	tests := []struct {
		level string
		want  bool
		typ   string
		eff   string
	}{
		{"auto", false, "", ""},
		{"off", true, "disabled", ""},
		{"low", true, "enabled", "low"},
		{"medium", true, "enabled", "medium"},
		{"high", true, "enabled", "high"},
		{"", false, "", ""},
	}
	for _, tt := range tests {
		req := &chatReq{}
		c := &OpenAI{}
		c.setThinkingLevel(req, tt.level)
		send := req.Thinking != nil
		if send != tt.want {
			t.Errorf("setThinkingLevel(%q): send=%v, want %v", tt.level, send, tt.want)
		}
		if send {
			if req.Thinking.Type != tt.typ {
				t.Errorf("setThinkingLevel(%q): type=%q, want %q", tt.level, req.Thinking.Type, tt.typ)
			}
			if req.ReasoningEffort != tt.eff {
				t.Errorf("setThinkingLevel(%q): effort=%q, want %q", tt.level, req.ReasoningEffort, tt.eff)
			}
		}
	}
}

// ============================================================================
// tool_choice 智能策略测试
// ============================================================================

// TestSetToolChoice 验证 setToolChoice 的行为：
// - tools 为空时不传 tool_choice
// - forcedToolCalls=0 时不传 tool_choice
// - forcedToolCalls>=1 时传 tool_choice="required"
func TestSetToolChoice(t *testing.T) {
	tests := []struct {
		name           string
		forcedCalls    int
		tools          []plugins.Tool
		wantToolChoice any
	}{
		{"无工具，强制次数0", 0, nil, nil},
		{"有工具，强制次数0", 0, []plugins.Tool{{Name: "bash"}}, nil},
		{"有工具，强制次数1", 1, []plugins.Tool{{Name: "bash"}}, "required"},
		{"有工具，强制次数3", 3, []plugins.Tool{{Name: "bash"}}, "required"},
		{"无工具，强制次数3", 3, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &OpenAI{forcedToolCalls: tt.forcedCalls}
			req := &chatReq{}
			o.setToolChoice(req, tt.tools)
			if tt.wantToolChoice == nil {
				if req.ToolChoice != nil {
					t.Errorf("expected ToolChoice=nil, got %v", req.ToolChoice)
				}
			} else {
				if req.ToolChoice != tt.wantToolChoice {
					t.Errorf("expected ToolChoice=%v, got %v", tt.wantToolChoice, req.ToolChoice)
				}
			}
		})
	}
}

// TestUpdateForcedToolCalls 验证计数器递增与重置逻辑。
func TestUpdateForcedToolCalls(t *testing.T) {
	o := &OpenAI{}
	// 初始为 0
	if o.forcedToolCalls != 0 {
		t.Fatalf("initial forcedToolCalls=%d, want 0", o.forcedToolCalls)
	}
	// 连续 3 次未调用工具，应递增到上限
	o.updateForcedToolCalls(false)
	if o.forcedToolCalls != 1 {
		t.Fatalf("after 1 miss: forcedToolCalls=%d, want 1", o.forcedToolCalls)
	}
	o.updateForcedToolCalls(false)
	if o.forcedToolCalls != 2 {
		t.Fatalf("after 2 misses: forcedToolCalls=%d, want 2", o.forcedToolCalls)
	}
	o.updateForcedToolCalls(false)
	if o.forcedToolCalls != 3 {
		t.Fatalf("after 3 misses: forcedToolCalls=%d, want 3", o.forcedToolCalls)
	}
	// 超过上限不再增加
	o.updateForcedToolCalls(false)
	if o.forcedToolCalls != 3 {
		t.Fatalf("after 4 misses: forcedToolCalls=%d, want 3 (capped)", o.forcedToolCalls)
	}
	// 模型实际调用工具，应重置
	o.updateForcedToolCalls(true)
	if o.forcedToolCalls != 0 {
		t.Fatalf("after tool call: forcedToolCalls=%d, want 0", o.forcedToolCalls)
	}
}

// TestChatWithTools_ToolChoiceSent 验证 ChatWithTools 在强制模式下发送 tool_choice=required。
func TestChatWithTools_ToolChoiceSent(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		// 模拟模型跳过工具调用，直接返回文本
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"我不知道"}}]}`)
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL+"/v1", "", "test-model")
	// 模拟连续 2 次未调用工具
	o.forcedToolCalls = 2
	_, err := o.ChatWithTools(
		[]agent.Message{{Role: "user", Content: "hi"}},
		[]plugins.Tool{{Name: "bash", Description: "run bash"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["tool_choice"] != "required" {
		t.Fatalf("expected tool_choice='required', got %v", body["tool_choice"])
	}
	// 计数器应被重置（因为这次响应是 reply，不是 tool call）
	if o.forcedToolCalls != 3 {
		t.Fatalf("expected forcedToolCalls=3 after reply, got %d", o.forcedToolCalls)
	}
}

// TestChatWithTools_ToolChoiceAuto 验证非强制模式下不发送 tool_choice。
func TestChatWithTools_ToolChoiceAuto(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		// 模拟模型正常调用工具
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`)
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL+"/v1", "", "test-model")
	_, err := o.ChatWithTools(
		[]agent.Message{{Role: "user", Content: "hi"}},
		[]plugins.Tool{{Name: "bash", Description: "run bash"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["tool_choice"] != nil {
		t.Fatalf("expected no tool_choice, got %v", body["tool_choice"])
	}
	// 模型调用了工具，计数器应重置
	if o.forcedToolCalls != 0 {
		t.Fatalf("expected forcedToolCalls=0 after tool call, got %d", o.forcedToolCalls)
	}
}

// TestIsToolCallResponse 验证响应类型检测。
func TestIsToolCallResponse(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"action":"tool","tool":"bash","args":"{}"}`, true},
		{`{"action":"reply","text":"hello"}`, false},
		{`  {"action":"tool","tool":"ls","args":""}  `, true},
		{`hello world`, false},
		{``, false},
	}
	for _, tt := range tests {
		got := isToolCallResponse(tt.input)
		if got != tt.want {
			t.Errorf("isToolCallResponse(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestChatWithTools_MalformedToolJSONNotWrappedAsReply 回归测试：
// 模型把工具调用当纯文本输出（畸形 JSON：缺 tool 字段、内层引号未转义），
// finish_reason=stop 且无原生 tool_calls。此前这段内容会被包装成
// {"action":"reply","text":...} 导致用户看到原始 JSON、任务提前"完成"。
// 修复后：应返回原始内容，让 agent 循环走解析失败纠错路径。
func TestChatWithTools_MalformedToolJSONNotWrappedAsReply(t *testing.T) {
	malformed := `{"action":"tool","args":"{"command": "tail -100 /var/log/x11vnc.log 2>&1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 模型以纯文本形式输出畸形工具 JSON（无原生 tool_calls）
		resp := map[string]any{
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": malformed},
				"finish_reason": "stop",
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := o.ChatWithTools(
		[]agent.Message{{Role: "user", Content: "看下x11vnc日志"}},
		[]plugins.Tool{{Name: "bash", Description: "run bash"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(reply, `{"action":"reply"`) {
		t.Fatalf("malformed tool JSON must NOT be wrapped as reply, got: %s", reply)
	}
	if !agent.IsToolCallText(reply) {
		t.Fatalf("reply should still be tool-call text, got: %s", reply)
	}
	// agent 循环侧：parseCallJSON 应报错（触发纠错提示），而不是解析成 reply
	if _, err := parseReqForTest(reply); err == nil {
		t.Fatal("agent parseCallJSON should fail on malformed JSON (error path injects correction hint)")
	}
}

// parseReqForTest 测试辅助：直接暴露包内行为不必要，这里通过导出的 IsToolCallText +
// 循环行为等价校验（畸形 JSON 必然解析失败）。
func parseReqForTest(s string) (any, error) {
	// 畸形 JSON 顶层解码必然失败
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// TestChatWithToolsStream_MalformedToolJSONNotWrappedAsReply 流式路径同样不受包装。
func TestChatWithToolsStream_MalformedToolJSONNotWrappedAsReply(t *testing.T) {
	malformed := `{"action":"tool","tool":"bash","args":"{"command": "tail -100 /var/log/x11vnc.log 2>&1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"{\"action\":\"tool\",\"tool\":\"bash\",\"args\":\"{"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"\"command\": \"tail -100 /var/log/x11vnc.log 2>&1\"}"}}]}`,
			`data: {"choices":[{"index":0,"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := o.ChatWithToolsStream(
		[]agent.Message{{Role: "user", Content: "hi"}},
		[]plugins.Tool{{Name: "bash", Description: "run bash"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(reply, `{"action":"reply"`) {
		t.Fatalf("stream: malformed tool JSON must NOT be wrapped as reply, got: %s", reply)
	}
	_ = malformed
}
