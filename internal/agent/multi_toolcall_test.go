package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"mizar/internal/plugins"
)

// 纯文本协议下发多个工具调用：必须按出现顺序全部执行，不能只执行首个。
func TestRunExecutesAllTextProtocolCallsInOrder(t *testing.T) {
	pm := testManager(t)
	var executed []string
	for _, name := range []string{"t_a", "t_b", "t_c"} {
		n := name
		pm.RegisterBuiltin(plugins.Tool{
			Name:        n,
			Description: "test tool",
			Run: func(args string) (string, error) {
				executed = append(executed, n)
				return "ok:" + n, nil
			},
		})
	}

	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, m := range msgs {
			if m.Kind == KindToolResult {
				return `{"action":"reply","text":"done"}`, nil
			}
		}
		// 一轮里发三个工具调用（文本协议 JSON 数组形态）
		return `{"action":"tool","calls":[` +
			`{"tool":"t_a","args":"{\"v\":1}"},` +
			`{"tool":"t_b","args":"{\"v\":1}"},` +
			`{"tool":"t_c","args":"{\"v\":1}"}` +
			`],"tool":"t_a","args":"{\"v\":1}"}`, nil
	}}
	a := New(llm, pm)

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("reply=%q", reply)
	}
	want := []string{"t_a", "t_b", "t_c"}
	if len(executed) != len(want) {
		t.Fatalf("应执行全部 %d 个调用（按顺序），实际执行 %v", len(want), executed)
	}
	for i := range want {
		if executed[i] != want[i] {
			t.Fatalf("顺序必须保持: want %v, got %v", want, executed)
		}
	}
}

// 文本协议里带 reason：每个调用都应带上它，且只发一次 LLM 请求（一轮多调用）。
func TestRunMultiCallsCarryReasonAndSingleLLMTurn(t *testing.T) {
	pm := testManager(t)
	var reasons []string
	for _, name := range []string{"m_a", "m_b"} {
		n := name
		pm.RegisterBuiltin(plugins.Tool{
			Name:        n,
			Description: "test tool",
			Run:         func(args string) (string, error) { return "ok", nil },
		})
	}
	llm := &scriptLLM{}
	calls := 0
	llm.fn = func(msgs []Message) (string, error) {
		calls++
		for _, m := range msgs {
			if m.Kind == KindToolResult {
				return `{"action":"reply","text":"fin"}`, nil
			}
		}
		return `{"action":"tool","calls":[{"tool":"m_a","args":"{\"v\":1}"},{"tool":"m_b","args":"{\"v\":1}"}],"reason":"先做这两步"}`, nil
	}
	a := New(llm, pm)
	// OnToolExchange 会把 reason 一起回传，借此观察
	a.OnToolExchange = func(tool, args, reason, out string, err error) {
		reasons = append(reasons, reason)
	}

	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 2 {
		t.Fatalf("两个调用都应触发 OnToolExchange, got %v", reasons)
	}
	for i, r := range reasons {
		if r != "先做这两步" {
			t.Fatalf("第 %d 个调用的 reason 应透传, got %q", i, r)
		}
	}
	if calls != 2 {
		t.Fatalf("应为 2 次 LLM 调用（1 次出多调用 + 1 次收尾）, got %d", calls)
	}
}

// respond 混在 calls 里：应作为最终回答立即返回，后续调用不再执行。
func TestRunMultiCallsRespondStopsRemaining(t *testing.T) {
	pm := testManager(t)
	executed := false
	pm.RegisterBuiltin(plugins.Tool{
		Name:        "after_respond",
		Description: "should not run",
		Run:         func(args string) (string, error) { executed = true; return "x", nil },
	})
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"tool","calls":[` +
			`{"tool":"respond","args":"{\"text\":\"最终答案\"}"},` +
			`{"tool":"after_respond","args":"{\"v\":1}"}` +
			`]}`, nil
	}}
	a := New(llm, pm)

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "最终答案" {
		t.Fatalf("respond 应作为最终回答, got %q", reply)
	}
	if executed {
		t.Fatal("respond 之后的调用不应被执行")
	}
}

// 单数字段形态（旧协议）必须保持兼容：没有 calls 时按单个执行。
func TestRunSingleFieldStillWorks(t *testing.T) {
	pm := testManager(t)
	var got []string
	pm.RegisterBuiltin(plugins.Tool{
		Name:        "solo",
		Description: "test",
		Run:         func(args string) (string, error) { got = append(got, "solo"); return "ok", nil },
	})
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, m := range msgs {
			if m.Kind == KindToolResult {
				return `{"action":"reply","text":"fin"}`, nil
			}
		}
		return `{"action":"tool","tool":"solo","args":"{\"v\":1}"}`, nil
	}}
	a := New(llm, pm)

	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("单数形态应执行一次, got %v", got)
	}
}

// 终止型工具出现在多调用里：应返回其输出，剩余调用不再执行。
func TestRunMultiCallsTerminateSkipsRemaining(t *testing.T) {
	pm := testManager(t)
	ran := false
	pm.RegisterBuiltin(plugins.Tool{
		Name:        "stop_here",
		Description: "terminating",
		Terminate:   true,
		Run:         func(args string) (string, error) { return "终止输出", nil },
	})
	pm.RegisterBuiltin(plugins.Tool{
		Name:        "never_run",
		Description: "should not run",
		Run:         func(args string) (string, error) { ran = true; return "x", nil },
	})
	llm := &scriptLLM{}
	calls := 0
	llm.fn = func(msgs []Message) (string, error) {
		calls++
		return `{"action":"tool","calls":[` +
			`{"tool":"stop_here","args":"{\"v\":1}"},` +
			`{"tool":"never_run","args":"{\"v\":1}"}` +
			`]}`, nil
	}
	a := New(llm, pm)

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "终止输出" {
		t.Fatalf("终止型工具输出应作为最终回答, got %q", reply)
	}
	if ran {
		t.Fatal("终止型工具之后的调用不应被执行")
	}
	if calls != 1 {
		t.Fatalf("终止型工具应省掉 follow-up LLM 调用, got %d calls", calls)
	}
}

// 提取层：文本里出现多个工具 JSON 时全部提取且保序。
func TestExtractAllActionJSONMultiple(t *testing.T) {
	text := `我按顺序做两件事。
{"action":"tool","tool":"read","args":"{\"path\":\"a\"}"}
{"action":"tool","tool":"write","args":"{\"path\":\"b\"}"}`
	objs := ExtractAllActionJSON(text)
	if len(objs) != 2 {
		t.Fatalf("应提取 2 个对象, got %d: %v", len(objs), objs)
	}
	var first, second struct {
		Tool string `json:"tool"`
	}
	_ = json.Unmarshal([]byte(objs[0]), &first)
	_ = json.Unmarshal([]byte(objs[1]), &second)
	if first.Tool != "read" || second.Tool != "write" {
		t.Fatalf("顺序必须保持: %q, %q", first.Tool, second.Tool)
	}
}

// 提取层：单数 API 行为不能变（返回首个）。
func TestExtractActionJSONStillReturnsFirst(t *testing.T) {
	objs := ExtractAllActionJSON(`{"action":"tool","tool":"x","args":"{}"} {"action":"tool","tool":"y","args":"{}"}`)
	if len(objs) != 2 {
		t.Fatalf("应提取 2 个, got %d", len(objs))
	}
	if !strings.Contains(objs[0], `"x"`) {
		t.Fatalf("首个应是 x: %v", objs)
	}
}
