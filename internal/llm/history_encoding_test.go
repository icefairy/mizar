package llm

import (
	"encoding/json"
	"testing"

	"mizar/internal/agent"
)

// 核心不变式：工具交换历史必须以 OpenAI **原生字段**发给 LLM。
//
// 旧实现把一切压成 chatMsg{Role, Content}，于是：
//
//	assistant content={"action":"tool",...}   ← 文本，不是 tool_calls
//	user      content="工具结果: {...}"         ← 文本，不是 role:"tool"
//
// 模型看到的历史是「有人在念 JSON」，会模仿这种模式——用文字描述而不发
// tool_call（实测长会话后 finish="stop" toolCalls=0、内容里写 <tool name=...>）。
// 这是「发一条『继续』就结束」的根本原因之一。
func TestEncodeMessagesUsesNativeToolFields(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "帮我看看目录"},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "ls", ToolArgs: `{"path":"."}`,
			Content:   `{"action":"tool","tool":"ls","args":"{\"path\":\".\"}"}`,
			ToolCalls: []agent.ToolCallRef{{ID: "call_abc", Name: "ls", Args: `{"path":"."}`}}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "call_abc",
			Content: `工具结果: {"tool_name":"ls","args":"{\"path\":\".\"}","output":"3 entries"}`},
		{Role: agent.RoleAssistant, Content: "目录里有 3 个文件"},
	}

	got := encodeMessages(msgs)
	if len(got) != 4 {
		t.Fatalf("应编码为 4 条, got %d", len(got))
	}

	// [1] assistant 必须带 tool_calls，且 content 不能是控制 JSON
	c1 := got[1]
	if c1.Role != "assistant" {
		t.Fatalf("[1] role 应为 assistant, got %q", c1.Role)
	}
	if len(c1.ToolCalls) != 1 {
		t.Fatalf("[1] 应带 1 个 tool_calls, got %d", len(c1.ToolCalls))
	}
	if c1.ToolCalls[0].ID != "call_abc" || c1.ToolCalls[0].Function.Name != "ls" {
		t.Fatalf("[1] tool_calls 内容不对: %+v", c1.ToolCalls[0])
	}
	if s, _ := c1.Content.(string); s != "" {
		t.Fatalf("[1] content 不应回灌控制 JSON, got %q", s)
	}

	// [2] 工具结果必须是 role:"tool" + tool_call_id
	c2 := got[2]
	if c2.Role != "tool" {
		t.Fatalf("[2] role 应为 tool, got %q", c2.Role)
	}
	if c2.ToolCallID != "call_abc" {
		t.Fatalf("[2] tool_call_id 应为 call_abc, got %q", c2.ToolCallID)
	}
	if s, _ := c2.Content.(string); s != "3 entries" {
		t.Fatalf("[2] content 应只含结果正文, got %q", s)
	}
}

// 序列化后的 JSON 必须真的是 OpenAI 期望的形状（字段名不能错）。
func TestEncodedJSONShape(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			ToolCalls: []agent.ToolCallRef{{ID: "c1", Name: "read", Args: `{"path":"x"}`}}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "c1", Content: "工具结果: ok"},
	}
	b, err := json.Marshal(encodeMessages(msgs))
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	// assistant: tool_calls[].function.{name,arguments}
	tcs, ok := raw[0]["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("assistant 应有 tool_calls 数组: %s", b)
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "c1" || tc["type"] != "function" {
		t.Fatalf("tool_calls 元素应有 id/type: %v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "read" || fn["arguments"] != `{"path":"x"}` {
		t.Fatalf("function 应有 name/arguments: %v", fn)
	}
	// tool 消息: role=tool + tool_call_id
	if raw[1]["role"] != "tool" || raw[1]["tool_call_id"] != "c1" {
		t.Fatalf("工具结果应为 role=tool + tool_call_id: %v", raw[1])
	}
}

// 旧会话（无 ToolCalls 字段）必须能回退：从 ToolName/ToolArgs 还原，
// 保证历史会话仍可用，不会因为缺字段而丢上下文。
// 结果侧同样无 ToolCallID（旧格式），按出现顺序回退配对。
func TestEncodeMessagesFallbackForLegacySession(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash", ToolArgs: `{"command":"ls"}`,
			Content: `{"action":"tool","tool":"bash","args":"{\"command\":\"ls\"}"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult,
			Content: `工具结果: {"tool_name":"bash","output":"a b"}`},
		// 旧数据可能连 ToolName 都没有，只有 Content 里的 JSON
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			Content: `{"action":"tool","tool":"read","args":"{\"path\":\"y\"}"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult,
			Content: `工具结果: {"tool_name":"read","output":"body"}`},
	}
	got := encodeMessages(msgs)
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("应从 ToolName/ToolArgs 回退: %+v", got[0].ToolCalls)
	}
	if got[0].ToolCalls[0].ID == "" {
		t.Fatal("回退生成的调用必须有合成 ID（供 tool 结果配对）")
	}
	if got[1].Role != "tool" || got[1].ToolCallID != got[0].ToolCalls[0].ID {
		t.Fatalf("旧格式结果应按顺序配对回退: %+v", got[1])
	}
	if len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].Function.Name != "read" {
		t.Fatalf("应从 Content JSON 回退: %+v", got[2].ToolCalls)
	}
	if got[3].Role != "tool" || got[3].ToolCallID != got[2].ToolCalls[0].ID {
		t.Fatalf("第二条旧格式结果也应配对: %+v", got[3])
	}
}

