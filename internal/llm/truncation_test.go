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

// 本文件覆盖 finish_reason=length（max_tokens 截断）的两件事：
//  1. 方案一：把「被截断」这一事实作为模型可见前缀注入返回文本，
//     让模型下一轮知道该拆分/精简重发，而不是误以为自己在写坏 JSON。
//  2. 方案二：通过 LastTruncated() 旁路信号告知上层，供 UI 提示用户「回答被截断」。

// sseServer 返回一个流式假服务端：先吐 content 分片，再以给定 finish_reason 收尾。
func sseServer(t *testing.T, content, finishReason string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "%s\n\n", fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"content":%q}}]}`, content))
		fl.Flush()
		if finishReason != "" {
			fmt.Fprintf(w, "%s\n\n", fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, finishReason))
			fl.Flush()
		}
		fmt.Fprintf(w, "%s\n\n", `data: [DONE]`)
		fl.Flush()
	}))
}

// 方案一：流式 length 截断的纯文本回复，必须带模型可见的截断提示，
// 且仍然是可被 agent 循环消化的 reply 控制 JSON（守住既有解析不变量）。
func TestStreamLengthInjectsTruncationNotice(t *testing.T) {
	srv := sseServer(t, "我还没说完", "length")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, truncationMarker) {
		t.Fatalf("length 截断必须注入模型可见的截断提示, got %q", reply)
	}
	// 解析不变量：仍必须是含 action:"reply" 的 JSON，否则循环会白耗一轮
	if !strings.Contains(reply, `"action"`) || !strings.Contains(reply, `"reply"`) {
		t.Fatalf("注入提示后仍须是 reply 控制 JSON, got %q", reply)
	}
	if !strings.Contains(reply, "我还没说完") {
		t.Fatalf("原文必须保留, got %q", reply)
	}
}

// 方案一：非流式 length 同样注入。
func TestNonStreamLengthInjectsTruncationNotice(t *testing.T) {
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
	if !strings.Contains(reply, truncationMarker) {
		t.Fatalf("非流式 length 也必须注入截断提示, got %q", reply)
	}
	if !strings.Contains(reply, `"reply"`) || !strings.Contains(reply, "被截断的回答") {
		t.Fatalf("仍是 reply 控制 JSON 且保留原文, got %q", reply)
	}
}

// 正常 stop 不得注入任何提示（否则每条回答都带警告，噪音）。
func TestStopInjectsNoTruncationNotice(t *testing.T) {
	srv := sseServer(t, "正常回答", "stop")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, truncationMarker) {
		t.Fatalf("stop 不应注入截断提示, got %q", reply)
	}
	if reply != `{"action":"reply","text":"正常回答"}` {
		t.Fatalf("stop 的返回必须与既有行为完全一致, got %q", reply)
	}
}

// content_filter 不注入「精简重发」提示：那是内容策略拦截，
// 引导模型"拆分输出"是错误纠偏，会浪费一轮。
func TestContentFilterInjectsNoTruncationNotice(t *testing.T) {
	srv := sseServer(t, "部分内容", "content_filter")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, truncationMarker) {
		t.Fatalf("content_filter 不应注入截断提示, got %q", reply)
	}
}

// 安全底线：被截断的工具调用 JSON 绝不能被拼上提示后当成可执行调用。
// 它必须继续走「格式错误」纠正路径，即仍是 IsToolCallText 成立、抽取不到完整 JSON。
func TestTruncatedToolCallJSONStaysUnparseable(t *testing.T) {
	srv := sseServer(t, `{"action":"tool","tool":"write","args":"{\"path\":\"a.go\",\"content\":\"package main`, "length")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	reply, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agent.ExtractActionJSON(reply); ok {
		t.Fatalf("截断的工具调用不得被解析为可执行调用, got %q", reply)
	}
	if !agent.IsToolCallText(reply) {
		t.Fatalf("应仍被识别为（畸形的）工具调用文本，从而走纠正路径, got %q", reply)
	}
	// 模型必须知道是长度问题，而不是被告知"JSON 语法错"
	if !strings.Contains(reply, truncationMarker) {
		t.Fatalf("截断的工具调用也应带上长度原因提示, got %q", reply)
	}
}

// 方案二：LastTruncated 是读后即清的旁路信号，不得串到下一次调用。
func TestLastTruncatedResetAfterRead(t *testing.T) {
	srv := sseServer(t, "截断", "length")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	if client.LastTruncated() {
		t.Fatal("初次调用前不应有截断信号")
	}
	if _, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !client.LastTruncated() {
		t.Fatal("length 截断后 LastTruncated 应为 true")
	}
	if client.LastTruncated() {
		t.Fatal("LastTruncated 必须读后清零，否则会串到下一步误报")
	}
}

// 方案二：正常 stop 后不得置位截断信号。
func TestLastTruncatedFalseOnStop(t *testing.T) {
	srv := sseServer(t, "正常", "stop")
	defer srv.Close()

	client := NewOpenAI(srv.URL+"/v1", "", "test-model")
	if _, err := client.ChatWithToolsStreamCtx(context.Background(),
		[]agent.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.LastTruncated() {
		t.Fatal("stop 不应置位截断信号")
	}
}
