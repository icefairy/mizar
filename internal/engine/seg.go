package engine

import (
	"encoding/json"
	"sync"

	"github.com/go-ego/gse"
)

// 全局分词器单例（gse 词典加载较耗时，只初始化一次）。
var (
	segOnce      sync.Once
	segMu        sync.Mutex
	segSingleton *gse.Segmenter
	segInitErr   error
)

// segInit 懒初始化全局分词器（thread-safe）。
// NewEmbed("zh", "en")：含 "en" 时开启 AlphaNum，能按单词切英文。
func segInit() (*gse.Segmenter, error) {
	segOnce.Do(func() {
		s, err := gse.NewEmbed("zh", "en")
		if err != nil {
			segInitErr = err
			return
		}
		segSingleton = &s
	})
	return segSingleton, segInitErr
}

// SegWord 单个分词结果（带词性标注）。
type SegWord struct {
	Text string  `json:"text"` // 词
	Pos  string  `json:"pos"`  // 词性标注（n 名词 / v 动词 / a 形容词 等，英文词可能为空）
	Freq float64 `json:"freq"` // 词典频率
}

// segText 对中英文混排文本分词（精确模式 + HMM），过滤标点/空白/空串。
// 返回纯词列表（不含词性）。供 seg_cut 宿主函数使用。
func segText(text string) []string {
	seg, err := segInit()
	if err != nil {
		return nil
	}
	segMu.Lock()
	words := seg.TrimPunct(seg.Cut(text, true))
	segMu.Unlock()
	return words
}

// segTokens 返回带词性的分词结果（供按名词/动词/形容词检索记忆用）。供 seg_pos 使用。
func segTokens(text string) []SegWord {
	seg, err := segInit()
	if err != nil {
		return nil
	}
	segMu.Lock()
	segs := seg.Pos(text, true)
	segs = seg.TrimPosPunct(segs)
	segMu.Unlock()
	out := make([]SegWord, 0, len(segs))
	for _, p := range segs {
		if p.Text == "" {
			continue
		}
		freq, _, _ := seg.Find(p.Text)
		out = append(out, SegWord{Text: p.Text, Pos: p.Pos, Freq: freq})
	}
	return out
}

// registerSeg 注册中文+英文分词宿主函数（纯函数，供 TS/JS 插件调用）：
//   - seg_cut(text): 返回纯词列表（JS string[]）
//   - seg_pos(text): 返回带词性的分词结果（JSON 字符串: [{text,pos,freq},...]）
func registerSeg(reg func(name string, fn any)) {
	reg("seg_cut", func(text string) []string { return segText(text) })
	reg("seg_pos", func(text string) (string, error) {
		tokens := segTokens(text)
		b, err := json.Marshal(tokens)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}