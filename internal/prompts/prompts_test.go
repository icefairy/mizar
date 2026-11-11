package prompts

import (
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	src := "---\ndescription: Review PRs from URLs\nargument-hint: <PR-URL>\n---\nReview the PR at $1.\n"
	desc, hint := parseFrontmatter(src)
	if desc != "Review PRs from URLs" {
		t.Fatalf("desc = %q, want Review PRs from URLs", desc)
	}
	if hint != "<PR-URL>" {
		t.Fatalf("hint = %q, want <PR-URL>", hint)
	}
}

func TestStripFrontmatter(t *testing.T) {
	src := "---\ndescription: test\n---\nHello $1 world"
	body := stripFrontmatter(src)
	want := "Hello $1 world"
	if body != want {
		t.Fatalf("got %q, want %q", body, want)
	}
}

func TestExpandSimple(t *testing.T) {
	r := NewRegistry()
	r.tpls["hello"] = &Template{
		Name: "hello", Content: "Hello, $1!",
	}
	out, err := r.Expand("hello", "World")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello, World!" {
		t.Fatalf("got %q, want Hello, World!", out)
	}
}

func TestExpandDefault(t *testing.T) {
	r := NewRegistry()
	r.tpls["sum"] = &Template{
		Name: "sum", Content: "Summarize in ${1:-5} points.",
	}
	out, err := r.Expand("sum", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Summarize in 5 points." {
		t.Fatalf("got %q, want Summarize in 5 points.", out)
	}
}

func TestExpandAt(t *testing.T) {
	r := NewRegistry()
	r.tpls["multi"] = &Template{
		Name: "multi", Content: "Args: $@ and $1 first.",
	}
	out, err := r.Expand("multi", "a b c")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Args: a b c and a first." {
		t.Fatalf("got %q, want Args: a b c and a first.", out)
	}
}

func TestExpandAtSlice(t *testing.T) {
	r := NewRegistry()
	r.tpls["slice"] = &Template{
		Name: "slice", Content: "${@:2:2}",
	}
	out, err := r.Expand("slice", "a b c d")
	if err != nil {
		t.Fatal(err)
	}
	if out != "b c" {
		t.Fatalf("got %q, want b c", out)
	}
}

func TestSplitArgs(t *testing.T) {
	parts := splitArgs(`hello "world foo" bar`)
	if len(parts) != 3 || parts[0] != "hello" || parts[1] != "world foo" || parts[2] != "bar" {
		t.Fatalf("got %v", parts)
	}
}

// Placeholders 提取模板用到的占位符（去重、保序），供 /templates 展示。
func TestPlaceholders(t *testing.T) {
	tpl := &Template{Content: "审查 $1 的改动，若无则用 ${2:-HEAD}；全部: ${@} ${@:2} $@ $1"}
	got := tpl.Placeholders()
	want := []string{"$1", "${2:-HEAD}", "${@}", "${@:2}", "$@"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 got %q, want %q（应去重保序）", i, got[i], want[i])
		}
	}
}

// TestHelpListsPlaceholders /templates 输出必须告诉用户「具体有哪些占位符可以填」：
// 每个模板列出参数提示、占位符清单，末尾给出占位符语法与模板目录。
func TestHelpListsPlaceholders(t *testing.T) {
	r := NewRegistry()
	r.AddDir("/tmp/mizar-prompts-test")
	r.tpls["review"] = &Template{
		Name:        "review",
		Description: "审查 PR",
		ArgHint:     "<PR-URL>",
		Content:     "审查 $1，重点 ${2:-安全性}",
	}
	r.tpls["plain"] = &Template{Name: "plain", Description: "纯文本", Content: "无参数片段"}
	out := r.Help()

	for _, want := range []string{
		"/review", "审查 PR", "<PR-URL>", "$1", "${2:-安全性}",
		"/plain", "纯文本", "（无，纯文本片段）",
		"${1:-默认}", "${@:2:3}", "/tmp/mizar-prompts-test",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Help 缺少 %q：\n%s", want, out)
		}
	}
}

// 模板名按字母序输出，保证 /templates 多次调用结果稳定。
func TestListSorted(t *testing.T) {
	r := NewRegistry()
	r.tpls["zeta"] = &Template{Name: "zeta"}
	r.tpls["alpha"] = &Template{Name: "alpha"}
	got := r.List()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Fatalf("应按名排序: %v", got)
	}
}

// 空注册表时也要说明占位符语法与放置目录，而不是一句「暂无模板」。
func TestHelpEmptyMentionsSyntax(t *testing.T) {
	r := NewRegistry()
	r.AddDir("~/.mizar/prompts")
	out := r.Help()
	if !strings.Contains(out, "~/.mizar/prompts") || !strings.Contains(out, "${@}") {
		t.Fatalf("空表 Help 应说明目录与语法：\n%s", out)
	}
}
