// 技能检索用的分词（2026-11-15）。
//
// 为什么不能只按空格切：中文没有词边界，「帮我把开阳的构建脚本修一下」按空格切
// 只有一个 token，任何描述词都匹配不上 → 该命中的项目技能得 0 分（实测过）。
//
// 方案：CJK 走 unigram + bigram（复用 memory 插件的分词思路，已在生产验证），
// ASCII 走整词。不引新依赖（gse 虽在 go.mod，但它在 engine 包里初始化较重，
// 检索这种高频小调用不值得为它拉起词典）。
//
// 取舍：bigram 会带来一些噪声匹配（例如「构建」会命中「构」+「建」+「构建」），
// 但技能库是小集合（几十到几百条），噪声可用阈值+topN 挡掉；漏召回则无法补救。
// 所以刻意**偏向召回**：宁可多给候选，再靠打分排序 + 上限截断控制精度。
package skills

import "strings"

// tokenize 中英混合分词，返回去重后的 token 集合（全部小写）。
//
//	CJK：单字 + 相邻两字（bigram）
//	ASCII：长度 ≥2 的整词
func tokenize(text string) map[string]bool {
	out := map[string]bool{}
	lower := strings.ToLower(text)
	var ascii strings.Builder
	var cjk []rune

	flushASCII := func() {
		if ascii.Len() >= 2 {
			out[ascii.String()] = true
		} else if ascii.Len() == 1 {
			// 单字母（如 "c"、"go" 里的 g）不入索引，噪声太大
			out[ascii.String()] = true
		}
		ascii.Reset()
	}
	flushCJK := func() {
		for i := 0; i < len(cjk); i++ {
			out[string(cjk[i])] = true // unigram：保证单字也能召回
			if i+1 < len(cjk) {
				out[string(cjk[i:i+2])] = true // bigram：主要信号
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range lower {
		switch {
		case isCJK(r):
			flushASCII()
			cjk = append(cjk, r)
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			flushCJK()
			ascii.WriteRune(r)
		default: // 标点/空白/其它
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

// isCJK 判断是否中日韩统一表意文字（够用即可，不含扩展区 B+）。
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// overlapCount 返回两个 token 集合的交集大小。
func overlapCount(a, b map[string]bool) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}
