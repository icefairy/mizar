package engine

import (
	"log"
	"os"
	"strings"

	"mizar/internal/wasmkern"
)

// 本文件实现记忆检索的批量打分宿主函数。
//
// 为什么需要它：memory.ts 的 tool_mem_search 原先在 goja 里对最多 2000 条记忆
// 逐条执行 similarity()，每次都要在解释器里建 memSet 对象、累加权重。
// 实测（i7-10700，2000 条真实 token，Query="中文分词记忆检索的性能测试"）：
//
//	goja 逐条打分   173 ms   786k allocs
//	宿主函数批量     33 ms    32k allocs   ← 5.2×，且只有一次跨边界
//
// 注意：真正的收益来源是「把循环搬进 Go」，而不是 wasm。wasm 内核每次调用有
// ~5µs 固定开销，逐条调用反而是负收益（见 bench_test.go 的 BigramWasm2000
// vs BigramGo2000）。所以这里只有「分词」这一步可能走内核，打分纯 Go。

// memStopwords 与 memory.ts 的 STOPWORDS、bigram.zig 的 STOP 完全一致。
// 三处必须同步：内核分词路径与 Go 回退路径若不同步，切换开关就会改变检索结果。
var memStopwords = map[string]bool{
	"的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
	"我": true, "你": true, "他": true, "它": true, "这": true, "那": true,
	"有": true, "没": true, "不": true, "就": true, "也": true, "都": true,
	"一个": true, "什么": true, "怎么": true, "如何": true, "可以": true,
	"a": true, "an": true, "the": true, "is": true, "are": true, "of": true,
	"to": true, "and": true, "or": true, "in": true, "on": true, "for": true,
}

func memIsCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// memTokenizeGo 是 memory.ts tokenize() 的纯 Go 参考实现（内核不可用时的回退）。
// 语义：先整体小写；CJK 输出 unigram，紧邻 CJK 再输出 bigram；
// 连续 ASCII 字母/数字长度>=2 输出整词，长度 1 且是 [a-z0-9] 也输出；
// 最后去停用词、去重（保留首次出现顺序）。
func memTokenizeGo(text string) []string {
	s := strings.ToLower(text)
	var toks []string
	var buf []rune
	flush := func() {
		if len(buf) >= 2 {
			toks = append(toks, string(buf))
		} else if len(buf) == 1 {
			c := buf[0]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				toks = append(toks, string(c))
			}
		}
		buf = buf[:0]
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if memIsCJK(c) {
			flush()
			toks = append(toks, string(c))
			if i+1 < len(rs) && memIsCJK(rs[i+1]) {
				toks = append(toks, string(c)+string(rs[i+1]))
			}
		} else if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			buf = append(buf, c)
		} else {
			flush()
		}
	}
	flush()
	seen := make(map[string]bool, len(toks))
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if memStopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// memTokenize 是分词入口，默认走纯 Go（memTokenizeGo）。
//
// 为什么默认不走 wasm 内核——实测（i7-10700，bench_test.go / memscore_bench_test.go）：
//
//	输入 1 段  纯 Go 9.5µs   内核 37.6µs
//	输入 3 段  纯 Go 28µs    内核 69µs
//	输入 10 段 纯 Go 79µs    内核 127µs
//
// 任何尺寸下内核都更慢：要把整段输入写进线性内存、再读回 (offset,len) 对，
// 这两次拷贝的开销远超纯 Go `for range string` 的切分。所以内核只在
// MIZAR_WASM_BIGRAM=force 时用于对照验证，不参与默认路径。
//
// 设置 MIZAR_WASM_VERIFY=1 时，即便走内核也会与 Go 参考逐 token 比对，
// 不一致就回退并记一次日志——防止内核语义漂移静默污染检索质量。
func memTokenize(text string) []string {
	if !memUseKernel() {
		return memTokenizeGo(text)
	}
	got, ok := wasmkern.BigramTokenize(text)
	if !ok {
		return memTokenizeGo(text)
	}
	if os.Getenv(envVerify) == "1" {
		if want := memTokenizeGo(text); !equalTokens(got, want) {
			log.Printf("mems core: bigram 内核与 Go 参考不一致，已回退。text=%q kernel=%v go=%v", text, got, want)
			return want
		}
	}
	return got
}

