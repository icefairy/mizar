package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathBoundary(t *testing.T) {
	// cwd 为空：CLI/TUI 行为不变，原样返回
	if p, err := resolvePath("", "../whatever"); err != nil || p != "../whatever" {
		t.Fatalf("empty cwd should pass through: %q %v", p, err)
	}
	base := filepath.Join(t.TempDir(), "convA")
	got, err := resolvePath(base, "a.txt")
	if err != nil || got != filepath.Join(base, "a.txt") {
		t.Fatalf("relative join: %q %v", got, err)
	}
	// 绝对路径落在 cwd 内允许
	if _, err := resolvePath(base, filepath.Join(base, "sub", "b.txt")); err != nil {
		t.Fatalf("inside absolute should be allowed: %v", err)
	}
	for _, bad := range []string{"../escape.txt", "../../etc/passwd", "/etc/passwd", filepath.Join(base, "..", "x")} {
		if _, err := resolvePath(base, bad); err == nil {
			t.Fatalf("expected escape rejection for %q", bad)
		}
	}
	// 解析时应顺手建好工作空间目录
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		t.Fatalf("workspace dir not created: %v", err)
	}
}

func TestFileToolsHonorCwd(t *testing.T) {
	var writeTool, readTool = findTool(t, "write"), findTool(t, "read")
	convA := filepath.Join(t.TempDir(), "A")
	convB := filepath.Join(t.TempDir(), "B")

	if _, err := writeTool.RunInDir(convA, `{"path":"note.txt","content":"from A"}`); err != nil {
		t.Fatalf("write in A: %v", err)
	}
	out, err := readTool.RunInDir(convA, `{"path":"note.txt"}`)
	if err != nil || !strings.Contains(out, "from A") {
		t.Fatalf("read in A: %q %v", out, err)
	}
	// 同一个相对文件名在 B 会话读不到 A 的文件（per-conv 隔离）
	if _, err := readTool.RunInDir(convB, `{"path":"note.txt"}`); err == nil {
		t.Fatal("read in B should not see A's file")
	}
	// 越界读取被拒绝
	if _, err := readTool.RunInDir(convA, `{"path":"../../etc/passwd"}`); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("escape should be rejected, got %v", err)
	}
	// Run（cwd 空）保持老行为：在进程 cwd 下解析
	if _, err := resolvePath("", "note.txt"); err != nil {
		t.Fatal(err)
	}
}

func findTool(t *testing.T, name string) pluginTool {
	t.Helper()
	for _, tl := range All("", nil) {
		if tl.Name == name {
			return pluginTool{Run: tl.Run, RunInDir: tl.RunInDir}
		}
	}
	t.Fatalf("tool %q not found", name)
	return pluginTool{}
}

type pluginTool struct {
	Run      func(string) (string, error)
	RunInDir func(string, string) (string, error)
}
