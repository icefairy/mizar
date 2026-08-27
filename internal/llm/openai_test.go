package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mizar/internal/agent"
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