// memUseKernel 报告是否启用 wasm 内核做分词。
// 直接复用 wasmkern 的开关语义（默认关，MIZAR_WASM_BIGRAM=force/1/on 开），
// 这样宿主与插件层只有一个真相来源，不会出现「宿主以为开了、内核自己关了」。
func memUseKernel() bool {
	return wasmkern.BigramAvailable()
}

// envVerify 开启内核结果的交叉校验（排障用）。
const envVerify = "MIZAR_WASM_VERIFY"

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// memSimilarity 是 memory.ts similarity() 的等价实现。
//
//	score = 覆盖率 * 0.75 + 精确度 * 0.25
//	覆盖率 = 命中 token 长度和 / 查询 token 长度和
//	精确度 = 命中数 / 记忆 token 数
//
// 权重用 token 长度：长 token 信息量更大（"性能测试" 比 "的" 有区分度）。
func memSimilarity(memTokens, queryTokens []string, queryWeight int) float64 {
	if len(memTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	// memSet 用一个 map 建一次，避免逐条在 Go 侧走 O(n²)。
	memSet := make(map[string]struct{}, len(memTokens))
	for _, t := range memTokens {
		memSet[t] = struct{}{}
	}
	hit, hitWeight := 0, 0
	for _, qt := range queryTokens {
		w := len([]rune(qt))
		if w < 1 {
			w = 1
		}
		if _, ok := memSet[qt]; ok {
			hit++
			hitWeight += w
		}
	}
	if hit == 0 {
		return 0
	}
	coverage := float64(hitWeight) / float64(queryWeight)
	precision := float64(hit) / float64(len(memTokens))
	return coverage*0.75 + precision*0.25
}

// memQueryWeight 预先算好查询 token 的权重和（长度累计），供所有记忆复用。
func memQueryWeight(queryTokens []string) int {
	total := 0
	for _, qt := range queryTokens {
		w := len([]rune(qt))
		if w < 1 {
			w = 1
		}
		total += w
	}
	return total
}

// memScores 对一批记忆的 token 串打分。docs[i] 是空格分隔的 tokens（DB 列原样）。
// 空串视为无 token → 0 分。返回与 docs 等长的分数切片。
func memScores(query string, docs []string) []float64 {
	out := make([]float64, len(docs))
	if query == "" || len(docs) == 0 {
		return out
	}
	queryTokens := memTokenize(query)
	if len(queryTokens) == 0 {
		return out
	}
	queryWeight := memQueryWeight(queryTokens)
	// 复用同一个 memSet 容器，减少 len(docs) 次 map 分配。
	buf := make(map[string]struct{}, 64)
	for i, doc := range docs {
		// DB 里的 tokens 列是 tokenize() 后 join(" ") 的结果，直接按空白切即可；
		// 用 strings.Fields 而不是 Split 以兼容历史数据里的连续空格。
		toks := strings.Fields(doc)
		if len(toks) == 0 {
			continue
		}
		for k := range buf {
			delete(buf, k)
		}
		for _, t := range toks {
			buf[t] = struct{}{}
		}
		hit, hitWeight := 0, 0
		for _, qt := range queryTokens {
			w := len([]rune(qt))
			if w < 1 {
				w = 1
			}
			if _, ok := buf[qt]; ok {
				hit++
				hitWeight += w
			}
		}
		if hit == 0 {
			continue
		}
		coverage := float64(hitWeight) / float64(queryWeight)
		precision := float64(hit) / float64(len(toks))
		out[i] = coverage*0.75 + precision*0.25
	}
	return out
}

// registerMem 注册记忆检索相关宿主函数（纯计算，无 I/O）：
//   - mem_tokenize(text): 分词并用空格拼接，供写入时的 tokens 列使用
//   - mem_score(query, docs): 批量打分，docs 为各条记忆的 tokens 列
//
// 两者都是确定性纯函数，引擎创建时无条件注册。
func registerMem(reg func(name string, fn any)) {
	reg("mem_tokenize", func(text string) string {
		return strings.Join(memTokenize(text), " ")
	})
	reg("mem_score", func(query string, docs []string) []float64 {
		return memScores(query, docs)
	})
}
