package agent

import (
	"strings"
	"testing"
)

// TestExtractActionJSON_MixedTextAndTool 复现用户报告的场景：
// 模型输出"解释文本 + tool JSON"混合内容，此前被整段包装成 reply 导致
// tool JSON 原样显示、工具不执行。现在应提取出 tool JSON 触发工具调用。
func TestExtractActionJSON_MixedTextAndTool(t *testing.T) {
	mixed := "文件内容已被更新，API路径格式是 `/api/v1/memory/{namespace}/{path}`。让我测试一下：\n\n" +
		`{"action":"tool","tool":"memory_stats","args":"{}"}`
	got, ok := ExtractActionJSON(mixed)
	if !ok {
		t.Fatalf("should extract action JSON from mixed text")
	}
	req, err := parseCallJSON(mixed)
	if err != nil {
		t.Fatalf("parseCallJSON(mixed) err = %v", err)
	}
	if req.Action != "tool" || req.Tool != "memory_stats" {
		t.Fatalf("parsed action=%q tool=%q, want tool/memory_stats", req.Action, req.Tool)
	}
	if !strings.Contains(got, "memory_stats") {
		t.Fatalf("extracted text = %q", got)
	}
}

// TestExtractActionJSON_BracesInText 文本含花括号占位符（API 路径 {namespace}）不干扰提取。
// 旧实现 Index("{")~LastIndex("}") 会从 {namespace} 截起导致解析失败。
func TestExtractActionJSON_BracesInText(t *testing.T) {
	mixed := "路径模板 /api/v1/{ns}/{path} 中的 {ns} 是命名空间。\n" +
		`{"action":"tool","tool":"bash","args":"{\"command\":\"echo hi\"}"}`
	req, err := parseCallJSON(mixed)
	if err != nil {
		t.Fatalf("parseCallJSON err = %v", err)
	}
	if req.Action != "tool" || req.Tool != "bash" {
		t.Fatalf("parsed action=%q tool=%q", req.Action, req.Tool)
	}
	if req.Args != `{"command":"echo hi"}` {
		t.Fatalf("args = %q", req.Args)
	}
}

// TestExtractActionJSON_ReplyWithEscapedToolInText reply 的 text 里嵌有转义的 tool JSON
// 时，不应误提取内层（转义形态解码必然失败），应解析出 reply。
func TestExtractActionJSON_ReplyWithEscapedToolInText(t *testing.T) {
	reply := `{"action":"reply","text":"用法示例：{\"action\":\"tool\",\"tool\":\"x\",\"args\":\"{}\"}"}`
	got, ok := ExtractActionJSON(reply)
	if !ok {
		t.Fatal("should extract reply JSON")
	}
	req, err := parseCallJSON(reply)
	if err != nil {
		t.Fatalf("parseCallJSON err = %v", err)
	}
	if req.Action != "reply" {
		t.Fatalf("action = %q, want reply", req.Action)
	}
	if !strings.Contains(got, "reply") {
		t.Fatalf("extracted = %q", got)
	}
}

// TestExtractActionJSON_Fenced 围栏包裹的控制 JSON。
func TestExtractActionJSON_Fenced(t *testing.T) {
	fenced := "```json\n{\"action\":\"reply\",\"text\":\"你好\"}\n```"
	req, err := parseCallJSON(fenced)
	if err != nil {
		t.Fatalf("parseCallJSON err = %v", err)
	}
	if req.Action != "reply" || req.Text != "你好" {
		t.Fatalf("parsed = %+v", req)
	}
}

// TestExtractActionJSON_ToolPriority 混合文本同时含 reply 和 tool JSON 时 tool 优先。
// （模型"想调工具但先说了句话"的场景应执行工具而非显示文本）
func TestExtractActionJSON_ToolPriority(t *testing.T) {
	mixed := `{"action":"reply","text":"先汇报一下"}` + "\n" +
		`{"action":"tool","tool":"ls","args":"{}"}`
	req, err := parseCallJSON(mixed)
	if err != nil {
		t.Fatalf("parseCallJSON err = %v", err)
	}
	if req.Action != "tool" || req.Tool != "ls" {
		t.Fatalf("action=%q tool=%q, want tool/ls（工具优先）", req.Action, req.Tool)
	}
}

// TestExtractActionJSON_PlainReplyText 纯文本（无控制 JSON）→ 不提取，走 reply 包装。
func TestExtractActionJSON_PlainReplyText(t *testing.T) {
	plain := "这只是普通说明文字，包含 {curly} 占位与 {\"a\":1} 样例。"
	if _, ok := ExtractActionJSON(plain); ok {
		t.Fatal("plain text without action should not extract")
	}
}
