package agent

import (
	"strings"
	"testing"
)

// systemRoles 取出角色序列，便于断言「有且仅有一条 system 且在首位」。
func systemRoles(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

func countSystem(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == RoleSystem {
			n++
		}
	}
	return n
}

// 严格上游（vLLM）的硬约束：只能有一条 system，且必须在第 0 条。
// 这条测试是「离线内网直连 vLLM 新会话直接 400」问题的闸门。
func TestMergeSystemMessages(t *testing.T) {
	cases := []struct {
		name       string
		in         []Message
		wantRoles  []string
		wantSystem string // 期望合并后的 system 内容（空=不检查）
	}{
		{
			name:      "无 system 原样返回",
			in:        []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "yo"}},
			wantRoles: []string{RoleUser, RoleAssistant},
		},
		{
			name:       "唯一且首位 system 原样返回",
			in:         []Message{{Role: RoleSystem, Content: "SYS"}, {Role: RoleUser, Content: "hi"}},
			wantRoles:  []string{RoleSystem, RoleUser},
			wantSystem: "SYS",
		},
		{
			name: "两条 system（主提示 + 时间上下文）合并到首位",
			in: []Message{
				{Role: RoleSystem, Content: "主提示词"},
				{Role: RoleSystem, Content: "当前实时时间：..."},
				{Role: RoleUser, Content: "你好"},
			},
			wantRoles:  []string{RoleSystem, RoleUser},
			wantSystem: "主提示词\n\n当前实时时间：...",
		},
		{
			name: "三条 system（主提示 + 时间 + 技能）不丢内容",
			in: []Message{
				{Role: RoleSystem, Content: "S1"},
				{Role: RoleSystem, Content: "S2"},
				{Role: RoleSystem, Content: "S3"},
				{Role: RoleUser, Content: "u"},
			},
			wantRoles:  []string{RoleSystem, RoleUser},
			wantSystem: "S1\n\nS2\n\nS3",
		},
		{
			// 压缩摘要在历史中间插入时也是 RoleSystem，必须提上来，
			// 否则直连 vLLM 依旧 400。
			name: "历史中间的 system（压缩摘要）提到首位",
			in: []Message{
				{Role: RoleSystem, Content: "SYS"},
				{Role: RoleUser, Content: "u1"},
				{Role: RoleSystem, Content: "摘要"},
				{Role: RoleAssistant, Content: "a1"},
				{Role: RoleUser, Content: "u2"},
			},
			wantRoles:  []string{RoleSystem, RoleUser, RoleAssistant, RoleUser},
			wantSystem: "SYS\n\n摘要",
		},
		{
			// 空的 system 不该在合并结果里留下孤零零的分隔符
			name: "空 system 被跳过且不留分隔符",
			in: []Message{
				{Role: RoleSystem, Content: ""},
				{Role: RoleSystem, Content: "S"},
				{Role: RoleUser, Content: "u"},
			},
			wantRoles:  []string{RoleSystem, RoleUser},
			wantSystem: "S",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MergeSystemMessages(c.in)
			if n := countSystem(got); n > 1 {
				t.Fatalf("合并后仍有 %d 条 system（必须 ≤1）：%v", n, systemRoles(got))
			}
			if n := countSystem(got); n == 1 && got[0].Role != RoleSystem {
				t.Fatalf("system 不在首位：%v", systemRoles(got))
			}
			if roles := systemRoles(got); strings.Join(roles, ",") != strings.Join(c.wantRoles, ",") {
				t.Fatalf("角色序列不对：got %v want %v", roles, c.wantRoles)
			}
			if c.wantSystem != "" && got[0].Content != c.wantSystem {
				t.Fatalf("system 内容不对：\n got %q\nwant %q", got[0].Content, c.wantSystem)
			}
			// 幂等：再合并一次不能有任何变化
			again := MergeSystemMessages(got)
			if strings.Join(systemRoles(again), ",") != strings.Join(systemRoles(got), ",") ||
				again[0].Content != got[0].Content {
				t.Fatalf("MergeSystemMessages 不幂等：%v -> %v", systemRoles(got), systemRoles(again))
			}
		})
	}
}

// 合并不能破坏工具调用配对信息（Kind/ToolCallID 必须原样保留）。
func TestMergeSystemMessages_KeepsToolPairing(t *testing.T) {
	in := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleSystem, Content: "TIME"},
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, Kind: KindToolCall, ToolCalls: []ToolCallRef{{ID: "c1", Name: "bash"}}},
		{Role: RoleUser, Kind: KindToolResult, ToolCallID: "c1", Content: "ok"},
	}
	got := MergeSystemMessages(in)
	if len(got) != 4 {
		t.Fatalf("条数不对：got %d want 4", len(got))
	}
	if got[2].Kind != KindToolCall || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "c1" {
		t.Fatalf("工具调用元数据被破坏：%+v", got[2])
	}
	if got[3].Kind != KindToolResult || got[3].ToolCallID != "c1" {
		t.Fatalf("工具结果元数据被破坏：%+v", got[3])
	}
}