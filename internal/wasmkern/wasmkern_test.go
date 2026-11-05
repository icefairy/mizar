package wasmkern

import (
	"os"
	"strings"
	"testing"
)

// refCountTokens 是与 internal/agent 压缩器、engine 插件宿主函数同口径的纯 Go 参考实现。
// wasm 内核必须与它逐字节一致，否则插件估算和 Agent 压缩触发会打架。
func refCountTokens(s string) int {
	if s == "" {
		return 0
	}
	chars, cjk := 0, 0
	for _, r := range s {
		chars++
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		}
	}
	return cjk + (chars-cjk+3)/4
}

func TestCountTokensMatchesGo(t *testing.T) {
	t.Setenv(EnvMode, "force") // 本测试验证内核正确性，需强制启用
	cases := []string{
		"",
		"a",
		"abcd",
		"abcde",
		"你好世界啊",
		"你好hello",
		"abcdefghijklmnopqrst",
		"这是一段中英混合 mixed 文本，包含标点！还有 emoji 😀 和换行\n第二行。",
		strings.Repeat("中", 1000),
		strings.Repeat("word ", 5000),
		// 边界：CJK 区间的两端
		"\u4e00", "\u9fff", "\u4dff", "\ua000",
	}
	for _, s := range cases {
		want := refCountTokens(s)
		got, ok := kernelCountTokens(s)
		if !ok {
			t.Fatalf("wasm 内核不可用（输入 %q），本测试要求内核可加载", trunc(s))
		}
		if got != want {
			t.Fatalf("count_tokens(%.40q) = %d, want %d", s, got, want)
		}
	}
}

// kernelCountTokens 绕过 CountTokens 的「小输入走 Go」门槛，直接压内核，
// 让一致性用例能覆盖短串与边界码点。
func kernelCountTokens(s string) (int, bool) {
	out, err := Default().Invoke(KernelCountTokens, FnCountTokens, []byte(s))
	if err != nil {
		return 0, false
	}
	return int(out), true
}

func trunc(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func TestCountTokensLarge(t *testing.T) {
	t.Setenv(EnvMode, "force")
	// 4MB 级输入：验证线性内存按需扩容与一次性写入不越界。
	s := strings.Repeat("这是一段中文文本 with some english 1234567890.\n", 80000)
	want := refCountTokens(s)
	got, ok := kernelCountTokens(s)
	if !ok {
		t.Fatal("内核不可用")
	}
	if got != want {
		t.Fatalf("大输入 count_tokens = %d, want %d", got, want)
	}
}

func TestDisabledByEnv(t *testing.T) {
	t.Setenv(EnvDisable, "0")
	if _, ok := CountTokens("你好"); ok {
		t.Fatal("设置 " + EnvDisable + "=0 后应回退，但内核仍返回结果")
	}
	if got := CountTokensOr("你好", refCountTokens); got != 2 {
		t.Fatalf("回退值 = %d, want 2", got)
	}
}

// TestDefaultModeUsesGo 确认默认配置下 count_tokens 走纯 Go 快路径
// （实测 wasm 在该算法上更慢，见 wasmkern.go 的 minKernelBytes 注释）。
func TestDefaultModeUsesGo(t *testing.T) {
	t.Setenv(EnvMode, "")
	if _, ok := CountTokens(strings.Repeat("中", 100000)); ok {
		t.Fatal("默认配置不应让 count_tokens 走 wasm 内核")
	}
	if got := CountTokensOr(strings.Repeat("中", 100000), refCountTokens); got != 100000 {
		t.Fatalf("回退结果 = %d, want 100000", got)
	}
}

func TestFallbackOnMissingKernel(t *testing.T) {
	if Default().Available("no_such_kernel") {
		t.Fatal("不存在的内核不应报告可用")
	}
	if got := CountTokensOr("abcdefghi", refCountTokens); got != 3 {
		t.Fatalf("CountTokensOr = %d, want 3", got)
	}
}

func TestConcurrent(t *testing.T) {
	t.Setenv(EnvMode, "force")
	// 实例池：并发调用必须各自安全、结果正确。
	const n = 16
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			got, ok := kernelCountTokens("你好hello")
			if !ok {
				done <- -1
				return
			}
			done <- got
		}()
	}
	for i := 0; i < n; i++ {
		if v := <-done; v != 4 {
			t.Fatalf("并发 count_tokens = %d, want 4", v)
		}
	}
}

func TestMain(m *testing.M) {
	// 确保测试在“内核可用”的默认配置下跑；单独用例自行用 t.Setenv 覆盖。
	os.Unsetenv(EnvDisable)
	os.Exit(m.Run())
}
