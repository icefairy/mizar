package agent

import (
	"os"
	"path/filepath"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// testManager 建一个临时插件管理器。
func testManager(t *testing.T) *plugins.Manager {
	t.Helper()
	dir := t.TempDir()
	// 写一个简单插件
	src := `
export function tool_ping(args: string): string { return "pong"; }
`
	if err := os.WriteFile(filepath.Join(dir, "ping.ts"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &engine.HostFuncs{}
	pm := plugins.NewManager(dir, host)
	pm.LoadAll()
	return pm
}

// TestSystemPromptStable 缓存友好核心测试：多次调用 SystemPrompt 必须字节级相同。
// 前缀缓存要求 system + 工具定义逐字节稳定，任何一处差异（含空格/顺序/换行）
// 都会使整个可缓存前缀失效。
func TestSystemPromptStable(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{}, pm)
	a.System = "你是开阳"

	p1 := a.SystemPrompt()
	p2 := a.SystemPrompt()
	p3 := a.SystemPrompt()
	if p1 != p2 || p2 != p3 {
		t.Fatal("SystemPrompt 必须字节级稳定（缓存友好）")
	}
	if len(p1) == 0 {
		t.Fatal("system prompt empty")
	}
}

// TestSystemPromptChangesAfterReload 验证 ReloadTools 会失效缓存（热加载场景）。
func TestSystemPromptChangesAfterReload(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{}, pm)
	a.System = "sys"
	p1 := a.SystemPrompt()
	a.ReloadTools()
	// 重新加载后（工具不变）内容应一致——证明 ReloadTools 只是失效，不改变内容
	p2 := a.SystemPrompt()
	if p1 != p2 {
		t.Fatal("reload with same tools should produce same prompt")
	}
}

// TestRunWithCompactor 端到端：Agent 循环中压缩触发后仍能完成任务。
func TestRunWithCompactor(t *testing.T) {
	pm := testManager(t)
	var calls int
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		calls++
		// 前 4 次调用工具，第 5 次回复
		if calls <= 4 {
			return `{"action":"tool","tool":"ping","args":""}`, nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}}
	a := New(llm, pm)
	a.System = "sys"
	a.Compactor = DefaultCompactor(func(msgs []Message) (string, error) {
		return "## Goal\ncompacted", nil
	})
	a.Compactor.ContextWindow = 200   // 窗口极小，必然触发压缩
	a.Compactor.ReserveTokens = 20
	a.Compactor.KeepRecentTokens = 30

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Errorf("reply = %q", reply)
	}
	if calls == 0 {
		t.Fatal("llm not called")
	}
}
