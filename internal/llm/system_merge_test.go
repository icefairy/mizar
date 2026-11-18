package llm

import (
	"testing"

	"mizar/internal/agent"
)

// 出口闸门：发给上游的消息里有且仅有一条 system，且在第一条。
//
// 起因：离线内网直连 vLLM 时，新会话第一句就 400
// 「System message must be at the beginning of the conversation (found at index 1)」——
// 主循环会并发注入「主提示词 + 实时时间 + 相关技能」，而 vLLM 只认第 0 条是 system。
// 这里断言编码层自己就能兜住，任何调用方（主循环/插件 ai_chat/子代理）拼出多条
// system 也不会把不合规的请求发出去。
func TestEncodeMessages_MergesSystemToSingleFirst(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "主提示词"},
		{Role: agent.RoleSystem, Content: "当前实时时间：2026-11-18 15:30"},
		{Role: agent.RoleSystem, Content: "【技能】bash 用法"},
		{Role: agent.RoleUser, Content: "你好"},
		{Role: agent.RoleAssistant, Content: "在的"},
	}
	out := encodeMessages(msgs)

	nSys := 0
	for i, m := range out {
		if m.Role == "system" {
			nSys++
			if i != 0 {
				t.Fatalf("system 出现在第 %d 条（必须在第 0 条）", i)
			}
		}
	}
	if nSys != 1 {
		t.Fatalf("system 条数 = %d，必须恰好 1 条", nSys)
	}
	if out[0].Content == nil {
		t.Fatalf("system 内容丢了")
	}
	want := "主提示词\n\n当前实时时间：2026-11-18 15:30\n\n【技能】bash 用法"
	if got, _ := out[0].Content.(string); got != want {
		t.Fatalf("system 合并内容不对：\n got %q\nwant %q", got, want)
	}
	// 非 system 消息顺序不变
	if out[1].Role != "user" || out[2].Role != "assistant" {
		t.Fatalf("非 system 消息顺序被改：%v/%v", out[1].Role, out[2].Role)
	}
}

// 历史中间的 system（压缩摘要）同样要被提上来——否则长会话压缩后又会 400。
func TestEncodeMessages_HoistsSystemFromHistory(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "SYS"},
		{Role: agent.RoleUser, Content: "u1"},
		{Role: agent.RoleSystem, Content: "摘要"}, // 压缩摘要落在历史中间
		{Role: agent.RoleAssistant, Content: "a1"},
	}
	out := encodeMessages(msgs)
	if len(out) == 0 || out[0].Role != "system" {
		t.Fatalf("首条不是 system：%+v", out)
	}
	if n := countRole(out, "system"); n != 1 {
		t.Fatalf("system 条数 = %d，必须 1", n)
	}
	if got, _ := out[0].Content.(string); got != "SYS\n\n摘要" {
		t.Fatalf("合并内容不对：%q", got)
	}
}

func countRole(msgs []chatMsg, role string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}