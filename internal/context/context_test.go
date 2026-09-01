package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindUpwards 向上递归查找。
func TestFindUpwards(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findUpwards(sub, "AGENTS.md"); got != "" {
		t.Fatalf("expect empty, got %s", got)
	}
	// 在 root 放一个
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findUpwards(sub, "AGENTS.md"); got != filepath.Join(root, "AGENTS.md") {
		t.Fatalf("expect %s, got %s", filepath.Join(root, "AGENTS.md"), got)
	}
}

// TestLoadGlobalLocal 全局 + 局部相加。
func TestLoadGlobalLocal(t *testing.T) {
	// 用临时 HOME 隔离全局文件
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".mizar"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 全局 AGENTS.md
	global := "全局规则：永远用中文回复"
	if err := os.WriteFile(filepath.Join(home, ".mizar", "AGENTS.md"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	// 局部 AGENTS.md
	work := t.TempDir()
	local := "项目规则：测试必须全绿"
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	out := Load(work)
	if !strings.Contains(out, "全局规则") || !strings.Contains(out, "项目规则") {
		t.Fatalf("expect both global+local, got: %s", out)
	}
	// 全局在前
	if strings.Index(out, "全局规则") > strings.Index(out, "项目规则") {
		t.Fatalf("global should come first: %s", out)
	}
}

// TestLoadNone 无文件返回空。
func TestLoadNone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if out := Load(t.TempDir()); out != "" {
		t.Fatalf("expect empty, got %s", out)
	}
}

// TestContextFilesMultiType 多类型上下文文件混合加载。
func TestContextFilesMultiType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".mizar"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 全局 AGENTS.md
	if err := os.WriteFile(filepath.Join(home, ".mizar", "AGENTS.md"), []byte("global"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 局部 .cursorrules
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".cursorrules"), []byte("cursorrules"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 局部 USER.md
	if err := os.WriteFile(filepath.Join(work, "USER.md"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := LoadContextFiles(work)
	if !strings.Contains(out, "global") || !strings.Contains(out, "cursorrules") || !strings.Contains(out, "user") {
		t.Fatalf("expect all three, got: %s", out)
	}
	// 全局在前
	gi := strings.Index(out, "global")
	ui := strings.Index(out, "cursorrules")
	if gi > ui {
		t.Fatalf("global should come before cursorrules: %s", out)
	}
}

// TestLoadSoul 加载 SOUL.md。
func TestLoadSoul(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".mizar"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "我是开阳 Agent，精通全栈开发"
	if err := os.WriteFile(SoulPath(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadSoul(); got != content {
		t.Fatalf("expect %q, got %q", content, got)
	}
}

// TestLoadSoulNotExist 无 SOUL.md 返回空。
func TestLoadSoulNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := LoadSoul(); got != "" {
		t.Fatalf("expect empty, got %q", got)
	}
}
