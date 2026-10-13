package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mizar/internal/agent"
)

// 复现「finish_reason 非 stop（如 length / 空）导致内容不包装，parseCallJSON 解析失败空转」。
// 期望：只要 agent 循环路径（wrapReply=true）拿到非空内容，就必须给出可被
// parseCallJSON 消化的控制协议 JSON。
func TestChatWithToolsStream_NonStopFinishReasonWrapsReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"我还没说完"}}]}`,
			// finish_reason=length：达到输出上限被截断，不是正常收尾
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			fl.Flush()
		}
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, `"action"`) || !strings.Contains(reply, `"reply"`) {
		t.Fatalf("非 stop 结束的流式回复应包装为 reply 控制 JSON, got %q", reply)
	}
	if !strings.Contains(reply, "我还没说完") {
		t.Fatalf("包装后应保留原始内容, got %q", reply)
	}
}

// finish_reason 缺失（收到 [DONE] 但服务端没给 finish_reason）是同类问题的最简形态。
func TestChatWithToolsStream_MissingFinishReasonWrapsReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "%s\n\n", `data: {"choices":[{"index":0,"delta":{"content":"中断前的回答"}}]}`)
		fl.Flush()
		fmt.Fprintf(w, "%s\n\n", `data: [DONE]`)
		fl.Flush()
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, `"reply"`) || !strings.Contains(reply, "中断前的回答") {
		t.Fatalf("缺 finish_reason 的流式回复应包装为 reply 控制 JSON, got %q", reply)
	}
}

// 正常 stop 的路径不能被新逻辑破坏（仍然是包装为 reply）。
func TestChatWithToolsStream_StopStillWrapsReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "%s\n\n", `data: {"choices":[{"index":0,"delta":{"content":"正常回答"}}]}`)
		fl.Flush()
		fmt.Fprintf(w, "%s\n\n", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fl.Flush()
		fmt.Fprintf(w, "%s\n\n", `data: [DONE]`)
		fl.Flush()
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, `"reply"`) || !strings.Contains(reply, "正常回答") {
		t.Fatalf("stop 的流式回复应包装为 reply 控制 JSON, got %q", reply)
	}
}

// 非流式路径对称缺陷：finish_reason=length 且内容是普通文本时，
// 旧实现直接返回原文，agent 循环的 parseCallJSON 认不出 → 注入「解析失败」提示空转。
func TestChatWithToolsCtx_NonStopFinishReasonWrapsReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"被截断的回答"},"finish_reason":"length"}]}`)
	}))
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, `"reply"`) || !strings.Contains(reply, "被截断的回答") {
		t.Fatalf("非 stop 的非流式回复应包装为 reply 控制 JSON, got %q", reply)
	}
}
