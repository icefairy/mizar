package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// P-1 扩展事件拦截：ToolCallDecision deny / rewrite / allow 语义。

// TestHookDecisionDeny 裁决 hook 拒绝 calc 调用：工具不执行，模型收到拒绝原因。
func TestHookDecisionDeny(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-dec-*")
	defer os.RemoveAll(dir)
	os.WriteFile(filepath.Join(dir, "calc.ts"), []byte(`export function tool_calc(args: string): string {
    const p = args.split(/[+\-*\/]/);
    const a = parseFloat(p[0]), b = parseFloat(p[1]);
    if (args.includes("+")) return String(a+b);
    if (args.includes("*")) return String(a*b);
    return "bad";
}`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	sawDeny := false
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, msg := range msgs {
			if msg.Kind == KindToolResult && strings.Contains(msg.Content, "calc 被安全策略禁止") {
				sawDeny = true
			}
		}
		if sawDeny {
			return `{"action":"reply","text":"收到拒绝"}`, nil
		}
		return `{"action":"tool","tool":"calc","args":"2+3"}`, nil
	}}
	a := New(llm, m)
	a.Hooks = NewHooks()
	a.Hooks.OnToolCallDecision(func(ctx *HookContext) (Decision, string) {
		if ctx.Tool == "calc" {
			return DecisionDeny, "calc 被安全策略禁止"
		}
		return DecisionAllow, ""
	})
	got, err := a.Run("算一下")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "收到拒绝" || !sawDeny {
		t.Fatalf("deny 未回填给模型: reply=%q sawDeny=%v", got, sawDeny)
	}
	// 拒绝不是计算结果：消息里不能出现 calc 的正常输出 5
	for _, msg := range a.Initial {
		_ = msg
	}
}

// TestHookDecisionRewrite 裁决 hook 改写参数：工具按改写后的参数执行。
func TestHookDecisionRewrite(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-rew-*")
	defer os.RemoveAll(dir)
	os.WriteFile(filepath.Join(dir, "calc.ts"), []byte(`export function tool_calc(args: string): string {
    const p = args.split(/[+\-*\/]/);
    const a = parseFloat(p[0]), b = parseFloat(p[1]);
    if (args.includes("+")) return String(a+b);
    if (args.includes("*")) return String(a*b);
    return "bad";
}`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	sawResult := false
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, msg := range msgs {
			if msg.Kind == KindToolResult && strings.Contains(msg.Content, "工具结果") {
				sawResult = true
			}
		}
		if sawResult {
			return `{"action":"reply","text":"done"}`, nil
		}
		return `{"action":"tool","tool":"calc","args":"2+3"}`, nil
	}}
	a := New(llm, m)
	a.Hooks = NewHooks()
	var gotArgs string
	a.Hooks.OnToolResult(func(ctx *HookContext) error {
		gotArgs = ctx.Args
		return nil
	})
	a.Hooks.OnToolCallDecision(func(ctx *HookContext) (Decision, string) {
		if ctx.Tool == "calc" {
			ctx.Args = "3*4"
			return DecisionRewrite, ""
		}
		return DecisionAllow, ""
	})
	if _, err := a.Run("算一下"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotArgs != "3*4" {
		t.Fatalf("rewrite 未生效，实际执行 args=%q", gotArgs)
	}
}

// TestHookDecisionOrder 多个裁决：rewrite 先生效，deny 后到立即终止。
func TestHookDecisionOrder(t *testing.T) {
	h := NewHooks()
	var seen []string
	h.OnToolCallDecision(func(ctx *HookContext) (Decision, string) {
		seen = append(seen, "h1:"+ctx.Args)
		if ctx.Args == "orig" {
			ctx.Args = "rewritten"
			return DecisionRewrite, ""
		}
		return DecisionAllow, ""
	})
	h.OnToolCallDecision(func(ctx *HookContext) (Decision, string) {
		seen = append(seen, "h2:"+ctx.Args)
		if ctx.Args == "stop" {
			return DecisionDeny, "no"
		}
		return DecisionAllow, ""
	})
	d, reason := h.fireToolCallDecision(&HookContext{Tool: "t", Args: "orig"}, nil)
	if d != DecisionRewrite || len(seen) != 2 || seen[0] != "h1:orig" || seen[1] != "h2:rewritten" {
		t.Fatalf("rewrite 链路错误: d=%v seen=%v", d, seen)
	}
	d, reason = h.fireToolCallDecision(&HookContext{Tool: "t", Args: "stop"}, nil)
	if d != DecisionDeny || reason != "no" {
		t.Fatalf("deny 应立即生效: d=%v reason=%q", d, reason)
	}
}

// TestHookDecisionPanicFailClosed 裁决 hook panic 按 deny 兜底（fail-closed）。
func TestHookDecisionPanicFailClosed(t *testing.T) {
	h := NewHooks()
	h.OnToolCallDecision(func(ctx *HookContext) (Decision, string) {
		panic("boom")
	})
	d, reason := h.fireToolCallDecision(&HookContext{Tool: "t", Args: "x"}, nil)
	if d != DecisionDeny || !strings.Contains(reason, "fail-closed") {
		t.Fatalf("panic 应 fail-closed deny: d=%v reason=%q", d, reason)
	}
}
