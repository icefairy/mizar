package engine

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"mizar/internal/wasmkern"
)

// 这些用例的期望值直接照抄 memory.ts 的 tokenize()/similarity() 语义，
// 是「插件行为不能因下沉到 Go 而改变」的回归护栏。

func TestMemTokenizeGo(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"我爱北京天安门", []string{"我爱", "爱", "爱北", "北", "北京", "京", "京天", "天", "天安", "安", "安门", "门"}},
		{"Hello World 123", []string{"hello", "world", "123"}},
		{"a an the is are", nil},
		{"中文", []string{"中", "中文", "文"}},
		{"", nil},
		{"!!!###", nil},
	}
	for _, c := range cases {
		got := memTokenizeGo(c.in)
		if len(got) != len(c.want) {
			t.Errorf("memTokenizeGo(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("memTokenizeGo(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// 内核路径（可用时）必须与 Go 参考逐 token 一致。
func TestMemTokenizeMatchesKernel(t *testing.T) {
	inputs := []string{
		"这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world",
		"我爱北京天安门",
		"Go语言与Zig混合ABC中文DEF",
		"记忆检索的性能测试：count_tokens 与 bigram 内核",
	}
	for _, in := range inputs {
		want := memTokenizeGo(in)
		got := memTokenize(in)
		if len(got) != len(want) {
			t.Errorf("%q: got %d tokens, want %d\ngot=%v\nwant=%v", in, len(got), len(want), got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q[%d]: got %q want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestMemScores(t *testing.T) {
	query := "中文分词"
	docs := []string{
		"中 中文 文 文分 分 分词 词", // 完全命中
		"记 记忆 忆 检索",        // 完全不命中
		"",                 // 空
		"中 中文 文 分 分词 词",    // 部分命中
	}
	got := memScores(query, docs)
	if len(got) != len(docs) {
		t.Fatalf("长度不对: %d", len(got))
	}
	if got[0] <= 0 || got[0] > 1 {
		t.Errorf("完全命中应为 (0,1] 之间，得到 %v", got[0])
	}
	if got[1] != 0 || got[2] != 0 {
		t.Errorf("不命中/空应为 0，得到 %v %v", got[1], got[2])
	}
	if got[3] <= 0 || got[3] >= got[0] {
		t.Errorf("部分命中应在 (0, 完全命中) 之间，得到 %v vs %v", got[3], got[0])
	}
}

// 与 memory.ts similarity() 的直接对照：给一条记忆算分，两边必须一致。
func TestMemSimilarityMatchesPlugin(t *testing.T) {
	query := "中文分词记忆检索的性能测试"
	mem := "这是一个关于中文分词和记忆检索的测试内容，包含英文 hello world"
	qt := memTokenizeGo(query)
	mt := strings.Fields(strings.Join(memTokenizeGo(mem), " "))

	got := memScores(query, []string{strings.Join(mt, " ")})[0]
	want := memSimilarity(mt, qt, memQueryWeight(qt))
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("memScores=%v 与 memSimilarity=%v 不一致", got, want)
	}
	if got <= 0 || got > 1 {
		t.Fatalf("分数越界: %v", got)
	}
	t.Logf("score = %.6f", got)
}

func TestMemScoresEmpty(t *testing.T) {
	if got := memScores("", []string{"a b"}); len(got) != 1 || got[0] != 0 {
		t.Fatalf("空 query 应全 0，得到 %v", got)
	}
	if got := memScores("中文", nil); len(got) != 0 {
		t.Fatalf("空 docs 应返回空，得到 %v", got)
	}
}

func TestMemVerifyEnv(t *testing.T) {
	t.Setenv(envVerify, "1")
	t.Setenv(wasmkern.EnvBigram, "force")
	got := memTokenize("我爱北京")
	want := memTokenizeGo("我爱北京")
	if len(got) != len(want) {
		t.Fatalf("verify 模式下结果应一致: %v vs %v", got, want)
	}
}

// 端到端：在 goja 里调用 mem_tokenize / mem_score 宿主函数，
// 验证「插件里 typeof mem_score === "function"」成立且结果与 Go 一致。
func TestMemHostFuncsInGoja(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	js, _ := CompileTS("probe.ts", `
export function tool_probe(): string {
  var joined = mem_tokenize("中文分词记忆检索");
  var sc = mem_score("中文分词", ["中 中文 文 分 分词 词", "记 忆 检 索"]);
  return JSON.stringify({joined: joined, sc: sc});
}`)
	if err := e.RunScript("probe.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	v, err := e.Call("tool_probe")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	out, _ := v.(string)
	if !strings.Contains(out, `"joined":"`) {
		t.Fatalf("mem_tokenize 未生效: %s", out)
	}
	if !strings.Contains(out, `"sc":[`) {
		t.Fatalf("mem_score 未生效: %s", out)
	}
	// sc[0] 应 > 0，sc[1] 应为 0
	var parsed struct {
		Joined string    `json:"joined"`
		Sc     []float64 `json:"sc"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse: %v (%s)", err, out)
	}
	if parsed.Sc[0] <= 0 || parsed.Sc[1] != 0 {
		t.Fatalf("分数异常: %v", parsed.Sc)
	}
	t.Logf("joined=%q sc=%v", parsed.Joined, parsed.Sc)
}
