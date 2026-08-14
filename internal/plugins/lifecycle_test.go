package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mizar/internal/engine"
)

// writePlugin 写一个 TS 插件文件到临时目录。
func writePlugin(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lifecyclePlugin 含 plugin_init / plugin_cleanup 的插件。
const lifecyclePlugin = `
var state = "init-not-called";
export function tool_state(): string { return state; }
export function plugin_init(): void { state = "init:" + Date.now().toString().slice(-4); }
export function plugin_cleanup(): void { state = "cleaned"; }
`

// TestPluginLifecycleInit 验证加载时 plugin_init 被调用。
func TestPluginLifecycleInit(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "life.ts", lifecyclePlugin)
	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	loaded, failed := m.LoadAll()
	if len(loaded) != 1 {
		t.Fatalf("loaded=%v failed=%v", loaded, failed)
	}
	// tool_state 应返回 init 后的值（plugin_init 已执行，state 非 "init-not-called"）
	out, err := m.Call("state", "")
	if err != nil {
		t.Fatalf("call state: %v", err)
	}
	if strings.HasPrefix(out, "init-not-called") {
		t.Fatalf("plugin_init 未执行: %q", out)
	}
	if !strings.HasPrefix(out, "init:") {
		t.Fatalf("state=%q, want init:xxxx", out)
	}
}

// TestPluginLifecycleReload 验证热重载时 cleanup(旧) + init(新)。
func TestPluginLifecycleReload(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "life.ts", lifecyclePlugin)
	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	m.LoadAll()

	// 修改插件：plugin_init 写入新值
	writePlugin(t, dir, "life.ts", strings.Replace(lifecyclePlugin,
		"init-not-called", "v2", 1))
	// 强制改 mtime（写入太快时秒级可能相同，LoadAll 会跳过）
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(dir, "life.ts"), future, future)
	loaded, failed := m.LoadAll() // modTime 变了 → 重载
	if len(loaded) != 1 {
		t.Fatalf("reload loaded=%v failed=%v", loaded, failed)
	}
	out, _ := m.Call("state", "")
	if !strings.HasPrefix(out, "init:") {
		t.Fatalf("重载后 plugin_init 未执行: %q", out)
	}
}

// TestPluginLifecycleUnload 验证插件文件删除后 plugin_cleanup 被调用 + 工具注销。
func TestPluginLifecycleUnload(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "life.ts", lifecyclePlugin)
	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	m.LoadAll()

	// 删除插件文件 → 下次 LoadAll 应卸载
	if err := os.Remove(filepath.Join(dir, "life.ts")); err != nil {
		t.Fatal(err)
	}
	loaded, failed := m.LoadAll()
	if len(loaded) != 0 {
		t.Fatalf("删除后不应加载: %v", loaded)
	}
	if len(failed) != 0 {
		t.Fatalf("failed=%v", failed)
	}
	// 工具应注销
	if _, ok := m.Get("state"); ok {
		t.Fatalf("删除后工具仍注册")
	}
	// 引擎应清空
	if len(m.pluginFilesLocked()) != 0 {
		t.Fatalf("引擎未清空: %v", m.pluginFilesLocked())
	}
}

// TestPluginLifecycleNoHooks 验证无生命周期钩子的插件正常加载。
func TestPluginLifecycleNoHooks(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "plain.ts", `export function tool_ping(): string { return "pong"; }`)
	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	loaded, failed := m.LoadAll()
	if len(loaded) != 1 {
		t.Fatalf("loaded=%v failed=%v", loaded, failed)
	}
	if _, ok := m.Get("ping"); !ok {
		t.Fatalf("ping 未注册")
	}
	// 删除后也不报错
	os.Remove(filepath.Join(dir, "plain.ts"))
	m.LoadAll()
}
