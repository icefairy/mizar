package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mizar/internal/agent"
)

// 模拟 OpenAI 兼容流式端点：按注入的 chunk 内容逐行 SSE 下发。
func mockStreamServer(t *testing.T, chunks []string, checkReq func(r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1<<20)
		n, _ := r.Body.Read(body)
		if checkReq != nil {
			checkReq(r, body[:n])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestStreamCoreForwardsUsageChunk(t *testing.T) {
	usageJSON := `{"prompt_tokens":100,"completion_tokens":42,"total_tokens":142,"cached_tokens":60}`
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"usage":` + usageJSON + `}`,
	}
	srv := mockStreamServer(t, chunks, nil)
	defer srv.Close()

	o := NewOpenAI(srv.URL, "k", "test-model")
	var got *agent.Usage
	var mu sync.Mutex
	nginx := func(d agent.StreamDelta) {
		if d.Usage != nil {
			mu.Lock()
			got = d.Usage
			mu.Unlock()
		}
	}
	_, err := o.ChatWithToolsStreamCtx(context.Background(), []agent.Message{{Role: "user", Content: "hi"}}, nil, nginx)
	if err != nil {
		t.Fatalf("stream err: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("expected usage forwarded via onToken, got nil")
	}
	if got.CachedTokens != 60 || got.CompletionTokens != 42 || got.PromptTokens != 100 {
		t.Errorf("usage = %+v, want cached=60 completion=42 prompt=100", got)
	}
	// 流内 usage 也应同步到 lastUsage
	if last := o.LastUsage(); last == nil || last.TotalTokens != 142 {
		t.Errorf("LastUsage = %+v, want total=142", last)
	}
}

func TestStreamOptionsRejectedThenDowngrade(t *testing.T) {
	// 第一个请求带 stream_options → 400（明确提示 unknown parameter）
	// 降级后（不带 stream_options）应重试成功，且 delta 正常回调
	var mu sync.Mutex
	calls := []string{}
	usageJSON := `{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1<<20)
		n, _ := r.Body.Read(body)
		mu.Lock()
		calls = append(calls, string(body[:n]))
		mu.Unlock()
		if len(calls) == 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'stream_options'"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"usage\":%s}\n\n", usageJSON)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	o := NewOpenAI(srv.URL, "k", "test-model")
	var got string
	var usages []int
	nginx := func(d agent.StreamDelta) {
		got += d.Content
		if d.Usage != nil {
			usages = append(usages, d.Usage.TotalTokens)
		}
	}
	reply, err := o.ChatWithToolsStream([]agent.Message{{Role: "user", Content: "hi"}}, nil, nginx)
	if err != nil {
		t.Fatalf("chat err: %v", err)
	}
	if !strings.Contains(reply, "ok") || got != "ok" {
		t.Errorf("reply=%q stream=%q, want contains ok", reply, got)
	}
	if len(usages) != 1 || usages[0] != 12 {
		t.Errorf("stream usages = %v, want [12]", usages)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 http calls (with + without stream_options), got %d", len(calls))
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(calls[0]), &first)
	_ = json.Unmarshal([]byte(calls[1]), &second)
	if _, ok := first["stream_options"]; !ok {
		t.Errorf("first request should carry stream_options: %s", calls[0])
	}
	if _, ok := second["stream_options"]; ok {
		t.Errorf("second request should drop stream_options: %s", calls[1])
	}
}
