package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWasmDemoPlugin 加载仓库里真实的 extensions/wasm_demo.ts，
// 验证「插件 → 宿主函数 wasm_invoke → Zig wasm 内核」这条链路端到端可用。
func TestWasmDemoPlugin(t *testing.T) {
	src := filepath.Join("..", "..", "extensions", "wasm_demo.ts")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("跳过：未找到 %s (%v)", src, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wasm_demo.ts"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(dir, host())
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("插件加载失败: %v", failed)
	}

	// 1) 内核清单里应能看到内嵌的 upper
	out, err := m.Call("wasm_kernels", "")
	if err != nil {
		t.Fatalf("wasm_kernels 调用失败: %v", err)
	}
	if !strings.Contains(out, "upper") {
		t.Fatalf("内核清单未包含 upper: %s", out)
	}

	// 2) 真正走 wasm 内核做转换
	out, err = m.Call("zig_upper", "hello zig")
	if err != nil {
		t.Fatalf("zig_upper 调用失败: %v", err)
	}
	if strings.TrimSpace(out) != "HELLO ZIG" {
		t.Fatalf("zig_upper 返回 %q, want HELLO ZIG", out)
	}
}

// TestWasmDemoPluginDegrades 模拟旧版 Mizar（宿主无 wasm 函数）时插件仍能加载并降级。
func TestWasmDemoPluginDegrades(t *testing.T) {
	dir := t.TempDir()
	// 一个「假装没有宿主持久化能力」的插件：未知函数走 typeof 判断后回退。
	writeTS(t, dir, "fallback.ts", `
declare const not_exist_host_fn: any;
export function tool_upper(args: string): string {
    if (typeof not_exist_host_fn !== "function") {
        return args.toUpperCase();
    }
    return "never";
}`)
	m := NewManager(dir, host())
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}
	out, err := m.Call("upper", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ABC" {
		t.Fatalf("降级结果 = %q, want ABC", out)
	}
}
