package llm

import (
	"testing"

	"mizar/internal/agent"
)

// 【复现 2026-10-30 线上 11133】tool_calls[].function.name 为空字符串。
//
// 璇玑网关侧实验确认：请求里只要有一条
//
//	{role:"assistant", content:"", tool_calls:[{function:{name:""}}]}
//	+ {role:"tool", tool_call_id:"call_1", content:"out"}
//
// 上游（workbuddy / stepfun / agnes）一律拒绝，code 11133。
// 根因在 mizar 侧：agent 循环在工具名为空时仍把
// ToolCallRef{ID, Name:"", ...} 写进历史，编码层又原样发给上游。
//
// 不变式：**任何发出的 tool_calls 元素都必须有非空 function.name**。
// 空名调用无法声明——它对应的结果退化为普通文本（信息不丢，请求合法）。
func TestEncodeMessagesNeverEmitsEmptyToolName(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hi"},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			ToolCalls: []agent.ToolCallRef{{ID: "call_1", Name: "", Args: ""}}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "call_1",
			Content: `工具结果: {"output":"out"}`},
		{Role: agent.RoleUser, Content: "hi"},
	}

	got := encodeMessages(msgs)
	for i, m := range got {
		for j, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				t.Fatalf("messages[%d].tool_calls[%d].function.name 为空串（上游会报 11133）: %+v", i, j, got)
			}
		}
	}
	// 空名调用被丢弃后，其结果不得以 role:"tool" 形式孤立存在
	for i, m := range got {
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			t.Fatalf("messages[%d] 是孤立 role:tool（对应调用已被丢弃）: %+v", i, got)
		}
	}
}

// 混合场景：同一消息里既有合法调用也有空名调用——只保留合法调用，
// 空名调用的结果降级为普通 user 文本。
func TestEncodeMessagesDropsOnlyEmptyNameCall(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "hi"},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
			ToolCalls: []agent.ToolCallRef{
				{ID: "call_bad", Name: "", Args: ""},
				{ID: "call_ok", Name: "bash", Args: `{"command":"ls"}`},
			}},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "call_bad", Content: `工具结果: {"output":"a"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "call_ok", Content: `工具结果: {"output":"b"}`},
	}

	got := encodeMessages(msgs)
	var names []string
	for _, m := range got {
		for _, tc := range m.ToolCalls {
			names = append(names, tc.Function.Name)
			if tc.Function.Name == "" {
				t.Fatalf("出现了空 name 的 tool_calls: %+v", got)
			}
		}
	}
	if len(names) != 1 || names[0] != "bash" {
		t.Fatalf("应只保留 1 个合法调用（bash）, got %v", names)
	}
	// call_bad 的结果不能被丢：必须降级为普通文本消息
	found := false
	for _, m := range got {
		if s, ok := m.Content.(string); ok && m.Role == "user" && s == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("空名调用的工具结果丢失了（应降级为普通文本）: %+v", got)
	}
}
