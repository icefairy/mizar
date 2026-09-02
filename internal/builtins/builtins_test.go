package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/plugins"
)

func tmpFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(t *testing.T, tool string, args string) (string, error) {
	t.Helper()
	for _, bt := range All("", nil) {
		if bt.Name == tool {
			return bt.Run(args)
		}
	}
	t.Fatalf("tool %s not found", tool)
	return "", nil
}

// TestBash echo + 超时。
func TestBash(t *testing.T) {
	out, err := call(t, "bash", `{"command":"echo hello"}`)
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("bash: out=%q err=%v", out, err)
	}
}

// TestRead 行号 + offset/limit。
func TestRead(t *testing.T) {
	p := tmpFile(t, "a\nb\nc\nd\ne\n")
	out, err := call(t, "read", `{"path":"`+p+`","offset":2,"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2|b") || !strings.Contains(out, "3|c") || strings.Contains(out, "4|d") {
		t.Fatalf("read: %s", out)
	}
}

// TestWrite 自动建父目录。
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "a.txt")
	out, err := call(t, "write", `{"path":"`+p+`","content":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote 2 bytes") {
		t.Fatalf("write: %s", out)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hi" {
		t.Fatalf("content=%q", string(b))
	}
}

// TestEdit 唯一替换 + 非唯一报错。
func TestEdit(t *testing.T) {
	p := tmpFile(t, "hello world hello\n")
	out, err := call(t, "edit", `{"path":"`+p+`","oldText":"world","newText":"mizar"}`)
	if err != nil || !strings.Contains(out, "1 edit") {
		t.Fatalf("edit: out=%q err=%v", out, err)
	}
	// 非唯一
	_, err = call(t, "edit", `{"path":"`+p+`","oldText":"hello","newText":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("expect uniqueness error, got %v", err)
	}
}

// TestLS 目录 + 排序。
func TestLS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	out, err := call(t, "ls", `{"path":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("ls: %s", out)
	}
	if strings.Index(out, "a.txt") > strings.Index(out, "b.txt") {
		t.Fatalf("ls not sorted: %s", out)
	}
}

// TestGrep 内容搜索。
func TestGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.ts"), []byte("const a = 1;\n// TODO fix\nconst b = 2;\n"), 0o644)
	out, err := call(t, "grep", `{"pattern":"TODO","path":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x.ts:2") {
		t.Fatalf("grep: %s", out)
	}
}

// TestFind glob 查找。
func TestFind(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("x"), 0o644)
	out, err := call(t, "find", `{"pattern":"*.ts","path":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.ts") || strings.Contains(out, "b.go") {
		t.Fatalf("find: %s", out)
	}
}

// TestRespondToolRegistered respond 工具已注册进 All()，且参数 schema 含必填 text。
func TestRespondToolRegistered(t *testing.T) {
	tools := All("", nil)
	var found *plugins.Tool
	for i := range tools {
		if tools[i].Name == "respond" {
			found = &tools[i]
			break
		}
	}
	if found == nil {
		t.Fatal("respond tool should be registered in builtins.All()")
	}
	schema := found.ArgsSchema()
	if schema == nil {
		t.Fatal("respond ArgsSchema should not be nil")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %v", schema)
	}
	if _, ok := props["text"]; !ok {
		t.Fatal("respond schema should have text property")
	}
	req, _ := schema["required"].([]string)
	if len(req) != 1 || req[0] != "text" {
		t.Fatalf("respond text should be required, got %v", req)
	}
}

// TestRespondToolRun 直接执行 respond 工具 Run 返回 text 本身。
func TestRespondToolRun(t *testing.T) {
	var found *plugins.Tool
	for _, tt := range All("", nil) {
		if tt.Name == "respond" {
			found = &tt
			break
		}
	}
	if found == nil {
		t.Fatal("respond not found")
	}
	out, err := found.Run(`{"text":"你好"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "你好" {
		t.Fatalf("want 你好 got %q", out)
	}
}
