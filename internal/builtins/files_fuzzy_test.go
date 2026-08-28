package builtins

import (
	"testing"
)

func TestNormalizeForFuzzyMatch(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// 全角字母数字 → 半角
		{"ＡＢＣ１２３", "ABC123"},
		// 智能引号 → ASCII
		{"'hello'", "'hello'"},
		{"\"world\"", "\"world\""},
		// 各种破折号 → -
		{"a–b", "a-b"},   // en-dash
		{"a—b", "a-b"},   // em-dash
		{"a‐b", "a-b"},   // hyphen
		// 特殊空格 → 普通空格
		{"a b", "a b"},   // NBSP
		{"a　b", "a b"},  // 全角空格
		// 连字
		{"ﬁre", "fire"},
		{"ﬂag", "flag"},
	}
	for _, tt := range tests {
		got := normalizeForFuzzyMatch(tt.in)
		if got != tt.want {
			t.Errorf("normalizeForFuzzyMatch(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectLineEndings(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"line1\nline2\n", "\n"},
		{"line1\r\nline2\r\n", "\r\n"},
		{"line1\rline2\r", "\n"}, // 旧 Mac 风格，兜底 LF
		{"", "\n"},
	}
	for _, tt := range tests {
		got := detectLineEndings(tt.content)
		if got != tt.want {
			t.Errorf("detectLineEndings(%q) = %q, want %q", tt.content, got, tt.want)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	// 不截断
	trunc, shown, reason, _, _ := truncateOutput("hello\nworld")
	if trunc {
		t.Fatalf("expected no truncation")
	}
	if shown != 2 {
		t.Errorf("shown = %d, want 2", shown)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}

	// 字节截断
	long := ""
	for i := 0; i < 20000; i++ {
		long += "x"
	}
	long += "\n"
	trunc, shown, reason, _, _ = truncateOutput(long)
	if !trunc {
		t.Fatalf("expected truncation")
	}
	if reason != "bytes" {
		t.Errorf("reason = %q, want bytes", reason)
	}
	if shown == 0 {
		t.Error("shown should be > 0")
	}
}
