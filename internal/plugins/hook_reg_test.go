package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"mizar/internal/engine"
)

// TestHookOnEngineRegistration 直接验证：engine.New + RunScript 后 hook_on 被调用。
func TestHookOnEngineRegistration(t *testing.T) {
	host := &engine.HostFuncs{Log: func(string) {}}
	registered := false
	host.HookOn = func(event string, cb func(ctxJSON string) (string, error)) error {
		registered = true
		return nil
	}
	e, err := engine.New(host)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	js, _ := engine.CompileTS("h.ts", `hook_on("RunEnd", (ctx: string): string => { return "ok"; });
export function tool_x(): string { return "x"; }`)
	if err := e.RunScript("h.ts", js); err != nil {
		t.Fatalf("run script: %v", err)
	}
	if !registered {
		t.Fatal("hook_on 未被调用")
	}
}

// TestHookOnViaPlugin 复现原问题：插件加载后注册表应有回调。
func TestHookOnViaPlugin(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-hook-reg2-*")
	defer os.RemoveAll(dir)

	os.WriteFile(filepath.Join(dir, "h.ts"), []byte(`
hook_on("RunEnd", (ctx: string): string => { return "ok"; });
export function tool_x(): string { return "x"; }
`), 0o644)

	m := NewManager(dir, &engine.HostFuncs{Log: func(string) {}, DBQuery: engine.DBQueryFn})
	loaded, failed := m.LoadAll()
	t.Logf("loaded: %v, failed: %v", loaded, failed)
	t.Logf("HookCount(RunEnd)=%d", m.HookCount("RunEnd"))

	if m.HookCount("RunEnd") != 1 {
		t.Fatalf("want 1 hook registered, got %d", m.HookCount("RunEnd"))
	}
}
