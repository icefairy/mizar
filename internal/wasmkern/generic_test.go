package wasmkern

import (
	"bytes"
	"strings"
	"testing"
)

// TestInvokeBytesUpper 验证通用 ABI 的完整链路：字节进 → 内核处理 → 字节出。
// upper.zig 是这套契约的参考实现，内核契约一旦被破坏这里最先炸。
func TestInvokeBytesUpper(t *testing.T) {
	t.Setenv(EnvDisable, "") // 确保没被禁用
	if !Available("upper") {
		t.Fatal("upper 内核未内嵌（先跑 make zig-kernels）")
	}
	cases := []string{
		"",
		"hello",
		"Hello, World! 123",
		"中文不受影响 abc",
	}
	for _, in := range cases {
		got, err := InvokeBytes("upper", "upper", []byte(in))
		if err != nil {
			t.Fatalf("InvokeBytes(%q): %v", in, err)
		}
		want := strings.ToUpper(in)
		if string(got) != want {
			t.Fatalf("InvokeBytes(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInvokeBytesLarge 验证超过内核静态缓冲区的大输入行为（截断而非崩）。
func TestInvokeBytesLarge(t *testing.T) {
	if !Available("upper") {
		t.Skip("upper 内核未内嵌")
	}
	in := bytes.Repeat([]byte("a"), 100*1024) // 100KB > OUT_CAP(64KB)
	got, err := InvokeBytes("upper", "upper", in)
	if err != nil {
		t.Fatalf("大输入调用失败: %v", err)
	}
	if len(got) != 64*1024 {
		t.Fatalf("应截断到 64KB，实际 %d 字节", len(got))
	}
	for i, b := range got {
		if b != 'A' {
			t.Fatalf("第 %d 字节未转换: %q", i, b)
		}
	}
}

// TestInvokeBytesMissingFn 未导出函数应报错而不是 panic。
func TestInvokeBytesMissingFn(t *testing.T) {
	if !Available("upper") {
		t.Skip("upper 内核未内嵌")
	}
	_, err := InvokeBytes("upper", "no_such_fn", []byte("x"))
	if err == nil {
		t.Fatal("调用不存在的函数应报错")
	}
	if !strings.Contains(err.Error(), "未导出函数") {
		t.Fatalf("错误信息不友好: %v", err)
	}
}

// TestInvokeBytesMissingKernel 不存在的内核应报错。
func TestInvokeBytesMissingKernel(t *testing.T) {
	_, err := InvokeBytes("definitely_not_a_kernel", "fn", nil)
	if err == nil {
		t.Fatal("不存在的内核应报错")
	}
}

// TestListKernels 内嵌内核清单里应能看到随仓库提交的几个。
func TestListKernels(t *testing.T) {
	names := ListKernels()
	want := map[string]bool{"count_tokens": false, "bigram": false, "upper": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, ok := range want {
		if !ok {
			t.Fatalf("内核清单缺失 %q，实际: %v", n, names)
		}
	}
}
