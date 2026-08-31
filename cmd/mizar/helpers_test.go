package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// newTestAgentForReload 构建用于 onPluginFileWritten 测试的 pm + agent。
func newTestAgentForReload(t *testing.T, pluginDir string) (*plugins.Manager, *agent.Agent) {
	t.Helper()
	host := &engine.HostFuncs{
		LLMChat: func(messagesJSON string) (string, error) { return "llm", nil },
		Log:     func(msg string) {},
	}
	pm := plugins.NewManager(pluginDir, host)
	a := agent.New(nil, pm)
	return pm, a
}

// TestOnPluginFileWritten 验证插件文件写入过滤与自动重载闭环：
// 插件目录内的 .ts/.js → 触发重载并返回说明；其他目录/后缀 → 不触发。
func TestOnPluginFileWritten(t *testing.T) {
	pluginDir := t.TempDir()
	otherDir := t.TempDir()

	pm, a := newTestAgentForReload(t, pluginDir)
	cb := onPluginFileWritten(pluginDir, pm, a)

	// 1) 插件目录内 .ts：写入后触发重载，插件生效
	tsPath := filepath.Join(pluginDir, "hello.ts")
	if err := os.WriteFile(tsPath, []byte("export function tool_ping(args: string): string { return 'pong'; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := cb(tsPath)
	if out == "" {
		t.Fatal("plugin file write should trigger reload note")
	}
	if !strings.Contains(out, "hello.ts") {
		t.Fatalf("reload note should mention loaded plugin, got: %s", out)
	}
	if _, ok := pm.Get("ping"); !ok {
		t.Fatal("tool_ping should be registered after auto reload")
	}
	// 系统提示缓存应已失效（ReloadTools 清空后 SystemPrompt 重建包含新工具）
	if !strings.Contains(a.SystemPrompt(), "ping") {
		t.Fatal("SystemPrompt should contain the new tool after reload")
	}

	// 2) 非插件目录 → 不触发
	if out := cb(filepath.Join(otherDir, "x.ts")); out != "" {
		t.Fatalf("non-plugin dir write should not trigger, got: %s", out)
	}

	// 3) 插件目录内非插件后缀 → 不触发
	if out := cb(filepath.Join(pluginDir, "note.md")); out != "" {
		t.Fatalf("non-plugin ext write should not trigger, got: %s", out)
	}

	// 4) 修改已有插件后回调 → 重载生效
	if err := os.WriteFile(tsPath, []byte("export function tool_ping2(args: string): string { return 'pong2'; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := cb(tsPath); out == "" {
		t.Fatal("modified plugin file should trigger reload")
	}
	if _, ok := pm.Get("ping2"); !ok {
		t.Fatal("tool_ping2 should be registered after modification reload")
	}
}
