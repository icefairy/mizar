package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// .js 插件免编译直喂 goja（少一道 esbuild 转译）；.ts 插件仍走编译。
func TestLoadJSWithoutCompile(t *testing.T) {
	dir := t.TempDir()
	// 纯 JS 插件：goja 直接执行（不经 esbuild），
	// 所以不能用 ESM 的 export 关键字，用普通函数声明即可（goja 建全局函数）。
	writeTS(t, dir, "plain.js", `function tool_js(args) { return "js:" + args; }
function tool_jsnum() { return 42; }`)
	m := NewManager(dir, host())
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("unexpected failures: %v", failed)
	}
	if len(loaded) != 1 || loaded[0] != "plain.js" {
		t.Fatalf("want plain.js loaded, got %v", loaded)
	}
	// 调用验证（工具名去 tool_ 前缀）
	out, err := m.Call("js", "hello")
	if err != nil {
		t.Fatalf("call tool_js: %v", err)
	}
	if out != "js:hello" {
		t.Fatalf("want js:hello got %s", out)
	}
	// 数字返回值也要能转字符串
	out2, err := m.Call("jsnum", "")
	if err != nil || out2 != "42" {
		t.Fatalf("tool_jsnum: %q %v", out2, err)
	}
}

// .js 与 .ts 混装目录都能加载
func TestLoadMixedJSTS(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "a.ts", `export function tool_ts(args: string): string { return "ts:" + args; }`)
	writeTS(t, dir, "b.js", `function tool_js(args) { return "js:" + args; }`)
	m := NewManager(dir, host())
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("unexpected failures: %v", failed)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 loaded, got %v", loaded)
	}
	tools := m.Tools()
	got := map[string]bool{}
	for _, t := range tools {
		got[t.Name] = true
	}
	for _, name := range []string{"ts", "js"} {
		if !got[name] {
			t.Fatalf("tool %s missing, have %v", name, got)
		}
	}
	// 文件确实存在（避免误删）
	if _, err := os.Stat(filepath.Join(dir, "b.js")); err != nil {
		t.Fatalf("b.js missing: %v", err)
	}
}
