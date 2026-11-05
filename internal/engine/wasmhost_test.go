package engine

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// runPluginScript 起一个引擎跑一段插件风格的 JS，返回最后表达式的值。
// 复用 New(nil) 的默认宿主函数，不依赖外部配置。
func runPluginScript(t *testing.T, src string) string {
	t.Helper()
	e, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	script := "function __probe() {\n" + src + "\n}"
	if err := e.RunScript("probe.js", script); err != nil {
		t.Fatalf("RunScript: %v\n脚本:\n%s", err, script)
	}
	v, err := e.Call("__probe")
	if err != nil {
		t.Fatalf("Call __probe: %v\n脚本:\n%s", err, script)
	}
	return fmt.Sprint(v)
}

// TestWasmInvokeUpper 验证插件里能直接调 Zig 内核做文本处理。
func TestWasmInvokeUpper(t *testing.T) {
	out := runPluginScript(t, `return wasm_invoke("upper", "upper", "hello zig");`)
	if out != "HELLO ZIG" {
		t.Fatalf("wasm_invoke 返回 %q, want %q", out, "HELLO ZIG")
	}
}

// TestWasmKernelsLists 验证插件能探测可用内核（用于降级判断）。
func TestWasmKernelsLists(t *testing.T) {
	out := runPluginScript(t, `return wasm_kernels();`)
	if !strings.Contains(out, "upper") {
		t.Fatalf("内核清单未包含 upper: %s", out)
	}
}

// TestWasmInvokeB64Roundtrip 验证二进制进出的 base64 通道。
func TestWasmInvokeB64Roundtrip(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("abc"))
	out := runPluginScript(t, `return wasm_invoke_b64("upper", "upper", "`+b64+`");`)
	got, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		t.Fatalf("输出不是 base64: %q", out)
	}
	if string(got) != "ABC" {
		t.Fatalf("wasm_invoke_b64 返回 %q, want %q", got, "ABC")
	}
}

// TestWasmInvokeMissingKernel 内核缺失时插件拿到的是可捕获的错误，而不是崩溃。
func TestWasmInvokeMissingKernel(t *testing.T) {
	out := runPluginScript(t, `
		let msg = "no-error";
		try { wasm_invoke("nope", "fn", "x"); } catch (e) { msg = "caught"; }
		return msg;
	`)
	if out != "caught" {
		t.Fatalf("缺失内核应在 JS 侧抛错，实际返回 %q", out)
	}
}
