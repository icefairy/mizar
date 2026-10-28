package agent

import (
	"strings"
	"testing"
)

// mkMsgs 构造测试消息序列（不含 system）。
func mkMsgs(specs ...struct {
	role string
	n    int // 内容重复次数（每份约 4 字符 = 1 token）
}) []Message {
	var out []Message
	for _, s := range specs {
		out = append(out, Message{Role: s.role, Content: strings.Repeat("abcd", s.n)})
	}
	return out
}

func spec(role string, n int) struct {
	role string
	n    int
} {
	return struct {
		role string
		n    int
	}{role, n}
}

// TestEmergencyTrimPrioritizesHugeToolResults 应急裁剪优先剔除超大工具结果。
//
// 工具结果常是几十 KB 的日志，一条顶几十条闲聊；按条数裁剪既低效又伤对话连贯。
func TestEmergencyTrimPrioritizesHugeToolResults(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: "用户问题一"},
		{Role: RoleAssistant, Content: "回答一"},
		{Role: RoleUser, Kind: KindToolResult, Content: strings.Repeat("x", 40000)}, // 超大工具结果
		{Role: RoleAssistant, Content: "基于工具的结论"},
		{Role: RoleUser, Content: "用户问题二"},
		{Role: RoleAssistant, Content: "回答二"},
	}
	// 只按 0% 剔除（第一步只动超大工具结果）
	out := emergencyTrim(msgs, 0)
	// 条数不变（用占位符替换，维持 tool_call/tool_result 配对），但内容已被置空
	if len(out) != len(msgs) {
		t.Fatalf("占位式剔除应保持条数不变: %d -> %d", len(msgs), len(out))
	}
	hugeKept := false
	for _, m := range out {
		if m.Kind == KindToolResult && len(m.Content) > 40000 {
			hugeKept = true
		}
	}
	if hugeKept {
		t.Error("超大工具结果未被剔除")
	}
	if out[0].Role != RoleSystem {
		t.Error("system 必须保留")
	}
	// 对话主干仍在
	if len(out) < 5 {
		t.Errorf("不应删掉普通对话，实际剩 %d 条", len(out))
	}
}

// TestEmergencyTrimProgressive 递进裁剪：比例越大，保留越少。
func TestEmergencyTrimProgressive(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, Message{Role: RoleSystem, Content: "SYS"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: strings.Repeat("abcd", 50)})
		msgs = append(msgs, Message{Role: RoleAssistant, Content: strings.Repeat("efgh", 50)})
	}

	prev := len(msgs) + 1
	for _, pct := range []int{10, 20, 40, 60} {
		out := emergencyTrim(msgs, pct)
		if len(out) >= prev {
			t.Errorf("比例 %d%%：保留 %d 条，不应多于上一次的 %d 条", pct, len(out), prev)
		}
		if out[0].Role != RoleSystem {
			t.Errorf("比例 %d%%：system 未被保留", pct)
		}
		prev = len(out)
	}
}

// TestEmergencyTrimKeepsSystemAndLatest 必须保留 system 与最新消息。
func TestEmergencyTrimKeepsSystemAndLatest(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: "old-1"},
		{Role: RoleAssistant, Content: "old-2"},
		{Role: RoleUser, Content: "最新问题"},
	}
	out := emergencyTrim(msgs, 50)
	if len(out) < 2 {
		t.Fatalf("至少应保留 system + 最新消息，实际 %d", len(out))
	}
	if out[0].Role != RoleSystem {
		t.Error("system 必须保留")
	}
	if out[len(out)-1].Content != "最新问题" {
		t.Errorf("最新消息必须保留，实际末条=%q", out[len(out)-1].Content)
	}
}

// TestEmergencyTrimNoOpWhenTiny 消息很少时不削减。
func TestEmergencyTrimNoOpWhenTiny(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: "hi"},
	}
	out := emergencyTrim(msgs, 60)
	if len(out) != len(msgs) {
		t.Errorf("过短会话不应裁剪: %d -> %d", len(msgs), len(out))
	}
}

// TestEmergencyTrimDropsOldestFirst 无超大工具结果时，从最老的开始丢。
func TestEmergencyTrimDropsOldestFirst(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "SYS"},
		{Role: RoleUser, Content: "最老的问题" + strings.Repeat("a", 400)},
		{Role: RoleAssistant, Content: "最老的回答" + strings.Repeat("b", 400)},
		{Role: RoleUser, Content: "中间的问题" + strings.Repeat("c", 400)},
		{Role: RoleAssistant, Content: "中间的回答" + strings.Repeat("d", 400)},
		{Role: RoleUser, Content: "最新的问题"},
	}
	out := emergencyTrim(msgs, 40)
	// 最老的消息应先被丢弃
	for _, m := range out {
		if strings.HasPrefix(m.Content, "最老的问题") {
			t.Error("应优先丢弃最老的消息")
		}
	}
	// 最新的必须保留
	if out[len(out)-1].Content != "最新的问题" {
		t.Errorf("最新消息应保留，实际 %q", out[len(out)-1].Content)
	}
}

// TestEmergencyStepsCoverage 递进步长在 4 次内累计到足够深度。
func TestEmergencyStepsCoverage(t *testing.T) {
	steps := emergencyTrimSteps()
	if len(steps) < 3 {
		t.Fatalf("递进步数应 >= 3，实际 %d", len(steps))
	}
	if steps[len(steps)-1] < 60 {
		t.Errorf("最后一档应至少裁剪 60%%，实际 %d%%", steps[len(steps)-1])
	}
	// 单调递增
	for i := 1; i < len(steps); i++ {
		if steps[i] <= steps[i-1] {
			t.Errorf("步长应递增: %v", steps)
		}
	}
}
