package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAtRefIgnoresInlineAt 回归：粘贴带 @ 的密码/邮箱时，
// @ 紧跟在词内字符之后（p@ssw0rd、user@example.com）不是引用，必须原样保留。
func TestResolveAtRefIgnoresInlineAt(t *testing.T) {
	cases := []string{
		"我的密码是 p@ssw0rd",
		"联系 user@example.com",
		"token: abc@def",
		"路径是 a.b@c-d",
		"中文前缀 中@foo",
	}
	for _, in := range cases {
		if got := resolveAtRef(in, nil, nil); got != in {
			t.Errorf("resolveAtRef(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestResolveAtRefRequiresExistingPath 回归：@ 后是裸路径时，只有路径真实存在才解析；
// 否则视为普通文本（行首 @ssw0rd 这类密码不能被吞掉）。
func TestResolveAtRefRequiresExistingPath(t *testing.T) {
	if got := resolveAtRef("@ssw0rd", nil, nil); got != "@ssw0rd" {
		t.Errorf("nonexistent bare path should stay untouched, got %q", got)
	}
	if got := resolveAtRef("密码 @ssw0rd 记住了", nil, nil); got != "密码 @ssw0rd 记住了" {
		t.Errorf("nonexistent bare path should stay untouched, got %q", got)
	}

	f := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := "看看 @" + f
	want := "看看 读取文件 " + f
	if got := resolveAtRef(in, nil, nil); got != want {
		t.Errorf("existing path: got %q want %q", got, want)
	}
}

// TestResolveAtRefExplicitPrefix 显式前缀（cmd:/tool:/file:/read:/dir:）表达明确意图，
// 即使目标不存在也照常解析。
func TestResolveAtRefExplicitPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"@file:nope.txt", "读取文件 nope.txt"},
		{"@read:nope.txt", "读取文件 nope.txt"},
		{"@dir:nope", "查看目录 nope"},
		{"@cmd:reload", "执行命令 /reload"},
		{"@tool:bash", "使用工具 bash"},
		{"请 @file:nope.txt 看看", "请 读取文件 nope.txt 看看"},
	}
	for _, c := range cases {
		if got := resolveAtRef(c.in, nil, nil); got != c.want {
			t.Errorf("resolveAtRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveAtRefQuotedPath 带空格的路径用 @"..." 引号形式（对齐 pi），
// token 只取引号内内容，引号后的文本保留。
func TestResolveAtRefQuotedPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "my file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := `看看 @"` + f + `" 总结下`
	want := "看看 读取文件 " + f + " 总结下"
	if got := resolveAtRef(in, nil, nil); got != want {
		t.Errorf("quoted path: got %q want %q", got, want)
	}

	// 引号内路径不存在 → 原样保留
	if got := resolveAtRef(`@"no such file"`, nil, nil); got != `@"no such file"` {
		t.Errorf("quoted nonexistent should stay untouched, got %q", got)
	}
}

// TestResolveAtRefKeepsTrailingText @ 引用的后续自然语言不能被吞掉。
func TestResolveAtRefKeepsTrailingText(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := "帮我 @" + f + " 提取要点"
	want := "帮我 读取文件 " + f + " 提取要点"
	if got := resolveAtRef(in, nil, nil); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestAtAfterToken 补全输入片段提取：显式前缀保留冒号，普通路径取到空白，引号取引号内。
func TestAtAfterToken(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"@file:ab", "file:ab"},
		{"@cmd:re", "cmd:re"},
		{"@dir:/tmp", "dir:/tmp"},
		{"@src/ma", "src/ma"},
		{`@"a b`, "a b"},
		{"@", ""},
	}
	for _, c := range cases {
		idx := lastAtMentionIndex(c.line)
		if got := atAfterToken(c.line, idx); got != c.want {
			t.Errorf("atAfterToken(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// TestLastAtMentionIndex 边界判定：@ 前是词内字符（字母/数字/下划线/连字符等）不算引用起点。
func TestLastAtMentionIndex(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"@foo", 0},
		{"a@b", -1},
		{"a b@c", -1},
		{"a @b", 2},
		{"a\t@b", 2},
		{"(中文)@b", 8}, // ')' 是标点，属于边界；词内 @ 才是非边界
		{"(p)@ss", 3},
		{`"@b"`, 1},
		{"中@b", -1},
		{"abc", -1},
		{"p@ss@word", -1},
		{"p@ss @word", 5},
	}
	for _, c := range cases {
		if got := lastAtMentionIndex(c.line); got != c.want {
			t.Errorf("lastAtMentionIndex(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

// TestAtToken 引用 token 提取：普通到空白为止，引号形式取引号内。
func TestAtToken(t *testing.T) {
	cases := []struct {
		line      string
		atIdx     int
		wantToken string
		wantEnd   int
		wantOK    bool
	}{
		{"@a/b.txt 后文", 0, "a/b.txt", 8, true},
		{`@"a b.txt" 后文`, 0, "a b.txt", 10, true},
		{"@", 0, "", 0, false},
		{"@ 空格", 0, "", 0, false},
		{`@"未闭合`, 0, "", 0, false},
	}
	for _, c := range cases {
		token, end, ok := atToken(c.line, c.atIdx)
		if token != c.wantToken || end != c.wantEnd || ok != c.wantOK {
			t.Errorf("atToken(%q, %d) = (%q, %d, %v), want (%q, %d, %v)",
				c.line, c.atIdx, token, end, ok, c.wantToken, c.wantEnd, c.wantOK)
		}
	}
}
