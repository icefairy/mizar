package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// callStr 调引擎函数并转字符串（e.Call 返回 any）。
func callStr(t *testing.T, e *Engine, fn string, args ...any) string {
	t.Helper()
	out, err := e.Call(fn, args...)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return fmt.Sprintf("%v", out)
}

// TestStdlibCountTokens 验证 count_tokens 估算与压缩器同口径。
func TestStdlibCountTokens(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	// 中文按字符：5 个汉字 = 5
	if got := callStr(t, e, "count_tokens", "你好世界啊"); got != "5" {
		t.Fatalf("中文 count_tokens = %q, want 5", got)
	}
	// 英文按 4 字符/token：20 字符 = 5（向上取整）
	if got := callStr(t, e, "count_tokens", "abcdefghijklmnopqrst"); got != "5" {
		t.Fatalf("英文 count_tokens = %q, want 5", got)
	}
	// 空串 = 0
	if got := callStr(t, e, "count_tokens", ""); got != "0" {
		t.Fatalf("空串 count_tokens = %q, want 0", got)
	}
	// 混合：2 中文 + 5 英文 = 2 + ceil(5/4) = 2+2 = 4
	if got := callStr(t, e, "count_tokens", "你好hello"); got != "4" {
		t.Fatalf("混合 count_tokens = %q, want 4", got)
	}
}

// TestStdlibTimeNow 验证 time_now 返回可解析的 RFC3339 时间。
func TestStdlibTimeNow(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	out := callStr(t, e, "time_now")
	if _, err := time.Parse(time.RFC3339Nano, out); err != nil {
		t.Fatalf("time_now %q 不是合法时间: %v", out, err)
	}
	unix := callStr(t, e, "time_unix")
	if strings.TrimSpace(unix) == "" || strings.Contains(unix, " ") {
		t.Fatalf("time_unix %q 应为数字", unix)
	}
}

// TestStdlibUUID 验证 uuid 格式：8-4-4-4-12，版本 4。
func TestStdlibUUID(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	a := callStr(t, e, "uuid")
	b := callStr(t, e, "uuid")
	if a == b {
		t.Fatalf("两次 uuid 相同: %s", a)
	}
	parts := strings.Split(a, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("uuid 格式非法: %s", a)
	}
	if parts[2][0] != '4' {
		t.Fatalf("uuid 版本应为 4: %s", a)
	}
}

// TestStdlibBase64 验证 base64 编解码往返。
func TestStdlibBase64(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	enc := callStr(t, e, "base64_encode", "hello 世界")
	if enc != "aGVsbG8g5LiW55WM" {
		t.Fatalf("base64_encode: %q", enc)
	}
	dec := callStr(t, e, "base64_decode", enc)
	if dec != "hello 世界" {
		t.Fatalf("base64_decode: %q", dec)
	}
	// 非法输入报错
	if _, err := e.Call("base64_decode", "!!!not-base64!!!"); err == nil {
		t.Fatal("base64_decode 非法输入应报错")
	}
}

// TestStdlibHash 验证 SHA-256 已知向量（"abc"）。
func TestStdlibHash(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	out := callStr(t, e, "hash_sha256", "abc")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if out != want {
		t.Fatalf("hash_sha256(abc) = %q, want %q", out, want)
	}
}

// TestStdlibPath 验证路径函数（变参 path_join）。
func TestStdlibPath(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	// goja 变参：多参数打包进 ...string
	joined := callStr(t, e, "path_join", "/a", "b", "c.ts")
	if joined != "/a/b/c.ts" {
		t.Fatalf("path_join = %q", joined)
	}
	base := callStr(t, e, "path_base", "/a/b/c.ts")
	if base != "c.ts" {
		t.Fatalf("path_base = %q", base)
	}
	dir := callStr(t, e, "path_dir", "/a/b/c.ts")
	if dir != "/a/b" {
		t.Fatalf("path_dir = %q", dir)
	}
}

// TestStdlibURLParse 验证 url_parse 返回结构化 JSON。
func TestStdlibURLParse(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	out := callStr(t, e, "url_parse", "https://user@example.com:8080/api/list?page=2&tag=go#top")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("url_parse 返回非 JSON: %v", err)
	}
	if m["scheme"] != "https" || m["host"] != "example.com:8080" ||
		m["path"] != "/api/list" || m["query"] != "page=2&tag=go" ||
		m["fragment"] != "top" || m["user"] != "user" {
		t.Fatalf("url_parse 字段不符: %v", m)
	}
	// 非法 URL 报错
	if _, err := e.Call("url_parse", "://bad"); err == nil {
		t.Fatal("url_parse 非法输入应报错")
	}
}

// TestStdlibVisibleInPlugin 验证纯函数在 TS 插件里可见可调（与 db_query 同路径）。
func TestStdlibVisibleInPlugin(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	js, _ := CompileTS("std.ts", `export function tool_meta(): string {
  const id = uuid();
  const t = time_now();
  const h = hash_sha256(id);
  return JSON.stringify({id, t, h});
}`)
	if err := e.RunScript("std.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := callStr(t, e, "tool_meta")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("out 非 JSON: %v", err)
	}
	if m["id"] == "" || m["t"] == "" || m["h"] == "" {
		t.Fatalf("tool_meta 字段为空: %v", m)
	}
	if len(m["h"].(string)) != 64 {
		t.Fatalf("hash 长度应为 64: %v", m["h"])
	}
}
