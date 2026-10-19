package agent

import (
	"strings"
	"testing"
)

// 场景：任务尚未收尾（还有未完成项），模型却调用 reply 结束本轮。
// 这是用户最常抱怨的「明明没做完就停了，必须手打继续」。
//
// 期望：CompletenessGate 拦截这次收尾 → 注入「还有未完成项」提醒 →
// 循环继续下一轮，而不是把回复直接返回给用户。

func TestCompletenessGateContinuesWhenPending(t *testing.T) {
	a, _, llm := newTestAgent(
		`{"action":"reply","text":"好的，已完成登录功能。"}`,
		`{"action":"reply","text":"CONTINUED-AFTER-NUDGE"}`,
	)
	nudged := 0
	a.Completeness = NewCompletenessGate(func() []string {
		// 第一次收尾时仍有未完成项 → 拦截并提醒；模型被提醒后继续干活，
		// 第二次收尾时视为已完成（模拟真实行为）。
		if nudged == 0 {
			nudged++
			return []string{"实现登录", "写测试"}
		}
		return nil
	})

	out, err := a.Run("实现登录功能并写测试")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "CONTINUED-AFTER-NUDGE" {
		t.Fatalf("未完成就收尾应被拦截并续跑，实际返回 %q（LLM 调用 %d 次）", out, llm.calls)
	}
	if llm.calls != 2 {
		t.Fatalf("应多调用一轮 LLM 以续跑，实际 %d 次", llm.calls)
	}
}

// 没有未完成项时应正常收尾，不得无限续跑。
func TestCompletenessGateAllowsReplyWhenNothingPending(t *testing.T) {
	a, _, llm := newTestAgent(
		`{"action":"reply","text":"DONE-OK"}`,
	)
	a.Completeness = NewCompletenessGate(func() []string { return nil })

	out, err := a.Run("6*7 等于几")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "DONE-OK" {
		t.Fatalf("want DONE-OK, got %q", out)
	}
	if llm.calls != 1 {
		t.Fatalf("不应额外续跑，实际调用 %d 次", llm.calls)
	}
}

// 未注入 Pending（nil）时闸门必须完全关闭：这是纯问答/单步任务的默认行为。
func TestCompletenessGateNilIsNoop(t *testing.T) {
	a, _, llm := newTestAgent(`{"action":"reply","text":"42"}`)
	a.Completeness = NewCompletenessGate(nil)

	out, err := a.Run("6*7 等于几")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "42" || llm.calls != 1 {
		t.Fatalf("闸门应无副作用，得到 out=%q calls=%d", out, llm.calls)
	}
}

// 续跑次数有上限：模型反复无视提醒时到上限就放行，避免无限烧 token。
func TestCompletenessGateNudgeLimit(t *testing.T) {
	replies := []string{}
	for i := 0; i < 10; i++ {
		replies = append(replies, `{"action":"reply","text":"还没完，但我不干了"}`)
	}
	a, _, llm := newTestAgent(replies...)
	a.Completeness = NewCompletenessGate(func() []string { return []string{"永不完成"} })

	out, err := a.Run("做一件永远做不完的事")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out == "" {
		t.Fatal("到达上限后应把模型回复返回给用户，而不是空")
	}
	if want := completenessMaxNudges + 1; llm.calls != want {
		t.Fatalf("LLM 调用次数 = %d, want %d（续跑上限 %d）", llm.calls, want, completenessMaxNudges)
	}
}

// 提醒文本必须点明「还有哪些未完成」，否则模型无从续起。
func TestCompletenessNudgeTextListsPending(t *testing.T) {
	g := NewCompletenessGate(nil)
	text := g.NudgeText([]string{"实现登录", "写测试"})
	for _, want := range []string{"实现登录", "写测试", "未完成"} {
		if !strings.Contains(text, want) {
			t.Fatalf("提醒文本缺少 %q: %s", want, text)
		}
	}
}
