package plugins

import (
	"strings"
	"testing"
)

// TestDisabledTools 禁用工具不注册、不可调用（不影响提示词/技能渲染层）。
func TestDisabledTools(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, host())
	m.RegisterBuiltin(Tool{Name: "bash", Description: "执行命令", Run: func(string) (string, error) { return "ok", nil }})
	m.RegisterBuiltin(Tool{Name: "grep", Description: "搜索", Run: func(string) (string, error) { return "ok", nil }})

	m.SetDisabledTools([]string{"bash"})

	// Tools() 不再返回禁用的 bash
	for _, tl := range m.Tools() {
		if tl.Name == "bash" {
			t.Fatal("disabled tool still in Tools()")
		}
	}

	// Get 返回 false
	if _, ok := m.Get("bash"); ok {
		t.Fatal("Get should return false for disabled tool")
	}

	// Call 报 not found
	if _, err := m.Call("bash", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Call disabled should fail: %v", err)
	}

	// 未禁用的正常
	if _, ok := m.Get("grep"); !ok {
		t.Fatal("enabled tool should be available")
	}
	if _, err := m.Call("grep", ""); err != nil {
		t.Fatalf("grep should work: %v", err)
	}
}
