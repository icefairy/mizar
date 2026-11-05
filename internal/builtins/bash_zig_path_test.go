package builtins

import (
	"os"
	"strings"
	"testing"

	"mizar/internal/zigboot"
)

// TestBashSeesEmbeddedZig 验证内嵌 Zig 工具链已释放时，
// bash 工具的子进程能直接在 PATH 里找到 zig（无需手写绝对路径）。
// 未释放时跳过——不把「必须跑过 /zig init」作为测试前提。
func TestBashSeesEmbeddedZig(t *testing.T) {
	if !zigboot.Available() {
		t.Skip("zig 未释放到缓存（先执行 /zig init）")
	}
	out, err := toolBash().Run(`{"command":"command -v zig && zig version"}`)
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(out, zigboot.CacheDir()) {
		t.Fatalf("bash 未看到内嵌 zig，实际输出: %s", out)
	}
	if !strings.Contains(out, zigboot.Version) {
		t.Fatalf("zig 版本不符，实际输出: %s", out)
	}
}

// TestBashZigCompiles 端到端：在 bash 工具里用内嵌 zig 直接编译一个原生二进制并运行。
// 这是「插件/Agent 用 Zig 写扩展」的最小可行性验证。
func TestBashZigCompiles(t *testing.T) {
	if !zigboot.Available() {
		t.Skip("zig 未释放到缓存（先执行 /zig init）")
	}
	dir := t.TempDir()
	src := dir + "/hello.zig"
	// 用 posix write 直出，避开 std 版本相关的 API 变动
	srcCode := `const std = @import("std");
pub fn main() u8 {
    _ = std.os.linux.write(1, "zig-ok\n", 7);
    return 0;
}
`
	if err := os.WriteFile(src, []byte(srcCode), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolBash().Run(`{"command":"cd ` + dir + ` && zig build-exe hello.zig -OReleaseFast -femit-bin=hello && ./hello", "timeout": 120}`)
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "zig-ok") {
		t.Fatalf("编译产物未正常运行，输出: %s", out)
	}
}
