package agent

import (
	"errors"
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

type panicLLM struct{}

func (panicLLM) Chat(messages []Message) (string, error) {
	panic("LLM 客户端炸了")
}

// Agent.Run 是所有入口（TUI / HTTP / RPC / WebUI）的唯一收口点：
// 任何内部 panic 都必须转成 error，不能让整个进程退出。
func TestAgentRunRecoversPanicFromLLM(t *testing.T) {
	pm := plugins.NewManager(t.TempDir(), &engine.HostFuncs{})
	a := New(panicLLM{}, pm)
	a.MaxSteps = 1

	reply, err := a.Run("test")
	if err == nil {
		t.Fatalf("panic 应转成 error，实际 reply=%q", reply)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("错误信息应包含 panicked: %v", err)
	}
	if reply != "" {
		t.Errorf("panic 后 reply 应为空，得到 %q", reply)
	}
}

// hook panic 不应中断 Agent（挂载点是旁路增强）。
func TestHookPanicDoesNotBreakRun(t *testing.T) {
	h := NewHooks()
	h.OnRunStart(func(ctx *HookContext) error { panic("hook 炸了") })
	// 直接验证 fire 的隔离性
	var logged string
	h.fire("RunStart", h.RunStart, &HookContext{}, func(s string) { logged = s })
	if !strings.Contains(logged, "panic") {
		t.Fatalf("panic 应被记录，实际 %q", logged)
	}
}

var _ = errors.New
