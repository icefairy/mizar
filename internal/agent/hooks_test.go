package agent

import (
	"errors"
	"strings"
	"testing"
)

// TestHooksMultipleMountOrder 多插件挂载同一挂载点：按注册顺序执行。
func TestHooksMultipleMountOrder(t *testing.T) {
	h := NewHooks()
	var order []string
	h.OnRunStart(func(ctx *HookContext) error { order = append(order, "a"); return nil })
	h.OnRunStart(func(ctx *HookContext) error { order = append(order, "b"); return nil })
	h.OnRunStart(func(ctx *HookContext) error { order = append(order, "c"); return nil })

	ctx := &HookContext{}
	h.fireRunStart(ctx, nil)
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("order = %v, want [a b c]", order)
	}
}

// TestHooksErrorDoesNotStopChain 一个挂载函数返回 error 不中断后续挂载。
func TestHooksErrorDoesNotStopChain(t *testing.T) {
	h := NewHooks()
	var calls int
	h.OnRunEnd(func(ctx *HookContext) error { calls++; return nil })
	h.OnRunEnd(func(ctx *HookContext) error { calls++; return errors.New("boom") })
	h.OnRunEnd(func(ctx *HookContext) error { calls++; return nil })

	h.fireRunEnd(&HookContext{}, nil)
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (error must not stop chain)", calls)
	}
}

// TestHooksLLMRequestCanModifyMessages LLMRequest 挂载点可修改 Messages（🔴 缓存敏感）。
func TestHooksLLMRequestCanModifyMessages(t *testing.T) {
	h := NewHooks()
	h.OnLLMRequest(func(ctx *HookContext) error {
		ctx.Messages = append(ctx.Messages, Message{Role: RoleUser, Content: "注入的上下文"})
		return nil
	})
	var got int
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		got = len(msgs)
		return `{"action":"reply","text":"ok"}`, nil
	}}
	pm := testManager(t)
	a := New(llm, pm)
	a.Hooks = h

	_, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	// system + task + 注入 = 3
	if got != 3 {
		t.Fatalf("llm saw %d messages, want 3 (hook injection)", got)
	}
}

// TestHooksAllPointsFired 验证所有挂载点都在对应环节触发。
func TestHooksAllPointsFired(t *testing.T) {
	pm := testManager(t)
	var fired []string
	h := NewHooks()
	h.OnRunStart(func(c *HookContext) error { fired = append(fired, "RunStart"); return nil })
	h.OnStepStart(func(c *HookContext) error { fired = append(fired, "StepStart"); return nil })
	h.OnLLMRequest(func(c *HookContext) error { fired = append(fired, "LLMRequest"); return nil })
	h.OnLLMResponse(func(c *HookContext) error { fired = append(fired, "LLMResponse"); return nil })
	h.OnToolCall(func(c *HookContext) error { fired = append(fired, "ToolCall"); return nil })
	h.OnToolResult(func(c *HookContext) error { fired = append(fired, "ToolResult"); return nil })
	h.OnStepEnd(func(c *HookContext) error { fired = append(fired, "StepEnd"); return nil })
	h.OnRunEnd(func(c *HookContext) error { fired = append(fired, "RunEnd"); return nil })

	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"tool","tool":"ping","args":""}`, nil
	}}
	a := New(llm, pm)
	a.Hooks = h

	// 第一次 LLM 调用返回工具调用，但脚本一直返回工具调用会死循环；用 2 步策略
	llm.fn = func(msgs []Message) (string, error) {
		if len(msgs) <= 3 { // 首次调用：工具
			return `{"action":"tool","tool":"ping","args":""}`, nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}
	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q", reply)
	}
	for _, want := range []string{"RunStart", "StepStart", "LLMRequest", "LLMResponse", "ToolCall", "ToolResult", "StepEnd", "RunEnd"} {
		found := false
		for _, f := range fired {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("挂载点 %s 未触发; fired=%v", want, fired)
		}
	}
}

// TestHooksCompactionFired 压缩触发时 CompactionBefore/After 挂载点触发。
func TestHooksCompactionFired(t *testing.T) {
	pm := testManager(t)
	var compactEvents []string
	h := NewHooks()
	h.OnCompactionBefore(func(c *HookContext) error { compactEvents = append(compactEvents, "Before"); return nil })
	h.OnCompactionAfter(func(c *HookContext) error { compactEvents = append(compactEvents, "After"); return nil })

	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"done"}`, nil
	}}
	a := New(llm, pm)
	a.Hooks = h
	a.Compactor = DefaultCompactor(func(msgs []Message) (string, error) { return "summary", nil })
	a.Compactor.ContextWindow = 100
	a.Compactor.ReserveTokens = 10
	a.Compactor.KeepRecentTokens = 10
	a.Compactor.MaxSummaryTokens = 100

	// 塞长任务强制超限
	longTask := strings.Repeat("长", 2000)
	_, err := a.Run(longTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(compactEvents) != 2 || compactEvents[0] != "Before" || compactEvents[1] != "After" {
		t.Fatalf("compact events = %v, want [Before After]", compactEvents)
	}
}

// TestCacheImpactLabels 缓存影响分级文案存在且正确。
func TestCacheImpactLabels(t *testing.T) {
	if ImpactSafe.String() != "🟢 安全" {
		t.Error("ImpactSafe label wrong")
	}
	if ImpactModerate.String() != "🟡 中等" {
		t.Error("ImpactModerate label wrong")
	}
	if ImpactSevere.String() != "🔴 严重" {
		t.Error("ImpactSevere label wrong")
	}
	if !strings.Contains(ImpactSevere.CacheWarning(), "缓存") {
		t.Error("severe warning should mention cache")
	}
	specs := AllHookSpecs()
	if len(specs) < 10 {
		t.Errorf("hook specs = %d, want >= 10", len(specs))
	}
	// LLMRequest 必须是 🔴
	for _, s := range specs {
		if s.Name == "LLMRequest" && s.CacheImpact != ImpactSevere {
			t.Error("LLMRequest should be ImpactSevere")
		}
	}
}
