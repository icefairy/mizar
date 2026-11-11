package caps

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGetBaseline Get 必须返回可用快照：GOOS 与运行平台一致、至少能找到 sh 或 cmd。
func TestGetBaseline(t *testing.T) {
	e := Get()
	if e.GOOS != runtime.GOOS {
		t.Fatalf("GOOS 不符: %s != %s", e.GOOS, runtime.GOOS)
	}
	if e.Tools == nil {
		t.Fatal("Tools 为 nil")
	}
	if runtime.GOOS != "windows" && !e.Has("sh") && !e.Has("bash") {
		t.Skipf("本机 PATH 里既无 sh 也无 bash（跳过）：%v", e.Tools)
	}
}

// TestNotesNoAwk 缺 awk 时 Notes 必须明确告知（Android 高频坑）。
func TestNotesNoAwk(t *testing.T) {
	e := Env{GOOS: "linux", Tools: map[string]string{}}
	notes := e.Notes()
	found := false
	for _, n := range notes {
		if contains(n, "没有 awk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺 awk 未在 Notes 中告知: %v", notes)
	}
}

// TestNotesAndroid Android 必须告知 mksh/toybox 与 POSIX 限制。
func TestNotesAndroid(t *testing.T) {
	e := Env{GOOS: "linux", Android: true, Tools: map[string]string{"awk": "/x/awk"}}
	notes := e.Notes()
	var hasMksh, hasNonTermux bool
	for _, n := range notes {
		if contains(n, "mksh") && contains(n, "toybox") {
			hasMksh = true
		}
		if contains(n, "非 Termux") {
			hasNonTermux = true
		}
	}
	if !hasMksh {
		t.Fatalf("Android 未告知 mksh/toybox: %v", notes)
	}
	if !hasNonTermux {
		t.Fatalf("非 Termux 未告知私有目录限制: %v", notes)
	}
}

// TestDetectTempDirWritable 探测到的临时目录必须真实可写。
func TestDetectTempDirWritable(t *testing.T) {
	dir := detectTempDir()
	if dir == "" {
		t.Skip("本机无任何可写临时目录")
	}
	f, err := os.CreateTemp(dir, "caps-test-*")
	if err != nil {
		t.Fatalf("探测出的临时目录不可写 %s: %v", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

// TestDetectTempDirFallback TMPDIR 指向不存在的路径时，仍应兜底成功。
func TestDetectTempDirFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(tmp, "does-not-exist", "nested"))
	if dir := detectTempDir(); dir == "" {
		t.Fatal("TMPDIR 无效时未兜底到其它可写目录")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
