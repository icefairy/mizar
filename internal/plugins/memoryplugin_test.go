package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"mizar/internal/engine"
)

// 内置 memory.ts 插件：应注册全部 mem_* 工具与 /mem 命令。
func TestMemoryPluginLoads(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "assets", "extensions", "memory.ts"))
	if err != nil {
		t.Skipf("memory.ts 不存在: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.ts"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	for _, name := range []string{
		"mem_write", "mem_search", "mem_list", "mem_get", "mem_delete",
		"mem_stats", "mem_clear",
	} {
		if _, ok := m.Get(name); !ok {
			t.Errorf("工具 %s 未注册", name)
		}
	}
}
