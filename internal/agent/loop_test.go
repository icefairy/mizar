package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// mockLLM 脚本化 LLM：按顺序返回预设回复。
type mockLLM struct {
	replies []string
	calls   int
}

func (m *mockLLM) Chat(msgs []Message) (string, error) {
	if m.calls >= len(m.replies) {
		return "", fmt.Errorf("mock exhausted at call %d", m.calls)
	}
	r := m.replies[m.calls]
	m.calls++
	return r, nil
}

func newTestAgent(replies ...string) (*Agent, *plugins.Manager, *mockLLM) {
	dir, _ := os.MkdirTemp("", "mizar-agent-*")
	os.WriteFile(filepath.Join(dir, "calc.ts"), []byte(`export function tool_calc(args: string): string {
    const p = args.split(/[+\-*\/]/);
    if (p.length !== 2) return "bad";
    const a = parseFloat(p[0]), b = parseFloat(p[1]);
    if (args.includes("+")) return String(a+b);
    if (args.includes("*")) return String(a*b);
    return "bad";
}`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		panic(fmt.Sprintf("plugin load failed: %v", failed))
	}
	llm := &mockLLM{replies: replies}
	return New(llm, m), m, llm
}

func TestRunToolThenReply(t *testing.T) {
	a, _, _ := newTestAgent(
		`{"action":"tool","tool":"calc","args":"2+3"}`,
		`{"action":"reply","text":"结果是5"}`,
	)
	got, err := a.Run("2加3等于几")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "结果是5" {
		t.Fatalf("want 结果是5 got %q", got)
	}
}

func TestToolResultFedToLLM(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-feed-*")
	os.WriteFile(filepath.Join(dir, "t.ts"), []byte(`export function tool_echo(args: string): string { return "ECHO:" + args; }`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	var seen []string
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, msg := range msgs {
			if msg.Role == RoleUser && strings.Contains(msg.Content, "工具结果") {
				seen = append(seen, msg.Content)
			}
		}
		if len(seen) == 0 {
			return `{"action":"tool","tool":"echo","args":"abc"}`, nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}}
	a := New(llm, m)
	if _, err := a.Run("x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "ECHO:abc") {
		t.Fatalf("tool result not fed to LLM: %v", seen)
	}
}

func TestParseRecovery(t *testing.T) {
	// 第一次回复是垃圾，第二次修复为合法 JSON
	a, _, _ := newTestAgent(
		`这不是JSON，我随便写的`,
		`{"action":"reply","text":"修好了"}`,
	)
	got, err := a.Run("x")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "修好了" {
		t.Fatalf("want 修好了 got %q", got)
	}
}

func TestMaxSteps(t *testing.T) {
	// 模型永远输出 tool 调用 → 循环到 max
	a, _, _ := newTestAgent(
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
	)
	a.MaxSteps = 3
	if _, err := a.Run("x"); err == nil {
		t.Fatal("expected max steps error")
	}
}

// scriptLLM 用回调驱动。
type scriptLLM struct {
	fn func(msgs []Message) (string, error)
}

func (s *scriptLLM) Chat(msgs []Message) (string, error) { return s.fn(msgs) }
