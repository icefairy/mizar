package server

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  修复多会话排队问题  ", "修复多会话排队问题"},
		{"“自动会话标题”", "自动会话标题"},
		{"「标题生成」\n多余的第二行", "标题生成"},
		{"会话主题。", "会话主题"},
		{"这是一个超过十二个字的很长很长的会话主题标题", "这是一个超过十二个字的很"},
		{"", ""},
	}
	for _, c := range cases {
		got := sanitizeTitle(c.in)
		if got != c.want {
			t.Fatalf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > titleMaxRunes {
			t.Fatalf("sanitizeTitle(%q) 超长: %d runes", c.in, n)
		}
	}
}

func TestShouldAutoTitle(t *testing.T) {
	msgs := func(n int) []convMessage {
		out := make([]convMessage, n)
		for i := range out {
			out[i] = convMessage{Role: "user", Content: "x"}
		}
		return out
	}
	cases := []struct {
		name string
		c    *conversation
		want bool
	}{
		{"nil", nil, false},
		{"3条不生成", &conversation{Messages: msgs(3)}, false},
		{"4条首次生成", &conversation{Messages: msgs(4)}, true},
		{"手动标题不覆盖", &conversation{Messages: msgs(10), TitleManual: true}, false},
		{"已生成且新增<2跳过", &conversation{Messages: msgs(5), TitleGenAt: 4}, false},
		{"已生成且新增>=2重新生成", &conversation{Messages: msgs(6), TitleGenAt: 4}, true},
	}
	for _, c := range cases {
		if got := shouldAutoTitle(c.c); got != c.want {
			t.Fatalf("%s: shouldAutoTitle = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSetAutoTitleAndManual(t *testing.T) {
	st := newWebStore(filepath.Join(t.TempDir(), "webui.json"))
	c := st.create("chat")

	if !st.setAutoTitle(c.ID, "  自动标题  ", 4) {
		t.Fatal("首次 setAutoTitle 应成功")
	}
	got := st.get(c.ID)
	if got.Title != "自动标题" {
		t.Fatalf("Title = %q, want 自动标题", got.Title)
	}
	if got.TitleGenAt != 4 {
		t.Fatalf("TitleGenAt = %d, want 4", got.TitleGenAt)
	}

	// 空标题不写入
	if st.setAutoTitle(c.ID, "   ", 5) {
		t.Fatal("空标题不应写入")
	}

	// 标记手动后，自动标题不得覆盖
	st.markTitleManual(c.ID, true)
	if st.setAutoTitle(c.ID, "新自动标题", 6) {
		t.Fatal("手动标题后 setAutoTitle 应被拒绝")
	}
	if got := st.get(c.ID); got.Title != "自动标题" {
		t.Fatalf("手动后 Title 被改成 %q", got.Title)
	}

	// 未知会话返回 false
	if st.setAutoTitle("nope", "x", 1) {
		t.Fatal("未知会话不应写入")
	}
}

func TestTitleModelSettingPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webui.json")
	st := newWebStore(path)
	st.saveSettings(webSettings{TitleModel: "gpt-4o-mini"})
	// 重新加载，确认 title_model 落盘可读
	st2 := newWebStore(path)
	if got := strings.TrimSpace(st2.getSettings().TitleModel); got != "gpt-4o-mini" {
		t.Fatalf("TitleModel = %q, want gpt-4o-mini", got)
	}
}
