package agent

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// GAP-S5 per-subagent token 计量：流式 usage 分片累加进句柄。

// usageStreamLLM 在 scriptedLLM 之上实现 StreamToolCallLLM：每轮发一个 usage 分片。
type usageStreamLLM struct {
	scriptedLLM
	usagePerCall Usage
}

func (u *usageStreamLLM) ChatWithToolsStream(msgs []Message, tools []plugins.Tool, onToken func(StreamDelta)) (string, error) {
	// 记录快照（复用 scriptedLLM 的 gotTool/gotMsgs 记账）
	_, _ = u.scriptedLLM.ChatWithTools(msgs, tools)
	onToken(StreamDelta{Usage: &Usage{
		PromptTokens:     u.usagePerCall.PromptTokens,
		CompletionTokens: u.usagePerCall.CompletionTokens,
	}})
	u.mu.Lock()
	i := u.calls - 1
	u.mu.Unlock()
	if i < len(u.script) {
		return u.script[i], nil
	}
	return `{"action":"reply","text":"done"}`, nil
}

func TestSubagent_TokenMetering(t *testing.T) {
	// 两轮脚本：第一轮调工具，第二轮交付 —— usage 按轮累加
	llm := &usageStreamLLM{usagePerCall: Usage{PromptTokens: 100, CompletionTokens: 10}}
	llm.script = []string{
		`{"action":"tool","tool":"ping","args":"{}"}`,
		`{"action":"reply","text":"ok"}`,
	}
	sm := newTestManager(llm, nil)
	h, err := sm.Launch(SubagentLaunchRequest{Goal: "计量验证"})
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(sm, h.ID, 3*time.Second); st != SubagentSucceeded {
		t.Fatalf("子 agent 应成功: %s", st)
	}
	got, _ := sm.Get(h.ID)
	if got.TokensIn != 200 || got.TokensOut != 20 {
		t.Fatalf("两轮 usage 应累加 in=200 out=20，实际 in=%d out=%d", got.TokensIn, got.TokensOut)
	}
}

func TestSubagent_TokenMeteringNoUsage(t *testing.T) {
	// 不发 usage 分片的 LLM：计量保持 0，不影响主流程
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	h, err := sm.Launch(SubagentLaunchRequest{Goal: "无 usage"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(sm, h.ID, 2*time.Second)
	got, _ := sm.Get(h.ID)
	if got.TokensIn != 0 || got.TokensOut != 0 {
		t.Fatalf("无 usage 分片应保持 0: in=%d out=%d", got.TokensIn, got.TokensOut)
	}
	// /subagents 人类渲染不应带 tokens 段
	if r := sm.RenderSubagentsForHuman(); strings.Contains(r, "tokens:") {
		t.Fatalf("零计量不应显示 tokens 段: %s", r)
	}
}
