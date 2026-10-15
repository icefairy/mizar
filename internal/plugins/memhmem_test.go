package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// 两个记忆插件（本地 SQLite 版 + HMEM 服务端版）都能加载并注册各自的工具集。
func TestBothMemoryPluginsLoad(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"memory.ts", "memory_hmem.ts"} {
		src, err := os.ReadFile(filepath.Join("..", "assets", "extensions", name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())

	m := NewManager(dir, hostWithMemDB(t))
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}

	// SQLite 版工具
	for _, name := range []string{"mem_write", "mem_search", "mem_list", "mem_get", "mem_delete", "mem_stats", "mem_clear"} {
		if _, ok := m.Get(name); !ok {
			t.Errorf("memory.ts 工具 %s 未注册", name)
		}
	}
	// HMEM 版工具
	for _, name := range []string{"mem_write", "mem_search", "mem_list", "mem_get", "mem_delete", "mem_stats", "mem_reflect", "mem_models", "mem_graph", "mem_namespaces"} {
		if _, ok := m.Get(name); !ok {
			t.Errorf("memory_hmem.ts 工具 %s 未注册", name)
		}
	}

	// 两个文件都被加载
	loaded := m.LoadedPlugins()
	if len(loaded) != 2 {
		t.Fatalf("应加载 2 个插件，得到 %v", loaded)
	}
}

// 内置资产里的默认 .disabled.json 应默认禁用 memory_hmem.ts（SQLite 版为默认后端）。
func TestDefaultDisabledPluginsAsset(t *testing.T) {
	dir := t.TempDir()
	// 模拟释放后的插件目录：两个插件 + .disabled.json
	for _, name := range []string{"memory.ts", "memory_hmem.ts", ".disabled.json"} {
		src, err := os.ReadFile(filepath.Join("..", "assets", "extensions", name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", t.TempDir())

	m := NewManager(dir, hostWithMemDB(t))
	m.LoadDisabledPlugins()
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}
	if !m.PluginDisabled("memory_hmem.ts") {
		t.Fatal("默认应禁用 memory_hmem.ts")
	}
	if m.PluginDisabled("memory.ts") {
		t.Fatal("默认不应禁用 memory.ts")
	}
	if _, ok := m.Get("mem_reflect"); ok {
		t.Fatal("HMEM 版被禁用时不应注册 mem_reflect")
	}
	if _, ok := m.Get("mem_clear"); !ok {
		t.Fatal("SQLite 版应注册 mem_clear")
	}
}
