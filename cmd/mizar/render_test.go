package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
	"github.com/gdamore/tcell/v2"
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

// ---------------------------------------------------------------------------
// 命令输出渲染（回归：/help 换行被 glamour 重排合并、末尾几行丢色）
// ---------------------------------------------------------------------------

// drawAndRowColors 把 tuiModel 的聊天区画到模拟屏幕，返回每行（非空）的前景色。
// 行文本直接从屏幕像素重取——不能用 GetText 按行切，
// 因为屏幕是按渲染宽度折行的，与源文本行号不一致。
// 颜色丢失/换行重排这类「渲染后视觉状态」问题只能这样实证。
func drawAndRowColors(t *testing.T, m *tuiModel) map[int]tcell.Color {
	t.Helper()
	sc := tcell.NewSimulationScreen("UTF-8")
	if err := sc.Init(); err != nil {
		t.Fatal(err)
	}
	defer sc.Fini()
	m.textView.SetRect(0, 0, 200, 30)
	m.textView.Draw(sc)
	out := map[int]tcell.Color{}
	w, h := sc.Size()
	for row := 0; row < h; row++ {
		var text strings.Builder
		var fg tcell.Color
		found := false
		for col := 0; col < w; col++ {
			r, _, style, _ := sc.GetContent(col, row)
			if r != ' ' {
				text.WriteRune(r)
			}
			c, _, _ := style.Decompose()
			if c != tcell.ColorDefault {
				if !found {
					fg, found = c, true
				}
			}
		}
		if strings.TrimSpace(text.String()) != "" {
			out[row] = fg
		}
	}
	return out
}

// TestCommandOutputLinesAndColorPreserved 命令输出（role=cmd）必须：
//  1. 每条命令占一行（glamour 的段落重排会把多行合并成一段）
//  2. 含 [args...] 描述的行之后，后续行颜色不丢
//     （tview 会把 [args...] 当颜色标签，吞掉前面的 [green] 状态）
func TestCommandOutputLinesAndColorPreserved(t *testing.T) {
	m := newTUIForQueueTest()
	m.lines = nil // 清掉 harness 的 banner 行（system 无色，干扰逐行颜色断言）
	help := "可用命令：\n" +
		"  /model       查看/列出模型\n" +
		"  /handoff     生成交接文档（/handoff [备注/下一步]）\n" +
		"  /template    展开 prompt 模板（/template <name> [args]）\n" +
		"  /mcp         MCP server 管理（/mcp add <name> [args...]）\n" +
		"  /help        列出所有可用命令\n"
	m.lines = append(m.lines, chatLine{role: "cmd", content: help, ts: now()})
	m.renderAllDirect()

	text := m.textView.GetText(true)
	for _, name := range []string{"/model", "/handoff", "/template", "/mcp", "/help"} {
		if !strings.Contains(text, name) {
			t.Fatalf("命令 %q 不在渲染结果中（可能被重排/吞掉）:\n%s", name, text)
		}
	}
	// <name> 不得被 markdown 当 HTML 标签吃掉
	if !strings.Contains(text, "<name>") {
		t.Fatalf("<name> 被 markdown 吞掉:\n%s", text)
	}
	// 所有可见行的颜色应保持 aiColor（默认 green），不允许后面几行丢色
	for row, fg := range drawAndRowColors(t, m) {
		if fg.String() != tcell.ColorGreen.String() {
			t.Fatalf("第 %d 行丢色: fg=%v\n%s", row, fg, text)
		}
	}
}

// TestUserAndToolTextEscapeBrackets 用户消息/工具输出里的 [...] 不得当颜色标签。
func TestUserAndToolTextEscapeBrackets(t *testing.T) {
	m := newTUIForQueueTest()
	m.lines = nil // 清掉 harness 的 banner 行
	m.lines = append(m.lines,
		chatLine{role: "user", content: "看看 [config] 里的 [args]", ts: now()},
		chatLine{role: "tool", content: "→ 输出 [plugins.toml] 已加载", ts: now()},
	)
	m.renderAllDirect()
	text := m.textView.GetText(true)
	if !strings.Contains(text, "[config]") || !strings.Contains(text, "[plugins.toml]") {
		t.Fatalf("方括号内容应原样显示:\n%s", text)
	}
	// 第 1 行 user 色（默认 white）、第 2 行 tool 色（cyan=teal），一行都不能丢
	want := []tcell.Color{tcell.ColorWhite, tcell.ColorTeal}
	got := []tcell.Color{}
	for _, fg := range drawAndRowColors(t, m) {
		got = append(got, fg)
	}
	if len(got) != len(want) {
		t.Fatalf("可见行数 = %d, want %d:\n%s", len(got), len(want), text)
	}
	for i, fg := range got {
		if fg.String() != want[i].String() {
			t.Fatalf("第 %d 行颜色 = %v, want %v\n%s", i, fg, want[i], text)
		}
	}
}
