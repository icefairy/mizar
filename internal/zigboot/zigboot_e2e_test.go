package zigboot

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestReleaseEndToEnd 走完整链路：释放 → zig version → zig cc 编一个 C 程序 → 运行它
// → 再 --force 重放一次（覆盖 tmp+rename 原子替换路径）。
//
// 未内嵌归档时跳过（离线/精简构建下保持 go test 全绿）。
// 释放 + 编译约 30 秒，用 -short 可跳过。
func TestReleaseEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过端到端释放")
	}
	if _, _, ok := embeddedArchive(); !ok {
		t.Skip("此构建未内嵌 Zig 归档，跳过端到端测试")
	}
	// 幂等：已释放时直接复用
	zig, err := Release(false)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	out, err := exec.Command(zig, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("zig version 失败: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != Version {
		t.Fatalf("zig version = %q, want %q", got, Version)
	}

	// 真实编译 + 运行：验证释放出的工具链可用（含 lib/std 与 C 头文件）
	dir := t.TempDir()
	src := dir + "/hello.c"
	if err := os.WriteFile(src, []byte(
		"#include <stdio.h>\nint main(void){printf(\"ok-embedded\\n\");return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := dir + "/hello"
	if o, e := exec.Command(zig, "cc", "-O2", src, "-o", bin).CombinedOutput(); e != nil {
		t.Fatalf("zig cc 失败: %v\n%s", e, o)
	}
	progOut, perr := exec.Command(bin).CombinedOutput()
	if perr != nil {
		t.Fatalf("运行产物失败: %v\n%s", perr, progOut)
	}
	if !strings.Contains(string(progOut), "ok-embedded") {
		t.Fatalf("产物输出异常: %q", progOut)
	}

	// 强制重放：走 tmp 目录 + rename 的原子替换路径
	if _, err := Release(true); err != nil {
		t.Fatalf("Release(force): %v", err)
	}
	if st, err := os.Stat(zig); err != nil || st.IsDir() {
		t.Fatalf("force 重放后 zig 可执行文件缺失: %v", err)
	}
}
