package agent

import (
	"strings"
	"testing"
)

// 端到端核对最终系统提示里两段关键约束都在（防止被后续改动误删）。
func TestReplyFormatSectionHasActionVsAnnounceRules(t *testing.T) {
	a, _, _ := newTestAgent()
	p := a.SystemPrompt()
	for _, want := range []string{
		"## 边做边说",
		"同一轮发出",
		"## 说了就要做",
		"不要只“宣布”而不执行",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("系统提示缺少 %q", want)
		}
	}
	// 旧措辞「汇报进展」已从「宣布任务完成」同句中移除，避免二者被当作同一出口
	if strings.Contains(p, "汇报进展/宣布任务完成") {
		t.Fatal("「汇报进展」不应再与「宣布任务完成」并列为同一出口")
	}
}
