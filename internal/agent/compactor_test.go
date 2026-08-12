package agent

import (
	"strings"
	"testing"
)

// buildMsgs 构造测试消息流。
func buildMsgs(n int) []Message {
	var msgs []Message
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			Message{Role: RoleUser, Content: "用户问题 " + strings.Repeat("中", 20) + " " + string(rune('a'+i%26)) + strings.Repeat("x", 40)},
			Message{Role: RoleAssistant, Content: "{\"action\":\"tool\",\"tool\":\"calc\",\"args\":\"1+1\"}", Kind: KindToolCall},
			Message{Role: RoleUser, Content: "工具结果: {\"tool_name\":\"calc\",\"output\":\"2\"}", Kind: KindToolResult},
		)
	}
	return msgs
}

func TestEstimateTokens(t *testing.T) {
	// 中文按字符 1 token，英文按 4 字符 1 token
	m := Message{Role: RoleUser, Content: "你好世界"}
	if got := EstimateTokens(m); got != 4 {
		t.Errorf("EstimateTokens(你好世界) = %d, want 4", got)
	}
	m2 := Message{Role: RoleUser, Content: "abc"}
	if got := EstimateTokens(m2); got != 1 {
		t.Errorf("EstimateTokens(abc) = %d, want 1", got)
	}
}

func TestFindCutPointNeverCutsToolResult(t *testing.T) {
	c := DefaultCompactor(func([]Message) (string, error) { return "s", nil })
	c.KeepRecentTokens = 100 // 很小，强制切点往前
	msgs := buildMsgs(10)
	cut := c.FindCutPoint(msgs)
	if cut < 0 {
		t.Fatalf("expected a cut point")
	}
	// 切点处必须是合法切点（不能是 tool_result / summary）
	if !msgs[cut].IsCutPoint() {
		t.Errorf("cut index %d is not a cut point: %+v", cut, msgs[cut])
	}
	// 从切点往后，tool_result 必须紧跟在它的 tool_call 后（配对完整性）
	for i := cut; i < len(msgs); i++ {
		if msgs[i].Kind == KindToolResult {
			if i-1 < cut || msgs[i-1].Kind != KindToolCall {
				t.Errorf("broken tool pair at %d: tool_result without preceding tool_call", i)
			}
		}
	}
}

func TestFindCutPointRespectsBudget(t *testing.T) {
	c := DefaultCompactor(func([]Message) (string, error) { return "s", nil })
	msgs := buildMsgs(50) // 每 3 条约 65+ tokens，50 组 ≈ 3200
	c.KeepRecentTokens = 300
	cut := c.FindCutPoint(msgs)
	if cut < 0 {
		t.Fatal("expected cut")
	}
	kept := EstimateMessages(msgs[cut:])
	if kept > 300*2 { // 允许一定误差（切点粒度）
		t.Errorf("kept tokens %d exceeds budget*2", kept)
	}
}

func TestCompactInsertsSummaryAfterSystem(t *testing.T) {
	var summarized []Message
	c := DefaultCompactor(func(msgs []Message) (string, error) {
		summarized = msgs
		return "## Goal\n测试", nil
	})
	c.KeepRecentTokens = 100
	msgs := append([]Message{{Role: RoleSystem, Content: "sys"}}, buildMsgs(8)...)
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatal(err)
	}
	// 结构: [system, summary, keep...]
	if out[0].Role != RoleSystem {
		t.Errorf("out[0] should be system")
	}
	if out[1].Kind != KindSummary || !strings.Contains(out[1].Content, "Goal") {
		t.Errorf("out[1] should be summary, got %+v", out[1])
	}
	if len(summarized) == 0 {
		t.Error("summarize should have been called")
	}
	// out = [system, summary, keep...]；keep 从切点开始
	cut := c.FindCutPoint(msgs)
	want := 2 + (len(msgs) - cut)
	if len(out) != want {
		t.Errorf("length mismatch: out=%d want=%d (cut=%d)", len(out), want, cut)
	}
}

func TestCompactNoSummarizeWhenUnderBudget(t *testing.T) {
	c := DefaultCompactor(func([]Message) (string, error) { return "s", nil })
	c.ContextWindow = 1000000
	msgs := buildMsgs(5)
	if c.ShouldCompact(EstimateMessages(msgs)) {
		t.Error("should not compact under budget")
	}
	out, err := c.Compact(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(msgs) {
		t.Errorf("no-op compact should return same length")
	}
}

func TestCompactorNil(t *testing.T) {
	var c *Compactor
	msgs := buildMsgs(3)
	if c.ShouldCompact(100) {
		t.Error("nil compactor never compacts")
	}
	out, err := c.Compact(msgs)
	if err != nil || len(out) != len(msgs) {
		t.Error("nil compact no-op")
	}
}
