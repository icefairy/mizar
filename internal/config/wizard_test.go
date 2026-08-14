package config

import (
	"strings"
	"testing"
)

// TestWizardParse 解析 wizard 命令。
func TestWizardParse(t *testing.T) {
	cases := []struct {
		line string
		name string
		args string
	}{
		{"/provider", "provider", ""},
		{"/provider http://127.0.0.1:3002/v1", "provider", "http://127.0.0.1:3002/v1"},
		{"/model", "model", ""},
		{"/model deepseek-v4-flash", "model", "deepseek-v4-flash"},
		{"/think on", "think", "on"},
		{"/context 128000", "context", "128000"},
		{"/context auto", "context", "auto"},
		{"/save", "save", ""},
		{"普通任务", "", ""},
	}
	for _, c := range cases {
		name, args := parseWizardCmd(c.line)
		if name != c.name || args != c.args {
			t.Errorf("parse(%q) = (%q, %q), want (%q, %q)", c.line, name, args, c.name, c.args)
		}
	}
}

// TestWizardFlow 向导主流程：provider → model → think → context → save。
func TestWizardFlow(t *testing.T) {
	// 模拟用户输入（通过 scan 函数注入）
	inputs := []string{
		"/provider http://example.com/v1",
		"sk-test",           // api key
		"/model mock-model", // 选择模型
		"/think on",
		"/context auto",
		"/save",
	}
	scanned := 0
	w := &wizard{
		cfg: &Config{},
		scan: func() (string, error) {
			if scanned >= len(inputs) {
				return "/quit", nil
			}
			line := inputs[scanned]
			scanned++
			return line, nil
		},
		save: func() error { return nil },
		listModels: func(baseURL, apiKey string) ([]string, error) {
			return []string{"mock-model", "other-model"}, nil
		},
		probeThinking: func(baseURL, apiKey, model string) (bool, error) {
			return true, nil
		},
		probeContext: func(baseURL, model string) int {
			return 256000
		},
	}
	out, err := w.Run()
	if err != nil {
		t.Fatalf("wizard run: %v", err)
	}
	if !strings.Contains(out, "配置已保存") {
		t.Fatalf("wizard output missing save ack: %s", out)
	}
	c := w.cfg
	if c.BaseURL != "http://example.com/v1" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", c.APIKey)
	}
	if c.Model != "mock-model" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.ThinkingLevel != "medium" {
		t.Errorf("ThinkingLevel = %q, want medium", c.ThinkingLevel)
	}
	if c.ContextWindow != 256000 {
		t.Errorf("ContextWindow = %d, want 256000", c.ContextWindow)
	}
}

// TestWizardHelp 输入 /help 应展示命令列表。
func TestWizardHelp(t *testing.T) {
	n := 0
	w := &wizard{
		cfg: &Config{},
		scan: func() (string, error) {
			n++
			if n == 1 {
				return "/help", nil
			}
			return "", errScanEOF
		},
	}
	out, _ := w.Run()
	for _, cmd := range []string{"/provider", "/model", "/think", "/context", "/save"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help missing %s", cmd)
		}
	}
}
