package engine

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// 基准对照：证明「批量打分下沉到宿主函数」是真实收益，而不是想当然。
//
// 场景取自 memory.ts 的真实调用：2000 条记忆，每条 tokens 列约 120 字符，
// 查询 "中文分词记忆检索的性能测试"。这正是用户可感的检索卡顿点。

const memBenchMemories = 2000

func memBenchDocs() []string {
	base := strings.Join(memTokenizeGo(strings.Repeat(
		"这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world ", 3)), " ")
	out := make([]string, memBenchMemories)
	for i := range out {
		out[i] = base
	}
	return out
}

// BenchmarkMemScoreHost 是改造后的路径：一次宿主函数调用完成 2000 条打分。
func BenchmarkMemScoreHost(b *testing.B) {
	docs := memBenchDocs()
	query := "中文分词记忆检索的性能测试"
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = memScores(query, docs)
	}
}

// memoryScoreJS 是 memory.ts similarity() 的原样 JS 实现（改造前的热路径）。
const memoryScoreJS = `
function similarity(memTokens, queryTokens) {
  if (memTokens.length === 0 || queryTokens.length === 0) return 0;
  var memSet = {};
  for (var i = 0; i < memTokens.length; i++) memSet[memTokens[i]] = true;
  var hit = 0, hw = 0, tw = 0;
  for (var j = 0; j < queryTokens.length; j++) {
    var qt = queryTokens[j];
    var w = Math.max(1, qt.length);
    tw += w;
    if (memSet[qt]) { hit++; hw += w; }
  }
  if (hit === 0) return 0;
  return (hw / tw) * 0.75 + (hit / memTokens.length) * 0.25;
}
`

// BenchmarkMemScoreGoja 是改造前的路径：在 goja 里逐条跑 similarity()。
func BenchmarkMemScoreGoja(b *testing.B) {
	docs := memBenchDocs()
	query := "中文分词记忆检索的性能测试"
	vm := goja.New()
	if _, err := vm.RunString(memoryScoreJS); err != nil {
		b.Fatal(err)
	}
	sim, ok := goja.AssertFunction(vm.Get("similarity"))
	if !ok {
		b.Fatal("similarity 未定义")
	}
	// 预构造 JS 侧数组（模拟从 SQLite 读出后 split 的结果），排除准备开销
	jsMems := make([]goja.Value, len(docs))
	for i, d := range docs {
		parts := strings.Fields(d)
		arr := make([]any, len(parts))
		for j, p := range parts {
			arr[j] = p
		}
		jsMems[i] = vm.ToValue(arr)
	}
	qt := memTokenizeGo(query)
	qarr := make([]any, len(qt))
	for i, t := range qt {
		qarr[i] = t
	}
	qval := vm.ToValue(qarr)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j := 0; j < len(jsMems); j++ {
			_, _ = sim(goja.Undefined(), jsMems[j], qval)
		}
	}
}

// BenchmarkMemTokenize 对比分词两条路径（纯 Go vs wasm 内核）。
// 结论：任何尺寸下 wasm 都更慢（跨边界 + 线性内存来回拷贝），
// 故 memTokenize 默认走纯 Go，内核仅在 MIZAR_WASM_BIGRAM=force 时用于对照。
func BenchmarkMemTokenizeGo(b *testing.B) {
	text := strings.Repeat("这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world ", 20)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = memTokenizeGo(text)
	}
}

func BenchmarkMemTokenizeAuto(b *testing.B) {
	text := strings.Repeat("这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world ", 20)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = memTokenize(text)
	}
}
