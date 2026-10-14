package agent

import (
	"errors"
	"strings"
	"testing"
)

// 模拟：本地估算严重低估（每消息只算 ~10 token），但服务端已报超限。
// 修复前：ShouldCompact(est) 返回 false → 不压缩 → 失败。
// 修复后：CompactForce 无条件按 keepTokens 切分压缩。
func TestCompactForce_BypassesEstimate(t *testing.T) {
	var summarized int
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		summarized++
		return "## Goal\n测试摘要", nil
	})
	c.ContextWindow = 128000
	c.ReserveTokens = 16384
	c.KeepRecentTokens = 20000

	// 构造：system + 20 轮 user/assistant，每条内容很短（本地估算远低于阈值）
	msgs := []Message{{Role: RoleSystem, Content: "sys"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: "问题"})
		msgs = append(msgs, Message{Role: RoleAssistant, Content: "回答"})
	}
	est := EstimateMessages(msgs)
	if c.ShouldCompact(est) {
		t.Fatalf("前置条件错误：本地估算不该触发压缩（est=%d），本测试模拟的正是这个场景", est)
	}

	out, err := c.CompactForce(msgs, c.KeepRecentTokens/2)
	if err != nil {
		t.Fatalf("CompactForce 失败: %v", err)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("强制压缩后消息数应减少: %d -> %d", len(msgs), len(out))
	}
	if summarized == 0 {
		t.Fatal("应调用摘要函数")
	}
	if out[0].Role != RoleSystem {
		t.Fatal("应保留 system 消息")
	}
	if out[1].Kind != KindSummary {
		t.Fatal("system 之后应是摘要消息")
	}
}

// 摘要调用失败时应降级截断重试，最终成功。
func TestSummarizeSafe_TrimRetry(t *testing.T) {
	calls := 0
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("llm status 400: maximum context length exceeded")
		}
		return "## Goal\n截断后摘要", nil
	})
	c.ContextWindow = 128000

	msgs := []Message{{Role: RoleSystem, Content: "sys"}}
	for i := 0; i < 50; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: strings.Repeat("长内容", 100)})
	}
	s, err := c.SummarizeSafe(msgs)
	if err != nil {
		t.Fatalf("SummarizeSafe 应降级重试成功: %v", err)
	}
	if !strings.Contains(s, "摘要") {
		t.Fatalf("摘要内容异常: %q", s)
	}
	if calls < 2 {
		t.Fatalf("应至少重试一次，实际调用 %d 次", calls)
	}
}

// 强制压缩失败时 hardTrimOnOverflow 仍应瘦身。
func TestHardTrimOnOverflow(t *testing.T) {
	msgs := []Message{{Role: RoleSystem, Content: "sys"}}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: strings.Repeat("内容", 200)})
	}
	out := hardTrimOnOverflow(msgs, 500)
	if len(out) >= len(msgs) {
		t.Fatalf("硬截断应减少消息: %d -> %d", len(msgs), len(out))
	}
	if out[0].Role != RoleSystem {
		t.Fatal("应保留 system")
	}
	if EstimateMessages(out) > 500+EstimateTokens(msgs[0])+400 {
		t.Fatalf("截断后应接近预算，实际 %d", EstimateMessages(out))
	}
}
