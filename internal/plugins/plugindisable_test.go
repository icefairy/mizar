package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"mizar/internal/engine"
)

// 插件级禁用：禁用后工具/命令不注册；启用后恢复；禁用清单持久化。
func TestPluginEnableDisable(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "a.ts", `export function tool_a(args: string): string { return "a"; }
export function command_a(args: string): string { return "ca"; }`)
	writeTS(t, dir, "b.ts", `export function tool_b(args: string): string { return "b"; }`)

	m := NewManager(dir, host())
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("load: %v", failed)
	}
	if _, ok := m.Get("a"); !ok {
		t.Fatal("工具 a 应已注册")
	}
	if _, ok := m.Get("b"); !ok {
		t.Fatal("工具 b 应已注册")
	}

	// 禁用 a.ts
	m.SetPluginDisabled("a.ts", true)
	if _, ok := m.Get("a"); ok {
		t.Fatal("禁用后工具 a 不应可见")
	}
	if _, ok := m.Get("b"); !ok {
		t.Fatal("禁用 a 不应影响 b")
	}
	if !m.PluginDisabled("a.ts") {
		t.Fatal("PluginDisabled 应为 true")
	}
	// 禁用清单落盘
	if _, err := os.Stat(filepath.Join(dir, ".disabled.json")); err != nil {
		t.Fatalf("禁用清单未落盘: %v", err)
	}
	// ReloadAll 后仍应保持禁用
	if _, failed := m.ReloadAll(); len(failed) > 0 {
		t.Fatalf("reload: %v", failed)
	}
	if _, ok := m.Get("a"); ok {
		t.Fatal("重载后禁用插件不应被加载")
	}

	// 启用 a.ts
	m.SetPluginDisabled("a.ts", false)
	if _, failed := m.ReloadAll(); len(failed) > 0 {
		t.Fatalf("reload after enable: %v", failed)
	}
	if _, ok := m.Get("a"); !ok {
		t.Fatal("启用后工具 a 应恢复")
	}

	// 新 Manager 读禁用清单（模拟重启）
	m.SetPluginDisabled("b.ts", true)
	m2 := NewManager(dir, host())
	m2.LoadDisabledPlugins()
	if _, failed := m2.LoadAll(); len(failed) > 0 {
		t.Fatalf("m2 load: %v", failed)
	}
	if _, ok := m2.Get("b"); ok {
		t.Fatal("重启后 b 应仍被禁用")
	}
	if _, ok := m2.Get("a"); !ok {
		t.Fatal("重启后 a 应加载")
	}
	// PluginFiles/LoadedPlugins/DisabledPlugins
	if len(m2.PluginFiles()) != 2 {
		t.Fatalf("PluginFiles 应为 2，得到 %v", m2.PluginFiles())
	}
	if len(m2.LoadedPlugins()) != 1 {
		t.Fatalf("LoadedPlugins 应为 1，得到 %v", m2.LoadedPlugins())
	}
	if dp := m2.DisabledPlugins(); len(dp) != 1 || dp[0] != "b.ts" {
		t.Fatalf("DisabledPlugins 应为 [b.ts]，得到 %v", dp)
	}
}

var _ = engine.HostFuncs{}
