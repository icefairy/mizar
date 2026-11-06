package main

import (
	"testing"

	"github.com/charmbracelet/glamour"
)

// TestEscJunkRune ConPTY 泄漏鼠标序列的字符判定：
// 只有 '[' / '<' 能作为序列起始；单独的数字绝不能被当成泄漏（否则用户回「1」会被吞）。
func TestEscJunkRune(t *testing.T) {
	cases := []struct {
		r     rune
		inSeq bool
		want  bool
	}{
		{'[', false, true},
		{'<', false, true},
		{'1', false, false}, // 用户回答选项：必须放行
		{'M', false, false},
		{';', false, false},
		{'1', true, true},
		{'M', true, true},
		{'m', true, true},
		{';', true, true},
		{'a', true, false},
		{'中', true, false},
	}
	for _, c := range cases {
		if got := escJunkRune(c.r, c.inSeq); got != c.want {
			t.Fatalf("escJunkRune(%q, %v) = %v, want %v", c.r, c.inSeq, got, c.want)
		}
	}
}

// TestChatLineMdCache Markdown 渲染缓存：content 不变时复用结果，
// content 变化后必须重新渲染（否则流式更新会显示旧内容）。
func TestChatLineMdCache(t *testing.T) {
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle())
	if err != nil {
		t.Fatal(err)
	}
	l := &chatLine{role: "bot", content: "第一版"}
	first, ok := l.mdRendered(r)
	if !ok || first == "" {
		t.Fatal("首次渲染应成功")
	}
	if !l.mdCacheOK {
		t.Fatal("渲染后应写入缓存")
	}
	// 再次渲染：命中缓存，结果一致
	again, ok := l.mdRendered(r)
	if !ok || again != first {
		t.Fatalf("缓存命中结果应一致: %q vs %q", again, first)
	}
	// 内容变化：必须失效并重新渲染
	l.content = "第二版"
	changed, ok := l.mdRendered(r)
	if !ok {
		t.Fatal("内容变化后应重新渲染成功")
	}
	if changed == first {
		t.Fatal("内容变化后缓存未失效")
	}
	// renderer 为 nil 时返回 ok=false（由调用方回退原文）
	if _, ok := l.mdRendered(nil); ok {
		t.Fatal("renderer 为 nil 时应返回 ok=false")
	}
}
