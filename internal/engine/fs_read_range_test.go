package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FSReadRangeFn 真实引擎测试：插件 TS 代码用 fs_read_range 分片读大文件。
// 大文件（>10MB，fs_read 会拒绝）用 fs_read_range 探测 size + 尾部 seek 读。
func TestFSReadRangeSeek(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	var sb strings.Builder
	for i := 0; i < 12*1024*1024/64; i++ {
		sb.WriteString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n")
	}
	if err := os.WriteFile(big, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	total := int64(len(sb.String()))
	if total <= 10*1024*1024 {
		t.Fatalf("test file too small: %d", total)
	}

	eng, err := New(&HostFuncs{FSReadRange: FSReadRangeFn})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	js, err := CompileTS("big.ts", `
export function tool_readbig(args: string): string {
  const [c0, size] = fs_read_range(args, 0, 1);
  const [tail] = fs_read_range(args, size - 64, 64);
  const [mid] = fs_read_range(args, 100, 16);
  return size + "|" + c0 + "|" + tail + "|" + mid;
}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := eng.RunScript("big.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := eng.Call("tool_readbig", big)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	out := fmt.Sprint(res)
	b, _ := os.ReadFile(big)
	wantTail := string(b[len(b)-64:])
	wantMid := string(b[100:116])
	if !strings.Contains(out, fmt.Sprintf("%d", total)) {
		t.Fatalf("size wrong: %s", out)
	}
	if !strings.Contains(out, wantTail) {
		t.Fatalf("tail seek wrong: %s", out)
	}
	if !strings.Contains(out, wantMid) {
		t.Fatalf("mid seek wrong: %s", out)
	}
}

// FSReadRangeFn 边界：负 offset / 越界 / 超大 length 都安全
func TestFSReadRangeEdge(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	os.WriteFile(f, []byte("hello world"), 0o644)

	eng, err := New(&HostFuncs{FSReadRange: FSReadRangeFn})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	js, err := CompileTS("edge.ts", `
export function tool_edge(args: string): string {
  const [c1, s1] = fs_read_range(args, -5, 100);
  const [c2, s2] = fs_read_range(args, 99, 100);
  const [c3, s3] = fs_read_range(args, 6, 5);
  return JSON.stringify([c1, s1, c2, s2, c3, s3]);
}`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := eng.RunScript("edge.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := eng.Call("tool_edge", f)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// c1="hello world"（负 offset 归 0，length 超 size 截断）, s1=11
	// c2=""（offset>size 归到 size）, s2=11
	// c3="world"（offset6 len5）, s3=11
	if !strings.Contains(fmt.Sprint(res), `"hello world",11,"",11,"world",11`) {
		t.Fatalf("edge case wrong: %s", res)
	}
}
