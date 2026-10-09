package agent

import (
	"strings"
	"testing"
)

// 压缩后不应插入空 system 占位消息（白占 prompt 预算且干扰缓存前缀）。
func TestCompactNoEmptySystemPlaceholder(t *testing.T) {
	c := &Compactor{
		KeepRecentTokens: 10,
		Summarize:        func([]Message) (string, error) { return "SUMMARY", nil },
	}
	msgs := []Message{
		{Role: RoleUser, Content: strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("b", 400)},
		{Role: RoleUser, Content: "最近一条"},
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for i, m := range out {
		if m.Role == RoleSystem {
			t.Fatalf("无 system 输入时不应插入 system 占位（第 %d 条）: %+v", i, m)
		}
	}
	// 摘要仍必须存在
	if out[0].Kind != KindSummary {
		t.Fatalf("摘要应为首条，得到 kind=%q", out[0].Kind)
	}
}

// 有 system 时仍保留原 system，且不重复插入。
func TestCompactKeepsExistingSystem(t *testing.T) {
	c := &Compactor{
		KeepRecentTokens: 10,
		Summarize:        func([]Message) (string, error) { return "SUMMARY", nil },
	}
	msgs := []Message{
		{Role: RoleSystem, Content: "SYSPROMPT"},
		{Role: RoleUser, Content: strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("b", 400)},
		{Role: RoleUser, Content: "最近一条"},
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	sysCount := 0
	for _, m := range out {
		if m.Role == RoleSystem {
			sysCount++
			if m.Content != "SYSPROMPT" {
				t.Fatalf("system 内容被改动: %q", m.Content)
			}
		}
	}
	if sysCount != 1 {
		t.Fatalf("应恰好一个 system，得到 %d", sysCount)
	}
	if out[0].Role != RoleSystem || out[1].Kind != KindSummary {
		t.Fatalf("顺序应为 system → summary，得到 %q/%q", out[0].Role, out[1].Kind)
	}
}

// split user span：单个 user span 超预算时，切点落在 span 内部，
// 该 span 的前半部分需要额外生成一份"span 前缀摘要"，与历史摘要合并。
func TestCompactSplitUserSpanGeneratesPrefixSummary(t *testing.T) {
	var calls []string
	c := &Compactor{
		KeepRecentTokens: 100,
		Summarize: func(msgs []Message) (string, error) {
			// 记录每次被摘要的消息，用于断言调用了两次
			var parts []string
			for _, m := range msgs {
				parts = append(parts, m.Role+":"+truncateRunes(m.Content, 8))
			}
			calls = append(calls, strings.Join(parts, "|"))
			return "SUMMARY", nil
		},
	}
	big := strings.Repeat("x", 4000) // 约 1000 token，远超 100 预算
	msgs := []Message{
		{Role: RoleUser, Content: big},
		{Role: RoleAssistant, Content: big},
		{Role: RoleAssistant, Content: big},
		{Role: RoleUser, Content: "第二个问题"},
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 切点落在 span 内部 → 两次摘要调用（历史摘要 + span 前缀摘要）
	if len(calls) < 2 {
		t.Fatalf("split span 应触发两次摘要（历史 + span 前缀），实际 %d 次: %v", len(calls), calls)
	}
	// 合并后的摘要应包含两份内容标记
	var sum Message
	for _, m := range out {
		if m.Kind == KindSummary {
			sum = m
			break
		}
	}
	if sum.Kind != KindSummary {
		t.Fatalf("未找到摘要消息: %+v", out)
	}
	if !strings.Contains(sum.Content, "SUMMARY") {
		t.Fatalf("摘要内容缺失: %q", sum.Content)
	}
}

// 非 split 情况（切点在 user 边界）只调用一次摘要，不应引入额外开销。
func TestCompactNormalBoundarySingleSummary(t *testing.T) {
	calls := 0
	c := &Compactor{
		KeepRecentTokens: 10,
		Summarize: func([]Message) (string, error) {
			calls++
			return "SUMMARY", nil
		},
	}
	msgs := []Message{
		{Role: RoleUser, Content: strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: "收到"},
		{Role: RoleUser, Content: strings.Repeat("c", 400)},
		{Role: RoleAssistant, Content: "好的"},
	}
	if _, err := c.Compact(msgs); err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("普通边界应只摘要一次，实际 %d 次", calls)
	}
}

// IsSplitUserSpan：判定切点是否落在某个 user span 内部。
func TestIsSplitUserSpan(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleUser, Content: "u2"},
	}
	// cut=2：切在 assistant 上，而它属于 u1 这个 span → split
	if !isSplitUserSpan(msgs, 2) {
		t.Fatal("cut=2 应判定为 split user span")
	}
	// cut=3：正好切在 u2 这个 user 消息上 → 不是 split
	if isSplitUserSpan(msgs, 3) {
		t.Fatal("cut=3 是 user 边界，不应判定为 split")
	}
	// cut=0：无前置内容 → 不是 split
	if isSplitUserSpan(msgs, 0) {
		t.Fatal("cut=0 不应判定为 split")
	}
}