// 普通对话消息不受影响（不能被工具编码误伤）。
func TestEncodeMessagesPlainUnchanged(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "sys"},
		{Role: agent.RoleUser, Content: "hi"},
		{Role: agent.RoleAssistant, Content: "hello"},
	}
	got := encodeMessages(msgs)
	if len(got) != 3 {
		t.Fatalf("应 3 条, got %d", len(got))
	}
	for i, w := range []string{"system", "user", "assistant"} {
		if got[i].Role != w {
			t.Fatalf("[%d] role 应为 %q, got %q", i, w, got[i].Role)
		}
		if got[i].ToolCalls != nil || got[i].ToolCallID != "" {
			t.Fatalf("[%d] 普通消息不应带工具字段: %+v", i, got[i])
		}
	}
}

// 孤儿工具结果（历史被截断，开头正好落在一条工具结果上）不能被编码为
// role:"tool"——OpenAI 要求它必须回应前面的 tool_calls，否则整个请求 400。
// 实测：-history 50 截断后的真实会话，第一条就是孤儿 tool 消息。
func TestEncodeMessagesOrphanToolResultDowngraded(t *testing.T) {
	msgs := []agent.Message{
		// 历史开头就是一条工具结果（其对应的 tool_call 已被截掉）
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "call_missing",
			Content: `工具结果: {"tool_name":"ls","output":"orphan"}`},
		{Role: agent.RoleAssistant, Content: "继续"},
	}
	got := encodeMessages(msgs)
	if got[0].Role == "tool" {
		t.Fatalf("孤儿工具结果不得编码为 role=tool（会 400）, got %+v", got[0])
	}
	if got[0].Role != agent.RoleUser {
		t.Fatalf("孤儿结果应降级为 user, got %q", got[0].Role)
	}
	s, _ := got[0].Content.(string)
	if s != "orphan" {
		t.Fatalf("降级后应保留结果正文, got %q", s)
	}
}

// 正常配对：tool_call 之后的结果必须仍然是 role:"tool" + 正确 id。
func TestEncodeMessagesPairedToolResultStaysNative(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			ToolCalls: []agent.ToolCallRef{{ID: "c9", Name: "read", Args: `{}`}}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "c9", Content: "工具结果: done"},
	}
	got := encodeMessages(msgs)
	if got[1].Role != "tool" || got[1].ToolCallID != "c9" {
		t.Fatalf("配对结果应保持原生形态, got %+v", got[1])
	}
}

// 同一 ID 只能配对一次：第二次出现应降级（不能重复回应同一 tool_call）。
func TestEncodeMessagesDuplicateResultDowngraded(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			ToolCalls: []agent.ToolCallRef{{ID: "c1", Name: "a", Args: `{}`}}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "c1", Content: "工具结果: first"},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "c1", Content: "工具结果: second"},
	}
	got := encodeMessages(msgs)
	if got[1].Role != "tool" {
		t.Fatalf("首个应配对, got %q", got[1].Role)
	}
	if got[2].Role != "tool" && got[2].Role != agent.RoleUser {
		t.Fatalf("重复结果应避免二次回应, got %q", got[2].Role)
	}
	if got[2].Role == "tool" {
		t.Fatal("同一 tool_call_id 不应被回应两次（OpenAI 会拒绝）")
	}
}
