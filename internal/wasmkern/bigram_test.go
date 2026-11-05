package wasmkern

import (
	"strings"
	"testing"
	"unicode"
)

// refTokenize 是 memory.ts tokenize() 的 Go 参考实现，用于验证内核语义一致。
func refTokenize(text string) []string {
	stop := map[string]bool{
		"的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
		"我": true, "你": true, "他": true, "它": true, "这": true, "那": true,
		"有": true, "没": true, "不": true, "就": true, "也": true, "都": true,
		"一个": true, "什么": true, "怎么": true, "如何": true, "可以": true,
		"a": true, "an": true, "the": true, "is": true, "are": true, "of": true,
		"to": true, "and": true, "or": true, "in": true, "on": true, "for": true,
	}
	isCJK := func(c rune) bool {
		return (c >= 0x4e00 && c <= 0x9fff) || (c >= 0x3400 && c <= 0x4dbf)
	}
	s := strings.ToLower(text)
	rs := []rune(s)
	var tokens []string
	var buf []rune
	flush := func() {
		if len(buf) >= 2 {
			tokens = append(tokens, string(buf))
		} else if len(buf) == 1 {
			c := buf[0]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				tokens = append(tokens, string(c))
			}
		}
		buf = buf[:0]
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if isCJK(c) {
			flush()
			tokens = append(tokens, string(c))
			if i+1 < len(rs) && isCJK(rs[i+1]) {
				tokens = append(tokens, string(c)+string(rs[i+1]))
			}
		} else if unicode.IsLetter(c) && c < 128 || c >= '0' && c <= '9' {
			buf = append(buf, c)
		} else {
			flush()
		}
	}
	flush()
	seen := map[string]bool{}
	var out []string
	for _, t := range tokens {
		if stop[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func TestBigramMatchesReference(t *testing.T) {
	t.Setenv(EnvBigram, "force")
	cases := []string{
		"我爱北京天安门",
		"中文分词和记忆检索的性能测试",
		"这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world ",
		"Hello World 123",
		"a an the is are",
		"Go语言与Zig",
		"",
		"!!!###",
		"中",
		"中文",
		"a",
		"ab",
		"abc def",
		"混合ABC中文DEF",
	}
	for _, c := range cases {
		want := refTokenize(c)
		got, ok := BigramTokenize(c)
		if len(c) == 0 {
			if ok {
				t.Errorf("空输入不应返回 ok")
			}
			continue
		}
		if !ok {
			t.Fatalf("内核不可用（输入 %q）", c)
		}
		if len(got) != len(want) {
			t.Errorf("%q: token 数 got %d want %d\ngot=%v\nwant=%v", c, len(got), len(want), got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: [%d] got %q want %q", c, i, got[i], want[i])
			}
		}
	}
}

func TestBigramDisable(t *testing.T) {
	t.Setenv(EnvBigram, "0")
	if _, ok := BigramTokenize("我爱北京"); ok {
		t.Fatal("EnvBigram=0 时应禁用")
	}
}

// 默认（未设环境变量）不启用：实测内核在任何尺寸都比纯 Go 慢（见
// wasmkern.go 的 EnvBigram 注释与 engine/memscore_bench_test.go），
// 所以默认让 Go 接管，只有显式 force/1/on 才走内核。
func TestBigramDefaultOff(t *testing.T) {
	t.Setenv(EnvBigram, "")
	if _, ok := BigramTokenize("我爱北京"); ok {
		t.Fatal("默认应不启用 bigram 内核")
	}
}

// 开关的各种写法都要能被正确识别。
func TestBigramSwitchParsing(t *testing.T) {
	on := []string{"force", "1", "on", "true", "yes", " ON "}
	for _, v := range on {
		t.Setenv(EnvBigram, v)
		if _, ok := BigramTokenize("我爱北京"); !ok {
			t.Errorf("%q 应启用内核", v)
		}
	}
	off := []string{"", "0", "off", "no", "false", "auto"}
	for _, v := range off {
		t.Setenv(EnvBigram, v)
		if _, ok := BigramTokenize("我爱北京"); ok {
			t.Errorf("%q 应禁用内核", v)
		}
	}
}

func TestBigramConcurrent(t *testing.T) {
	t.Setenv(EnvBigram, "force")
	text := "这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world "
	done := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- true }()
			for j := 0; j < 50; j++ {
				got, ok := BigramTokenize(text)
				if !ok || len(got) == 0 {
					t.Errorf("并发调用失败")
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
