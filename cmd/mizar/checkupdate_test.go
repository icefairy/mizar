package main

import (
	"strings"
	"testing"
)

// TestParseSiteVersion 站点 HTML 里的版本徽章与最新动态标题都要能解析出来。
func TestParseSiteVersion(t *testing.T) {
	html := `<div class="container">
    <header>
      <div class="badge">AGPL v3</div><div class="badge">v0.5.3</div>
      <h1>开阳 <span>Mizar</span></h1>
    </header>
    <section id="news">
      <h2>最新动态</h2>
      <div class="grid cols-2">
        <div class="card">
          <h3>v0.5.3 · 时间感知 + 消息时间戳</h3>
          <p>会话文件每条消息带 timestamp。</p>
        </div>
        <div class="card">
          <h3>v0.5.2 · 旧版本</h3>
          <p>旧内容。</p>
        </div>
      </div>
    </section>
  </div>`
	info := parseSiteInfo(html)
	if info.Version != "v0.5.3" {
		t.Errorf("版本解析错误: got %q want v0.5.3", info.Version)
	}
	if info.Title != "时间感知 + 消息时间戳" {
		t.Errorf("标题解析错误: got %q", info.Title)
	}
	if !strings.Contains(info.Summary, "timestamp") {
		t.Errorf("摘要应含 timestamp，实际 %q", info.Summary)
	}
}

// TestParseSiteVersionIgnoresAgpl AGPL v3 这类非语义化版本不能被误当版本号。
func TestParseSiteVersionIgnoresAgpl(t *testing.T) {
	html := `<div class="badge">AGPL v3</div><div class="badge">v1.2.3</div>`
	info := parseSiteInfo(html)
	if info.Version != "v1.2.3" {
		t.Errorf("应取语义化版本 v1.2.3，实际 %q", info.Version)
	}
}

// TestCompareSemver 语义化版本比较（只看数字段，忽略前缀 v）。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.5.5", "v0.5.3", 1},
		{"v0.5.3", "v0.5.5", -1},
		{"v0.5.5", "v0.5.5", 0},
		{"0.5.5", "v0.5.5", 0},
		{"v0.10.0", "v0.9.9", 1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.5.5", "v0.5", 1},
		{"v0.5.5", "", 1},
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		if got != c.want {
			t.Errorf("compareSemver(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseSiteInfoNoNews 没有最新动态段落时也要能拿到版本，不 panic。
func TestParseSiteInfoNoNews(t *testing.T) {
	info := parseSiteInfo(`<div class="badge">v2.0.0</div>`)
	if info.Version != "v2.0.0" {
		t.Errorf("got %q", info.Version)
	}
	if info.Title != "" || info.Summary != "" {
		t.Errorf("无动态段落时标题/摘要应为空，实际 %q / %q", info.Title, info.Summary)
	}
}

// TestParseSiteInfoGarbage 垃圾输入不能 panic。
func TestParseSiteInfoGarbage(t *testing.T) {
	for _, s := range []string{"", "hello", "<html>", "<<<>>>", "v"} {
		info := parseSiteInfo(s)
		_ = info
	}
}
