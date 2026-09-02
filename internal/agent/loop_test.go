package agent

import (
	"errors"
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

func TestMaxStepsErrorHasHint(t *testing.T) {
	// 耗尽时应返回带调大提示的错误，且 errors.Is 兼容 ErrMaxSteps（abortreason 分类依赖）。
	a, _, _ := newTestAgent(
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
		`{"action":"tool","tool":"calc","args":"1*1"}`,
	)
	a.MaxSteps = 3
	_, err := a.Run("x")
	if err == nil {
		t.Fatal("expected max steps error")
	}
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("expected ErrMaxSteps wrapping, got: %v", err)
	}
	for _, kw := range []string{"/config max_steps", "max_steps"} {
		if !strings.Contains(err.Error(), kw) {
			t.Fatalf("error should hint how to raise the limit (missing %q): %v", kw, err)
		}
	}
}

func TestStepBudgetWarningInjected(t *testing.T) {
	// 剩余步数 ≤ 3 时向 LLM 注入预算预警（检查出现在发给模型的消息里）。
	warned := false
	calls := 0
	a, _, _ := newTestAgent()
	a.MaxSteps = 5
	s := &scriptLLM{fn: func(msgs []Message) (string, error) {
		calls++
		for _, msg := range msgs {
			if msg.Role == RoleUser && strings.Contains(msg.Content, "剩余步骤") {
				warned = true
			}
		}
		return `{"action":"tool","tool":"calc","args":"1*1"}`, nil
	}}
	a.LLM = s
	if _, err := a.Run("x"); err == nil {
		t.Fatal("expected max steps error")
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", calls)
	}
	if !warned {
		t.Fatal("expected step budget warning message to be injected before LLM calls")
	}
}

// streamMockLLM 实现 StreamLLM：按轮次分发流式分片（每轮 = agent 循环一次 LLM 调用的完整回复）。
type streamMockLLM struct {
	steps [][]string // steps[i] = 第 i 次 LLM 调用的流式分片
	calls int
}

func (m *streamMockLLM) Chat(msgs []Message) (string, error) {
	if m.calls >= len(m.steps) {
		return "", fmt.Errorf("mock exhausted")
	}
	chunks := m.steps[m.calls]
	m.calls++
	return strings.Join(chunks, ""), nil
}

func (m *streamMockLLM) ChatStream(msgs []Message, onToken func(StreamDelta)) (string, error) {
	if m.calls >= len(m.steps) {
		return "", fmt.Errorf("mock exhausted")
	}
	chunks := m.steps[m.calls]
	m.calls++
	full := ""
	for _, c := range chunks {
		full += c
		onToken(StreamDelta{Content: c})
	}
	return full, nil
}

// TestRunStreamReply 验证 OnLLMStream 收到流式分片且 agent 返回最终回复。
func TestRunStreamReply(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-agent-*")
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	llm := &streamMockLLM{steps: [][]string{{`{"action":"reply","text":"第一`, `段，`, `第二段"}`}}}
	a := New(llm, m)
	var steps []int
	var got []string
	a.OnLLMStream = func(step int, d StreamDelta) {
		steps = append(steps, step)
		if d.Content != "" {
			got = append(got, d.Content)
		}
	}
	reply, err := a.Run("测试流式")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply != "第一段，第二段" {
		t.Fatalf("want 第一段，第二段 got %q", reply)
	}
	if len(steps) == 0 {
		t.Fatal("OnLLMStream 未被调用")
	}
	if strings.Join(got, "") != `{"action":"reply","text":"第一段，第二段"}` {
		t.Fatalf("增量回调不符: %v", got)
	}
}

