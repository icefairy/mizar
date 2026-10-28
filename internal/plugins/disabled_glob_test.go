package plugins

import (
	"testing"
)

// TestDisabledToolsGlob glob 模式禁用（对齐 pi 1.0.4 的 --tools '*' 通配）。
//
// 典型用法：mcp__* 一次关掉某个 MCP server 的全部工具。
func TestDisabledToolsGlob(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, host())
	ok := func(string) (string, error) { return "ok", nil }
	for _, n := range []string{"bash", "grep", "mcp__redis__get", "mcp__redis__set", "mcp__pg__query"} {
		m.RegisterBuiltin(Tool{Name: n, Description: n, Run: ok})
	}

	m.SetDisabledTools([]string{"mcp__redis__*"})

	// 前缀匹配的两个被禁用
	for _, n := range []string{"mcp__redis__get", "mcp__redis__set"} {
		if _, found := m.Get(n); found {
			t.Errorf("glob 应禁用 %s", n)
		}
	}
	// 其他 server 与普通工具不受影响
	for _, n := range []string{"mcp__pg__query", "bash", "grep"} {
		if _, found := m.Get(n); !found {
			t.Errorf("glob 不应误伤 %s", n)
		}
	}
}

// TestDisabledToolsExactAndGlob 精确名与通配符共存。
func TestDisabledToolsExactAndGlob(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, host())
	ok := func(string) (string, error) { return "ok", nil }
	for _, n := range []string{"bash", "grep", "mcp__a__x", "mcp__b__y"} {
		m.RegisterBuiltin(Tool{Name: n, Description: n, Run: ok})
	}

	m.SetDisabledTools([]string{"bash", "mcp__a__*"})

	if _, found := m.Get("bash"); found {
		t.Error("精确名 bash 应被禁用")
	}
	if _, found := m.Get("mcp__a__x"); found {
		t.Error("glob mcp__a__* 应禁用 mcp__a__x")
	}
	if _, found := m.Get("mcp__b__y"); !found {
		t.Error("mcp__b__y 不应被禁用")
	}
	if _, found := m.Get("grep"); !found {
		t.Error("grep 不应被禁用")
	}
}

// TestDisabledToolsAllGlob 全通配 * 禁用一切。
func TestDisabledToolsAllGlob(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, host())
	ok := func(string) (string, error) { return "ok", nil }
	m.RegisterBuiltin(Tool{Name: "bash", Description: "b", Run: ok})
	m.RegisterBuiltin(Tool{Name: "grep", Description: "g", Run: ok})

	m.SetDisabledTools([]string{"*"})
	if got := m.Tools(); len(got) != 0 {
		t.Fatalf("* 应禁用全部，仍有 %d 个: %v", len(got), got)
	}
}

// TestDisabledToolsGlobDoesNotAffectPrompt 禁用只动工具注册表，不影响提示词。
func TestDisabledToolsGlobDoesNotAffectPrompt(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, host())
	ok := func(string) (string, error) { return "ok", nil }
	m.RegisterBuiltin(Tool{Name: "bash", Description: "执行命令", Run: ok})
	m.RegisterBuiltin(Tool{Name: "mcp__x__y", Description: "mcp 工具", Run: ok})

	// 禁用后工具注册表变小，但命名快照（提示词来源）应保持不变。
	before := len(m.Tools())
	m.SetDisabledTools([]string{"mcp__*"})
	if after := len(m.Tools()); after != before-1 {
		t.Fatalf("Tools() 应从 %d 减为 %d，实际 %d", before, before-1, after)
	}
}
