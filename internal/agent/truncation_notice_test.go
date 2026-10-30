package agent

import (
	"testing"
)

// truncLLM 既满足基础 LLM 接口，又实现可选的 TruncationLister，
// 用于验证 agent 循环能否感知「上一条回复被 max_tokens 截断」。
type truncLLM struct {
	scriptLLM
	truncated bool
	readCount int
}

func (m *truncLLM) LastTruncated() bool {
	m.readCount++
	v := m.truncated
	m.truncated = false // 与 llm.OpenAI 一致：读后清零
	return v
}

// 截断时 OnTruncated 必须被回调，且带上当前步号，供 UI 提示用户「回答被截断」。
func TestOnTruncatedCallbackFired(t *testing.T) {
	pm := testManager(t)
	llm := &truncLLM{}
	llm.fn = func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"半截话"}`, nil
	}
	llm.truncated = true // 模拟上一次调用被 max_tokens 截断
	a := New(llm, pm)

	var gotSteps []int
	a.OnTruncated = func(step int) { gotSteps = append(gotSteps, step) }

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "半截话" {
		t.Fatalf("reply=%q", reply)
	}
	if len(gotSteps) != 1 {
		t.Fatalf("OnTruncated 应回调 1 次，实际 %d 次", len(gotSteps))
	}
}

// 未被截断时不得回调（否则每条回答都会误报「被截断」）。
func TestOnTruncatedNotFiredWhenClean(t *testing.T) {
	pm := testManager(t)
	llm := &truncLLM{} // truncated=false
	llm.fn = func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"完整回答"}`, nil
	}
	a := New(llm, pm)

	fired := 0
	a.OnTruncated = func(step int) { fired++ }
	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
	if fired != 0 {
		t.Fatalf("未截断时不应回调，实际 %d 次", fired)
	}
}

// LLM 未实现 TruncationLister 时必须完全无副作用：不 panic、不回调、行为与旧版一致。
// 这条守护「可选接口」契约——第三方 LLM 实现（含测试替身）不需改动即可继续工作。
func TestNotTruncationListerIsSafe(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"ok"}`, nil
	}}, pm)

	fired := 0
	a.OnTruncated = func(step int) { fired++ }
	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "ok" {
		t.Fatalf("reply=%q", reply)
	}
	if fired != 0 {
		t.Fatalf("不支持截断信号的 LLM 不应触发回调，实际 %d 次", fired)
	}
}

// OnTruncated 为 nil 时（无人关心中断提示）也必须安全，不得空指针。
func TestTruncatedWithNilCallbackIsSafe(t *testing.T) {
	pm := testManager(t)
	llm := &truncLLM{}
	llm.fn = func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"x"}`, nil
	}
	a := New(llm, pm)
	a.OnTruncated = nil

	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
}
