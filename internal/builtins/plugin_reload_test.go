package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolWritePluginReloadCallback 验证 toolWrite 的热重载回调契约：
// 回调收到写入路径，返回的说明追加到工具输出；nil 回调正常写文件。
// （插件目录/后缀过滤在上层 onPluginFileWritten 包装中，见 cmd/mizar/helpers_test.go）
func TestToolWritePluginReloadCallback(t *testing.T) {
	dir := t.TempDir()
	var got []string
	onChanged := func(path string) string {
		got = append(got, path)
		return "[自动热重载] OK"
	}

	w := toolWrite(onChanged)
	p := filepath.Join(dir, "hello.ts")
	out, err := w.Run(`{"path":"` + jsonEscapePath(p) + `","content":"export function tool_x(){return 1}"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 1 || got[0] != p {
		t.Fatalf("callback got %v, want [%s]", got, p)
	}
	if !strings.Contains(out, "[自动热重载] OK") {
		t.Fatalf("output should append callback note, got: %s", out)
	}

	// 回调返回空 → 不追加内容
	w2 := toolWrite(func(string) string { return "" })
	p2 := filepath.Join(dir, "b.ts")
	out2, err := w2.Run(`{"path":"` + jsonEscapePath(p2) + `","content":"x"}`)
	if err != nil {
		t.Fatalf("write2: %v", err)
	}
	if strings.Contains(out2, "[") || strings.Contains(out2, "\n") {
		t.Fatalf("empty callback result should not append, got: %q", out2)
	}

	// nil 回调 → 正常写文件
	w3 := toolWrite(nil)
	if _, err := w3.Run(`{"path":"` + jsonEscapePath(filepath.Join(dir, "c.txt")) + `","content":"x"}`); err != nil {
		t.Fatalf("write with nil cb: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestToolEditPluginReloadCallback 验证 toolEdit 同样触发热重载回调。
func TestToolEditPluginReloadCallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "edit.ts")
	if err := os.WriteFile(p, []byte("const a = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fired := false
	e := toolEdit(func(path string) string {
		fired = true
		return "[自动热重载] EDIT"
	})
	out, err := e.Run(`{"path":"` + jsonEscapePath(p) + `","oldText":"const a = 1;","newText":"const a = 2;"}`)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !fired {
		t.Fatal("edit should fire reload callback for plugin file")
	}
	if !strings.Contains(out, "[自动热重载] EDIT") {
		t.Fatalf("edit output should append note, got: %s", out)
	}
}

// jsonEscapePath 把本地路径转成嵌入 JSON 字符串的安全形式（仅测试用，处理 Windows 反斜杠）。
func jsonEscapePath(p string) string {
	return strings.ReplaceAll(p, "\\", "\\\\")
}
