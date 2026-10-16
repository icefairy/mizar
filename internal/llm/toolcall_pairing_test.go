package llm

import (
	"encoding/json"
	"testing"

	"mizar/internal/agent"
)

// 【复现 2026-10-16 线上 400】工具调用配对断裂。
//
// 现场：mizar 走璇玑网关调 codebuddy，长会话第 32 步起连续 400
//
//	{"code":11148,"msg":"tool calls and tool results do not match"}
//
// 根因（agent/loop.go runOneToolCall 的两条早退分支）：
//
//	空参数兜底 / respond 缺参数时，循环注入一条
//	  {Role:assistant, Kind:KindToolCall, Content:<原始回复>}
//	——标了 KindToolCall 却**没有 ToolCalls 字段**，且下一条是普通 user 纠正文本，
//	没有 role:"tool" 结果。encodeMessages 见 KindToolCall 就走回退逻辑，
//	从 Content 里还原出一个 tool_call（并合成 ID）→ 发出「有 tool_calls 无结果」
//	的非法序列 → 上游 400。
//
// 不变式：**凡输出 tool_calls 的 assistant 消息，后续必须有 role:"tool" 回应**。
// 纯文本 assistant（含纠正提示场景）不得携带 tool_calls。
func TestEncodeMessagesNeverEmitsUnpairedToolCalls(t *testing.T) {
	cases := []struct {
		name string
		msgs []agent.Message
	}{
		{
			name: "空参数兜底：assistant 标 KindToolCall 但无 ToolCalls 字段",
			msgs: []agent.Message{
				{Role: agent.RoleUser, Content: "搜索一下"},
				{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
					Content: `{"action":"tool","calls":[{"tool":"anysearch","args":"","id":"call_x"}],"tool":"anysearch","args":"","callId":"call_x"}`},
				{Role: agent.RoleUser, Content: "⚠️ 工具 anysearch 调用缺少参数（args 为空）。请重新调用，并在 args 中传入正确的 JSON 参数。"},
			},
		},
		{
			name: "respond 缺参数兜底：assistant 标 KindToolCall 但无 ToolCalls 字段",
			msgs: []agent.Message{
				{Role: agent.RoleUser, Content: "回答我"},
				{Role: agent.RoleAssistant, Kind: agent.KindToolCall,
					Content: `{"action":"tool","tool":"respond","args":"{}"}`},
				{Role: agent.RoleUser, Content: "⚠️ respond 工具调用缺少 text 参数。请重新调用 respond。"},
			},
		},
		{
			name: "旧会话：tool_call 之后紧跟另一条 assistant（结果从未落盘）",
			msgs: []agent.Message{
				{Role: agent.RoleUser, Content: "跑一下"},
				{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash", ToolArgs: `{"command":"ls"}`,
					Content: `{"action":"tool","tool":"bash","args":"{\"command\":\"ls\"}"}`},
				{Role: agent.RoleAssistant, Content: "我看了一下，目录是空的"},
			},
		},
		{
			name: "历史尾部截断：最后一条是 tool_call，没有结果",
			msgs: []agent.Message{
				{Role: agent.RoleUser, Content: "继续"},
				{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "read", ToolArgs: `{"path":"a"}`,
					Content:   `{"action":"tool","tool":"read","args":"{\"path\":\"a\"}"}`,
					ToolCalls: []agent.ToolCallRef{{ID: "call_z", Name: "read", Args: `{"path":"a"}`}}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeMessages(tc.msgs)
			// 收集所有 tool_call id，以及所有 role:tool 的 tool_call_id
			open := map[string]bool{}
			for i, m := range got {
				for _, c := range m.ToolCalls {
					if open[c.ID] {
						t.Fatalf("[%d] tool_call id 重复: %s", i, c.ID)
					}
					open[c.ID] = true
				}
				if m.Role == "tool" {
					if m.ToolCallID == "" {
						t.Fatalf("[%d] role:tool 缺 tool_call_id", i)
					}
					if !open[m.ToolCallID] {
						t.Fatalf("[%d] role:tool 的 id=%s 没有对应的 tool_calls", i, m.ToolCallID)
					}
					delete(open, m.ToolCallID)
				}
			}
			if len(open) > 0 {
				b, _ := json.Marshal(got)
				t.Fatalf("发出 %d 个未被回应的 tool_calls（上游必然 400）: %v\nencoded=%s", len(open), open, b)
			}
		})
	}
}
