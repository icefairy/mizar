package wasmkern

import (
	"strings"
	"testing"
)

func bigramBenchTexts() ([]string, string) {
	memTexts := make([]string, 2000)
	for i := range memTexts {
		memTexts[i] = strings.Repeat("这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world ", 3)
	}
	return memTexts, "中文分词记忆检索的性能测试"
}

func BenchmarkBigramWasm2000(b *testing.B) {
	b.Setenv(EnvBigram, "force") // 基准测的就是内核路径本身
	memTexts, query := bigramBenchTexts()
	if _, ok := BigramTokenize(query); !ok {
		b.Skip("内核不可用")
	}
	b.ResetTimer()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		_, _ = BigramTokenize(query)
		for _, mt := range memTexts {
			_, _ = BigramTokenize(mt)
		}
	}
}

func BenchmarkBigramGo2000(b *testing.B) {
	b.Setenv(EnvBigram, "force")
	memTexts, query := bigramBenchTexts()
	b.ResetTimer()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		_ = refTokenize(query)
		for _, mt := range memTexts {
			_ = refTokenize(mt)
		}
	}
}
