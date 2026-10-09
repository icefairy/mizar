package agent

import (
	"errors"
	"strings"
	"testing"

	"mizar/internal/plugins"
)

var errTestBoom = errors.New("boom")

// 声明 Terminate 的工具执行成功后，循环应直接以工具输出作为最终回答，
// 不再发起 follow-up LLM 调用（对齐 pi 0.69.0 `terminate: true`）。
func TestTerminatingToolEndsRunWithoutFollowUp(t *testing.T) {
	a, m, llm := newTestAgent(
		`{"action":"tool","tool":"finish","args":"{\"ok\":1}"}`,
		// 若循环没有终止，会消费这条并返回它——测试据此判断是否多调了一轮
		`{"action":"reply","text":"SHOULD-NOT-BE-USED"}`,
	)
	m.RegisterBuiltin(plugins.Tool{
		Name:        "finish",
		Description: "Finish the task. Args: {}",
		Terminate:   true,
		Run:         func(string) (string, error) { return "FINAL-ANSWER", nil },
	})

	out, err := a.Run("do it")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "FINAL-ANSWER" {
		t.Fatalf("应以工具输出收尾，得到 %q", out)
	}
	if llm.calls != 1 {
		t.Fatalf("应只调用 1 次 LLM，实际 %d 次", llm.calls)
	}
}

// 未声明 Terminate 的工具照旧：结果回填后继续下一轮。
func TestNonTerminatingToolContinues(t *testing.T) {
	a, m, llm := newTestAgent(
		`{"action":"tool","tool":"peek","args":"{\"ok\":1}"}`,
		`{"action":"reply","text":"done"}`,
	)
	m.RegisterBuiltin(plugins.Tool{
		Name:        "peek",
		Description: "Peek. Args: {}",
		Run:         func(string) (string, error) { return "peeked", nil },
	})

	out, err := a.Run("do it")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "done" {
		t.Fatalf("应继续到第二轮，得到 %q", out)
	}
	if llm.calls != 2 {
		t.Fatalf("应调用 2 次 LLM，实际 %d 次", llm.calls)
	}
}

// 终止工具执行失败时不应收尾（错误要回填给模型，让它有机会纠正）。
func TestTerminatingToolErrorDoesNotEndRun(t *testing.T) {
	a, m, _ := newTestAgent(
		`{"action":"tool","tool":"finish","args":"{\"ok\":1}"}`,
		`{"action":"reply","text":"recovered"}`,
	)
	m.RegisterBuiltin(plugins.Tool{
		Name:        "finish",
		Description: "Finish. Args: {}",
		Terminate:   true,
		Run:         func(string) (string, error) { return "", errTestBoom },
	})

	out, err := a.Run("do it")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(out, "recovered") {
		t.Fatalf("失败后应继续循环，得到 %q", out)
	}
}

// respond 的既有行为不能被通用机制破坏。
func TestRespondStillTerminates(t *testing.T) {
	a, _, _ := newTestAgent(
		`{"action":"tool","tool":"respond","args":"{\"text\":\"hello\"}"}`,
	)
	out, err := a.Run("hi")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if out != "hello" {
		t.Fatalf("respond 应直接返回 text，得到 %q", out)
	}
}

// 终止工具的输出应写回消息历史（供会话持久化/后续恢复）。
func TestTerminatingToolOutputRecordedInHistory(t *testing.T) {
	a, m, _ := newTestAgent(`{"action":"tool","tool":"finish","args":"{\"ok\":1}"}`)
	m.RegisterBuiltin(plugins.Tool{
		Name:        "finish",
		Description: "Finish. Args: {}",
		Terminate:   true,
		Run:         func(string) (string, error) { return "RESULT-XYZ", nil },
	})
	var got []Message
	a.OnToolExchange = func(tool, args, reason, out string, err error) {
		got = append(got, ToolExchangeMessages(tool, args, reason, out, err)...)
	}
	if _, err := a.Run("do it"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("终止工具的输出也应通过 OnToolExchange 记录")
	}
	found := false
	for _, msg := range got {
		if strings.Contains(msg.Content, "RESULT-XYZ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("工具输出未记录: %+v", got)
	}
}
