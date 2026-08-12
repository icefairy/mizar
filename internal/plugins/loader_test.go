package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mizar/internal/engine"
)

func host() *engine.HostFuncs {
	return &engine.HostFuncs{
		LLMChat: func(messagesJSON string) (string, error) { return "llm", nil },
		Log:     func(msg string) {},
	}
}

// writeTS 写一个测试插件文件。
func writeTS(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndCall(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "a.ts", `export function tool_hello(args: string): string { return "hi:" + args; }
export function tool_noop(): string { return "ok"; }`)
	m := NewManager(dir, host())
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	if len(loaded) != 1 {
		t.Fatalf("want 1 loaded got %v", loaded)
	}
	tools := m.Tools()
	if len(tools) != 2 {
		t.Fatalf("want 2 tools got %d: %v", len(tools), tools)
	}
	out, err := m.Call("hello", "mizar")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hi:mizar" {
		t.Fatalf("want hi:mizar got %q", out)
	}
	// 未知工具
	if _, err := m.Call("nope", ""); err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestReloadDetectsChange(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "b.ts", `export function tool_v(): string { return "v1"; }`)
	m := NewManager(dir, host())
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	out, _ := m.Call("v", "")
	if out != "v1" {
		t.Fatalf("want v1 got %s", out)
	}
	// 修改后重载
	time.Sleep(10 * time.Millisecond)
	writeTS(t, dir, "b.ts", `export function tool_v(): string { return "v2"; }`)
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	out, _ = m.Call("v", "")
	if out != "v2" {
		t.Fatalf("want v2 got %s", out)
	}
}

func TestLoadErrorsReported(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "bad.ts", `export function broken( {`)
	m := NewManager(dir, host())
	_, failed := m.LoadAll()
	if len(failed) != 1 {
		t.Fatalf("want 1 failure got %v", failed)
	}
	// 无 tool_ 导出
	writeTS(t, dir, "empty.ts", `export function helper(): string { return "x"; }`)
	m2 := NewManager(dir, host())
	_, failed = m2.LoadAll()
	if len(failed) != 2 {
		t.Fatalf("want 2 failures got %v", failed)
	}
}

func TestTimeout(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "slow.ts", `
export function tool_slow(args: string): string {
    const end = Date.now() + 5000;
    while (Date.now() < end) {}
    return "done";
}`)
	m := NewManager(dir, host())
	m.SetMaxExec(200 * time.Millisecond)
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	if _, err := m.Call("slow", ""); err == nil {
		t.Fatal("expected timeout error")
	}
}
