package wasmkern

import (
	"strings"
	"testing"
)

// BenchmarkCountTokensGo 与 BenchmarkCountTokensWasm 用同一份输入，
// 对比纯 Go 实现与 Zig→wasm 内核的吞吐，作为「值得下沉」的量化依据。

func benchInput() []string {
	return []string{
		"你好hello",
		strings.Repeat("这是一段中英混合文本 with english 1234567890.\n", 2000), // ~100KB
		strings.Repeat("word ", 40000), // ~200KB 纯英文
	}
}

func BenchmarkCountTokensGo(b *testing.B) {
	inputs := benchInput()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range inputs {
			_ = refCountTokens(s)
		}
	}
}

func BenchmarkCountTokensWasm(b *testing.B) {
	inputs := benchInput()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range inputs {
			if _, ok := CountTokens(s); !ok {
				b.Skip("wasm 内核不可用")
			}
		}
	}
}
