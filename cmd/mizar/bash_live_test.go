package main

import (
	"strings"
	"testing"
)

func TestTailLinesKeepsTail(t *testing.T) {
	in := "l1\nl2\nl3\nl4\nl5"
	got := tailLines(in, 2)
	if got != "l4\nl5" {
		t.Fatalf("应保留末尾 2 行，得到 %q", got)
	}
}

func TestTailLinesFewerThanN(t *testing.T) {
	if got := tailLines("only", 10); got != "only" {
		t.Fatalf("行数不足应返回全部，得到 %q", got)
	}
}

func TestTailLinesEmpty(t *testing.T) {
	if got := tailLines("", 5); got != "" {
		t.Fatalf("空输入应返回空，得到 %q", got)
	}
	if got := tailLines("\n\n", 5); got != "" {
		t.Fatalf("仅换行应返回空，得到 %q", got)
	}
}

func TestTailLinesTruncatesLongLine(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := tailLines(long, 5)
	if len([]rune(got)) > 210 {
		t.Fatalf("超长行应被截断，长度=%d", len([]rune(got)))
	}
}

func TestTailLinesTrailingNewline(t *testing.T) {
	got := tailLines("a\nb\n", 5)
	if got != "a\nb" {
		t.Fatalf("末尾换行应被去掉，得到 %q", got)
	}
}

func TestBashLiveLifecycle(t *testing.T) {
	m := &tuiModel{}
	m.clearBashLive()
	if m.bashLiveActive() {
		t.Fatal("初始应为非活跃")
	}
	m.bashMu.Lock()
	m.bashLive = "some output"
	m.bashLiveOn = true
	m.bashMu.Unlock()
	if !m.bashLiveActive() {
		t.Fatal("设置后应为活跃")
	}
	m.clearBashLive()
	if m.bashLiveActive() || m.bashLive != "" {
		t.Fatal("清空后应复位")
	}
}
