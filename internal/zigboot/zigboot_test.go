package zigboot

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTargetSupported(t *testing.T) {
	plat, ext, err := target()
	if err != nil {
		t.Skipf("当前平台无官方归档: %v", err)
	}
	if plat == "" {
		t.Fatal("平台标识为空")
	}
	if ext != ".tar.xz" && ext != ".zip" {
		t.Fatalf("意外的归档扩展名: %s", ext)
	}
	name, err := ArchiveName()
	if err != nil {
		t.Fatalf("ArchiveName: %v", err)
	}
	want := "zig-" + plat + "-" + Version + ext
	if name != want {
		t.Fatalf("ArchiveName = %s, want %s", name, want)
	}
}

func TestStripTopDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zig-x86_64-linux-0.17.1/zig", "zig"},
		{"zig-x86_64-linux-0.17.1/lib/std/std.zig", "lib/std/std.zig"},
		{"./zig-x86_64-linux-0.17.1/zig", "zig"},
		{"single", ""},      // 无目录段 → 丢弃
		{"trailing/", ""},   // 只剥顶层
		{"a\\b\\c", "b\\c"}, // Windows 归档也可能用反斜杠
	}
	for _, c := range cases {
		if got := stripTopDir(c.in); got != c.want {
			t.Errorf("stripTopDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStatusNoPanic 确保未内嵌归档时 Status 也能给出有用信息而不 panic。
func TestStatusNoPanic(t *testing.T) {
	s := Status()
	if s == "" {
		t.Fatal("Status 返回空字符串")
	}
}

// TestReleaseWithoutEmbedded 未内嵌归档时必须返回清晰错误（而非 panic 或残留文件）。
func TestReleaseWithoutEmbedded(t *testing.T) {
	if _, _, ok := embeddedArchive(); ok {
		t.Skip("此构建内嵌了归档，跳过「无归档」分支")
	}
	// 隔离缓存目录：否则本机已存在释放好的 zig 时，Release(false) 幂等短路返回成功，
	// 掩盖「无归档应报错」的分支。
	t.Setenv("MIZAR_ZIG_CACHE", t.TempDir())
	_, err := Release(false)
	if err == nil {
		t.Fatal("期望 Release 报错（未内嵌归档）")
	}
}

// TestZigBinaryPath 校验路径落在缓存目录下、文件名与平台一致。
func TestZigBinaryPath(t *testing.T) {
	p := ZigBinary()
	if !filepath.IsAbs(p) && !filepath.IsLocal(p) {
		t.Fatalf("路径既非绝对也非本地: %s", p)
	}
	base := filepath.Base(p)
	want := "zig"
	if runtime.GOOS == "windows" {
		want = "zig.exe"
	}
	if base != want {
		t.Fatalf("可执行文件名 = %s, want %s", base, want)
	}
	if dir := filepath.Dir(p); dir != filepath.Join(CacheDir(), mustPlat()) {
		if _, _, err := target(); err == nil {
			t.Fatalf("目录 = %s, 期望在 %s 下", dir, CacheDir())
		}
	}
}

func mustPlat() string {
	p, _, err := target()
	if err != nil {
		return ""
	}
	return p
}

// TestClean 清空缓存目录后确实消失。
func TestClean(t *testing.T) {
	// 重定向到临时目录，避免动到真实的 ~/.cache/mizar/zig
	// （go test 会并行跑不同包，互踩会报 "directory not empty"）。
	t.Setenv("MIZAR_ZIG_CACHE", t.TempDir())
	dir := CacheDir()
	_ = os.MkdirAll(filepath.Join(dir, "probe"), 0o755)
	if err := Clean(); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Clean 后目录仍存在: %s", dir)
	}
}
