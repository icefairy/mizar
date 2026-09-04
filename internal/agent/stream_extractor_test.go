package agent

import (
	"testing"
)

// TestStreamExtractor_ToolIntro 模型先说话再调工具：说明文字应被捕获为 ToolIntro。
func TestStreamExtractor_ToolIntro(t *testing.T) {
	var e StreamTextExtractor
	// 流式拼入前导说明文字（分片，模拟逐 token）
	chunks := []string{"好的", "，让我", "先", "检测", "系统环境", "\n"}
	for _, c := range chunks {
		if got := e.Feed(c); got != c {
			t.Fatalf("intro delta %q = %q, want %q", c, got, c)
		}
	}
	// 此时 action 未确认（无 { 开头），intro 已累积
	if e.IsTool() {
		t.Fatal("IsTool should be false before tool JSON arrives")
	}
	// 工具 JSON 到达：后续不透传，intro 定格
	toolJSON := `{"action":"tool","tool":"bash","args":"{\"command\":\"hostname\"}"}`
	if got := e.Feed(toolJSON); got != "" {
		t.Fatalf("tool JSON delta = %q, want empty (control text not shown)", got)
	}
	if !e.IsTool() {
		t.Fatal("IsTool should be true after tool JSON")
	}
	want := "好的，让我先检测系统环境"
	if got := e.ToolIntro(); got != want {
		t.Fatalf("ToolIntro = %q, want %q", got, want)
	}
}

// TestStreamExtractor_ToolNoIntro 模型直接调工具（不先说话）：intro 为空，不显示。
func TestStreamExtractor_ToolNoIntro(t *testing.T) {
	var e StreamTextExtractor
	// 直接来工具 JSON（流式前缀 { 开头的控制文本不透传）
	if got := e.Feed(`{"action":`); got != "" {
		t.Fatalf("json-prefixed delta = %q, want empty", got)
	}
	if got := e.Feed(`"tool","tool":"ls","args":"{}"}`); got != "" {
		t.Fatalf("tool tail delta = %q, want empty", got)
	}
	if !e.IsTool() {
		t.Fatal("IsTool should be true")
	}
	if got := e.ToolIntro(); got != "" {
		t.Fatalf("ToolIntro = %q, want empty", got)
	}
}

// TestStreamExtractor_ReplyStreaming 纯 reply 场景：JSON 以 { 开头整体流式到达，text 字段被提取透传。
// 将整段控制 JSON 分 3 片喂入（模拟常见 SSE 分片粒度），断言累计可显示文本为 reply 的 text 值。
func TestStreamExtractor_ReplyStreaming(t *testing.T) {
	var e StreamTextExtractor
	tokens := []string{`{"action":"reply","text":"已`, `经查`, `清楚了"}`}
	var out string
	for _, tk := range tokens {
		out += e.Feed(tk)
	}
	if e.IsTool() {
		t.Fatal("reply should not be tool")
	}
	if !e.Replying() {
		t.Fatal("reply should be replying")
	}
	if got := out; got != "已经查清楚了" {
		t.Fatalf("streamed text = %q, want 已经查清楚了", got)
	}
}

// TestStreamExtractor_Reset 步骤切换后状态清空。
func TestStreamExtractor_Reset(t *testing.T) {
	var e StreamTextExtractor
	e.Feed("先说说")
	e.Feed(`{"action":"tool","tool":"x","args":""}`)
	if !e.IsTool() || e.ToolIntro() == "" {
		t.Fatalf("precondition: tool+intro expected, got tool=%v intro=%q", e.IsTool(), e.ToolIntro())
	}
	e.Reset()
	if e.IsTool() || e.ToolIntro() != "" || e.Text() != "" || e.Replying() {
		t.Fatalf("Reset should clear all state")
	}
}

// TestRunUnlimitedSteps 无限模式（MaxSteps=-1）下：循环条件永真，agent 能正常走到 reply 结束，
// 不会因“step < -1”一次都不进而误触步数耗尽错误（回归 guard：旧逻辑 <=0 会把 -1 改写回默认 60）。
func TestRunUnlimitedSteps(t *testing.T) {
	a, _, _ := newTestAgent(`{"action":"reply","text":"完成"}`)
	a.MaxSteps = -1 // 无限
	reply, err := a.Run("任务")
	if err != nil {
		t.Fatalf("unlimited run err = %v", err)
	}
	if reply != "完成" {
		t.Fatalf("reply = %q", reply)
	}
}