// TestRunStreamReplyWithTool 验证工具调用步（tool JSON）不会破坏流式回复步。
func TestRunStreamReplyWithTool(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-agent-*")
	os.WriteFile(filepath.Join(dir, "calc.ts"), []byte(`export function tool_calc(args: string): string {
    const p = args.split(/[+\-*\/]/);
    if (p.length !== 2) return "bad";
    const a = parseFloat(p[0]), b = parseFloat(p[1]);
    if (args.includes("+")) return String(a+b);
    return "bad";
}`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("plugin load failed: %v", failed)
	}
	llm := &streamMockLLM{steps: [][]string{
		// 第一步：工具调用（流式分片）
		{`{"action":"tool","tool":"calc","args":"2+3"}`},
		// 第二步：最终回复（流式分片）
		{`{"action":"reply","text":"结果是`, `5"}`},
	}}
	a := New(llm, m)
	var got []string
	a.OnLLMStream = func(step int, d StreamDelta) {
		if d.Content != "" {
			got = append(got, d.Content)
		}
	}
	reply, err := a.Run("2加3等于几")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply != "结果是5" {
		t.Fatalf("want 结果是5 got %q", reply)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 个分片回调（工具 JSON 1 + 回复 2），got %d: %v", len(got), got)
	}
}

// ============================================================================
// Context overflow / non-retryable error 分类测试
// ============================================================================

func TestIsContextOverflow(t *testing.T) {
	tests := []struct {
		err    string
		wantOK bool
	}{
		{"llm status 400: maximum context length exceeded", true},
		{"llm error: context_length_exceeded", true},
		{"llm status 400: prompt is too long", true},
		{"llm status 400: 超出上下文长度限制", true},
		{"llm status 500: internal server error", false},
		{"http: connection refused", false},
		{"decode: unexpected EOF", false},
		{"", false},
	}
	for _, tt := range tests {
		var err error
		if tt.err != "" {
			err = errors.New(tt.err)
		}
		got := isContextOverflow(err)
		if got != tt.wantOK {
			t.Errorf("isContextOverflow(%q) = %v, want %v", tt.err, got, tt.wantOK)
		}
	}
}

func TestIsNonRetryableError(t *testing.T) {
	tests := []struct {
		err    string
		wantOK bool
	}{
		{"llm error: model not found", true},
		{"llm status 401: unauthorized", true},
		{"llm status 403: forbidden", true},
		{"llm status 402: insufficient quota", true},
		{"llm status 429: rate limit exceeded, retry after 60s", false}, // 带 retry 提示的 429 可重试
		{"llm status 500: internal server error", false},
		{"http: connection refused", false},
		{"", false},
	}
	for _, tt := range tests {
		var err error
		if tt.err != "" {
			err = errors.New(tt.err)
		}
		got := isNonRetryableError(err)
		if got != tt.wantOK {
			t.Errorf("isNonRetryableError(%q) = %v, want %v", tt.err, got, tt.wantOK)
		}
	}
}

// TestRespondToolReturnsText 模型调用 respond 工具 → 直接返回 text，不执行工具、不回填结果。
// respond 是协议层面的回答出口，由 agent 循环拦截（无需工具注册）。
func TestRespondToolReturnsText(t *testing.T) {
	a, _, _ := newTestAgent(`{"action":"tool","tool":"respond","args":"{\"text\":\"任务完成！\"}"}`)
	got, err := a.Run("汇报一下")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "任务完成！" {
		t.Fatalf("want 任务完成！ got %q", got)
	}
}

// TestRespondToolEmptyText respond 工具缺 text 参数 → 注入纠错提示，模型重试。
func TestRespondToolEmptyText(t *testing.T) {
	steps := 0
	s := &scriptLLM{fn: func(msgs []Message) (string, error) {
		steps++
		if steps == 1 {
			// 第一轮：空 text 参数
			return `{"action":"tool","tool":"respond","args":"{}"}`, nil
		}
		// 第二轮：应收到纠错提示后在第二轮给出正确回答
		for _, msg := range msgs {
			if strings.Contains(msg.Content, "缺少 text 参数") {
				return `{"action":"tool","tool":"respond","args":"{\"text\":\"修正后回答\"}"}`, nil
			}
		}
		return `{"action":"tool","tool":"respond","args":"{\"text\":\"修正后回答\"}"}`, nil
	}}
	dir, _ := os.MkdirTemp("", "mizar-respond-*")
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	_, _ = m.LoadAll()
	a := New(s, m)
	got, err := a.Run("回答我")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "修正后回答" {
		t.Fatalf("want 修正后回答 got %q", got)
	}
}

// TestRespondToolPlainTextArgs 模型把回答文本直接当 args 传（非 JSON 格式）。
func TestRespondToolPlainTextArgs(t *testing.T) {
	a, _, _ := newTestAgent(`{"action":"tool","tool":"respond","args":"你好，我是开阳"}`)
	got, err := a.Run("打招呼")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "你好，我是开阳" {
		t.Fatalf("want 你好，我是开阳 got %q", got)
	}
}

// TestRespondToolThenStop 模型调用 respond 后不再继续循环（respond 是 terminal action）。
func TestRespondToolThenStop(t *testing.T) {
	a, _, llm := newTestAgent(
		`{"action":"tool","tool":"respond","args":"{\"text\":\"完成\"}"}`,
		`{"action":"reply","text":"这轮不该执行"}`,
	)
	got, err := a.Run("任务")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "完成" {
		t.Fatalf("want 完成 got %q", got)
	}
	if llm.calls != 1 {
		t.Fatalf("expected 1 LLM call (respond terminates), got %d", llm.calls)
	}
}

// TestRespondToolInRequiredMode 模拟 tool_choice=required 场景：模型被迫调工具，
// respond 作为合法出口。该测试验证 respond 在多步任务后可作为收尾回答。
func TestRespondToolInRequiredMode(t *testing.T) {
	// 模型先执行一个真实工具，再用 respond 收尾（模拟 required 模式下被迫调工具）
	a, _, _ := newTestAgent(
		`{"action":"tool","tool":"calc","args":"2+3"}`,
		`{"action":"tool","tool":"respond","args":"{\"text\":\"最终答案：5\"}"}`,
	)
	got, err := a.Run("算 2+3 并回答")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "最终答案：5" {
		t.Fatalf("want 最终答案：5 got %q", got)
	}
}

// TestSystemPromptNoXMLFormat 验证系统提示不再包含 XML 双格式指令（避免模型混淆手写 JSON）。
func TestSystemPromptNoXMLFormat(t *testing.T) {
	a, _, _ := newTestAgent()
	prompt := a.SystemPrompt()
	if strings.Contains(prompt, "<tool name=") {
		t.Fatal("system prompt should not contain XML tool tags")
	}
	if strings.Contains(prompt, "<reply>") {
		t.Fatal("system prompt should not contain <reply> tag")
	}
	if !strings.Contains(prompt, "respond") {
		t.Fatal("system prompt should mention respond tool")
	}
}
