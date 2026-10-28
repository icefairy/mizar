package agent

import (
	"strings"
	"testing"
)

// TestCompactReportsNoChange 切点不可用时必须让调用方知道「没压缩」，
// 而不是返回原样却让上层误判成功（曾出现日志 result 与实际 token 不变）。
func TestCompactReportsNoChange(t *testing.T) {
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		return "摘要", nil
	})
	c.KeepRecentTokens = 100000 // 预算极大 → 全部内容都在预算内 → 无切点

	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatal(err)
	}
	// 未压缩时应原样返回
	if len(out) != len(msgs) {
		t.Fatalf("无切点时应原样返回，实际 %d -> %d", len(msgs), len(out))
	}
	// 且结果里不应有 summary
	for _, m := range out {
		if m.Kind == KindSummary {
			t.Error("未实际压缩却产生了 summary")
		}
	}
}

// TestCompactRejectsEmptySummary 摘要为空时必须报错，不能写出空摘要的压缩记录。
//
// 空摘要在会话文件里等价于「静默丢弃前面所有消息」，比不压缩更糟。
func TestCompactRejectsEmptySummary(t *testing.T) {
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		return "   ", nil // 空白摘要
	})
	c.KeepRecentTokens = 10 // 小预算 → 必定有切点

	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("b", 400)},
		{Role: RoleUser, Content: strings.Repeat("c", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("d", 400)},
	}
	_, err := c.Compact(msgs)
	if err == nil {
		t.Fatal("摘要为空时应返回错误，而不是写出空摘要")
	}
	if !strings.Contains(err.Error(), "摘要") && !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "summary") {
		t.Errorf("错误信息应说明摘要为空，实际: %v", err)
	}
}

// TestCompactNormalizesSummaryWhitespace 正常摘要应去掉首尾空白后保留。
func TestCompactNormalizesSummaryWhitespace(t *testing.T) {
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		return "  \n## Goal\n做点事\n  ", nil
	})
	c.KeepRecentTokens = 10
	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("b", 400)},
		{Role: RoleUser, Content: strings.Repeat("c", 400)},
		{Role: RoleAssistant, Content: strings.Repeat("d", 400)},
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatal(err)
	}
	var sum string
	for _, m := range out {
		if m.Kind == KindSummary {
			sum = m.Content
		}
	}
	if sum == "" {
		t.Fatal("应有摘要消息")
	}
	if strings.HasPrefix(sum, " ") || strings.HasPrefix(sum, "\n") || strings.HasSuffix(sum, " ") {
		t.Errorf("摘要首尾空白未清理: %q", sum)
	}
}
