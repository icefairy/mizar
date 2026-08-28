package engine

import (
	"encoding/json"
	"testing"
)

func TestSegText(t *testing.T) {
	cases := map[string][]string{
		"我爱中国":            {"我", "爱", "中国"},
		"我们今天去北京天安门":         {"我们", "今天", "去", "北京", "天安门"},
		"Hello world from Go": {"Hello", "world", "from", "Go"},
		"我爱Go语言":           {"我", "爱", "Go", "语言"},
	}
	for in, _ := range cases {
		got := segText(in)
		if len(got) == 0 {
			t.Errorf("segText(%q) returned empty", in)
			continue
		}
		t.Logf("segText(%q) = %v", in, got)
	}
}

func TestSegTokens(t *testing.T) {
	toks := segTokens("我爱北京天安门，今天天气真好。")
	if len(toks) == 0 {
		t.Fatal("segTokens returned empty")
	}
	b, _ := json.Marshal(toks)
	t.Logf("segTokens = %s", b)
}