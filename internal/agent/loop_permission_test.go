package agent

import (
	"strings"
	"sync/atomic"
	"testing"

	"mizar/internal/plugins"
)

// ============================================================================
// GAP-S2：runOneToolCall 权限门禁路径（loop.go Plugins.Call 之前）
// ============================================================================

// permHarness 带权限门禁的测试 agent：注册 edit/read 两个内置工具，
// edit 每次真实执行都把 flag 置真（验证「拒绝 = 没执行」）。
type permHarness struct {
	a       *Agent
	pm      *plugins.Manager
	editRun atomic.Bool
	readRun atomic.Bool
	gate    *PermissionGate
}

func newPermHarness(mode PermissionMode) *permHarness {
	m := plugins.NewManager("", nil)
	h := &permHarness{pm: m}
	m.RegisterBuiltin(plugins.Tool{Name: "edit", Description: "t",
		Run: func(string) (string, error) { h.editRun.Store(true); return "edited", nil }})
	m.RegisterBuiltin(plugins.Tool{Name: "read", Description: "t",
		Run: func(string) (string, error) { h.readRun.Store(true); return "content", nil }})
	h.gate = NewPermissionGate(mode)
	llm := &scriptLLM{fn: func([]Message) (string, error) { return `{"action":"reply","text":"done"}`, nil }}
	h.a = New(llm, m)
	h.a.Permission = h.gate
	return h
}

// TestRunPermissionDenyWriteInPlanMode plan 模式拒写类：edit 不执行、
// 拒绝消息成对回填（KindToolCall/KindToolResult）、模型收到「权限门禁」原因。
func TestRunPermissionDenyWriteInPlanMode(t *testing.T) {
	h := newPermHarness(PermPlan)
	var sawDenyResult bool
	llm := h.a.LLM.(*scriptLLM)
	step := 0
	llm.fn = func(msgs []Message) (string, error) {
		step++
		if step == 1 {
			return `{"action":"tool","tool":"edit","args":"{\"path\":\"a.txt\"}"}`, nil
		}
		// 第 2 步应已收到成对拒绝结果
		for _, m := range msgs {
			if m.Kind == KindToolResult && strings.Contains(m.Content, "权限门禁") {
				sawDenyResult = true
			}
		}
		return `{"action":"reply","text":"改用只读方案"}`, nil
	}
	reply, err := h.a.Run("改文件")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reply != "改用只读方案" {
		t.Fatalf("reply = %q", reply)
	}
	if h.editRun.Load() {
		t.Fatal("被拒的 edit 不应真实执行")
	}
	if !sawDenyResult {
		t.Fatal("模型应收到成对的拒绝工具结果")
	}
}

// TestRunPermissionDenySkipsRemainingBatch 一批两个调用，第一个被拒：
// 第二个不执行（controlNextStep 跳过本批剩余），且无孤儿工具结果。
func TestRunPermissionDenySkipsRemainingBatch(t *testing.T) {
	h := newPermHarness(PermPlan)
	llm := h.a.LLM.(*scriptLLM)
	step := 0
	llm.fn = func([]Message) (string, error) {
		step++
		if step == 1 {
			return `{"action":"tool","calls":[` +
				`{"tool":"edit","args":"{\"path\":\"a.txt\"}"},` +
				`{"tool":"read","args":"{\"path\":\"b.txt\"}"}]}`, nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}
	if _, err := h.a.Run("批量任务"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.editRun.Load() || h.readRun.Load() {
		t.Fatal("第一个被拒后，本批剩余调用（含只读的 read）不应执行")
	}
}

// TestRunPermissionAllowAuto auto 模式行为与无 gate 一致（出厂默认回归保护）。
func TestRunPermissionAllowAuto(t *testing.T) {
	h := newPermHarness(PermAuto)
	llm := h.a.LLM.(*scriptLLM)
	calls := 0
	llm.fn = func([]Message) (string, error) {
		calls++
		if calls == 1 {
			return `{"action":"tool","tool":"edit","args":"{\"path\":\"a.txt\"}"}`, nil
		}
		return `{"action":"reply","text":"ok"}`, nil
	}
	if _, err := h.a.Run("x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !h.editRun.Load() {
		t.Fatal("auto 模式下 edit 应直接执行（与无 gate 行为一致）")
	}
}

// TestRunPermissionNilGateIsNoOp nil gate = 现行为（未接线的旧配置零影响）。
func TestRunPermissionNilGateIsNoOp(t *testing.T) {
	m := plugins.NewManager("", nil)
	ran := atomic.Bool{}
	m.RegisterBuiltin(plugins.Tool{Name: "edit", Description: "t",
		Run: func(string) (string, error) { ran.Store(true); return "edited", nil }})
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, m := range msgs {
			if m.Kind == KindToolResult {
				return `{"action":"reply","text":"ok"}`, nil
			}
		}
		return `{"action":"tool","tool":"edit","args":"{\"p\":1}"}`, nil
	}}
	a := New(llm, m) // a.Permission 未设置
	if _, err := a.Run("x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ran.Load() {
		t.Fatal("nil gate 应保持现行为（工具直接执行）")
	}
}

// TestRunPermissionDefaultModeAsksInLoop default 模式下写类经 AskFunc 确认：
// 拒绝 → 不执行；「本会话允许」→ 执行且下次同参数不再询问。
func TestRunPermissionDefaultModeAsksInLoop(t *testing.T) {
	h := newPermHarness(PermDefault)
	answers := []string{
		`[{"id":"perm-confirm","value":"拒绝"}]`,
		`[{"id":"perm-confirm","value":"本会话允许"}]`,
	}
	askIdx := 0
	h.gate.SetAskUser(func(questionsJSON string) (string, error) {
		ans := answers[askIdx]
		askIdx++
		return ans, nil
	})
	llm := h.a.LLM.(*scriptLLM)
	calls := 0
	llm.fn = func([]Message) (string, error) {
		calls++
		if calls <= 2 {
			return `{"action":"tool","tool":"edit","args":"{\"path\":\"same.txt\"}"}`, nil
		}
		return `{"action":"reply","text":"ok"}`, nil
	}
	if _, err := h.a.Run("x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if askIdx != 2 {
		t.Fatalf("应恰好确认 2 次（第一次拒绝、第二次本会话允许），实际 %d", askIdx)
	}
	if !h.editRun.Load() {
		t.Fatal("第二次（本会话允许）应执行")
	}
}
