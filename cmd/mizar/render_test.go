package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

// TestRenderNewlinesPreserved TUI 渲染器（不用 WithWordWrap）渲染含 \n 的回复时，
// 换行必须保留（曾因 WithWordWrap(80) 把 \n 折叠成空格导致多行回复挤成一行）。
func TestRenderNewlinesPreserved(t *testing.T) {
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle())
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("第一行\n第二行\n第三行")
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	// Strip 后每行应包含原文内容（允许 glamour 加缩进/尾随空格）
	if !strings.Contains(plain, "第一行") || !strings.Contains(plain, "第二行") || !strings.Contains(plain, "第三行") {
		t.Fatalf("content missing, got %q", plain)
	}
	for _, l := range lines {
		t.Logf("LINE=%q", l)
	}
	if len(lines) < 3 {
		t.Fatalf("expected >=3 lines, got %d: %q", len(lines), plain)
	}
}

// TestRenderLiteralBackslashN 字面 backslash-n（模型偶发输出 \\n）不应被转成换行。
func TestRenderLiteralBackslashN(t *testing.T) {
	r, _ := glamour.NewTermRenderer(glamour.WithAutoStyle())
	out, _ := r.Render("第一行\\n第二行")
	plain := ansi.Strip(out)
	if !strings.Contains(plain, `\n`) {
		t.Fatalf("literal \\n lost: %q", plain)
	}
}

// TestRenderCodeAndList 代码块与列表在渲染后仍保留结构（\n 足够覆盖行数）。
func TestRenderCodeAndList(t *testing.T) {
	r, _ := glamour.NewTermRenderer(glamour.WithAutoStyle())
	out, _ := r.Render("```go\nfmt.Println(\"a\\nb\")\n```\n\n- 列表项1\n- 列表项2")
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "fmt.Println") {
		t.Fatalf("code lost: %q", plain)
	}
	if !strings.Contains(plain, "列表项1") || !strings.Contains(plain, "列表项2") {
		t.Fatalf("list lost: %q", plain)
	}
}
