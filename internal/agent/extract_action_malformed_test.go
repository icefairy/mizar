package agent

import (
	"testing"
)

// TestStreamExtractor_MalformedToolCallJSON 复现用户报告的场景：
// 模型输出畸形的工具调用 JSON（缺 tool 字段、内层引号未转义），
// 此前 streamCore 将其包装成 reply 导致用户看到原始 JSON 且任务提前结束。
// 期望：StreamTextExtractor 不把这种内容当 reply 显示。
func TestStreamExtractor_MalformedToolCallJSON(t *testing.T) {
	// 模型输出的畸形 JSON：缺 "tool" 字段，args 内层引号未转义
	malformed := `{"action":"tool","args":"{"command": "tail -100 /var/log/x11vnc.log 2>&1"}`
	e := &StreamTextExtractor{}
	out := e.Feed(malformed)
	if out != "" {
		t.Fatalf("malformed tool JSON should not be displayed as reply, got %q", out)
	}
	if e.Replying() {
		t.Fatal("malformed tool JSON should not set Replying=true")
	}
}

// TestExtractActionJSON_MalformedToolCallJSON 确认 ExtractActionJSON 无法识别畸形 JSON。
// 这是根因所在：识别失败导致 streamCore 把它包成 reply。
func TestExtractActionJSON_MalformedToolCallJSON(t *testing.T) {
	malformed := `{"action":"tool","args":"{"command": "tail -100 /var/log/x11vnc.log 2>&1"}`
	_, ok := ExtractActionJSON(malformed)
	if ok {
		t.Fatal("ExtractActionJSON should NOT extract malformed JSON (this test documents the limitation)")
	}
}

// TestParseCallJSON_MalformedToolCallNoToolField 缺 tool 字段但 action=tool 的合法 JSON。
// 例如模型模仿了 args 为嵌套对象的 OpenAI 原生格式：{"action":"tool","args":{...}}
func TestParseCallJSON_MalformedToolCallNoToolField(t *testing.T) {
	// 变体1：args 为 JSON 对象（非字符串）—— 模型混淆了 OpenAI 原生格式
	native := `{"action":"tool","args":{"command": "tail -100 /var/log/x11vnc.log 2>&1"}}`
	_, err := parseCallJSON(native)
	if err == nil {
		t.Fatal("args as object should fail parse (args field is string)")
	}
	// 变体2：args 为合法转义字符串但缺 tool 字段
	noTool := `{"action":"tool","args":"{\"command\": \"tail -100\"}"}`
	req, err := parseCallJSON(noTool)
	if err != nil {
		t.Fatalf("valid JSON with missing tool should parse (action present): %v", err)
	}
	if req.Action != "tool" || req.Tool != "" {
		t.Fatalf("got action=%q tool=%q, want tool/empty", req.Action, req.Tool)
	}
	// 注意：此处 Agent 循环会因 Tool=="" 而调用 a.Plugins.Call("", ...) → tool not found
	// 这个场景不需要 streamCore 层修复（JSON 可解析，错误由循环内处理）
}

// TestParseCallJSON_TruncatedToolCall 截断的工具调用 JSON（模型 max_tokens 中断）。
// 例如：{"action":"tool","tool":"bash","args":"{\"command\": \"tail -100 /var/log
// 截断时引号/括号不闭合，ExtractActionJSON 也无法识别。
func TestParseCallJSON_TruncatedToolCall(t *testing.T) {
	truncated := `{"action":"tool","tool":"bash","args":"{\"command\": \"tail -100 /var/log`
	_, ok := ExtractActionJSON(truncated)
	if ok {
		t.Fatal("truncated JSON should not extract")
	}
}
