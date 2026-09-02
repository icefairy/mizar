package agent

import (
	"strings"
	"testing"
)

// testBadToolJSON 是含未转义引号（"offset": 1）的坏工具 JSON，
// 模型在解析失败后常把它包在 <reply> 里再次输出。
const testBadToolJSON = `{"action":"tool","tool":"read","args":"{\"limit\": 80, \"offset": 1, \"path": "/data/codes/mizar/internal/plugins/loader.go\"}"}`

// TestParseCallJSON_BadToolJSONWrappedInReply 坏 JSON 被包在 <reply> 中时，
// 应整体解析为 reply（人工判断），而不是错误地提取内层 action 字段。
func TestParseCallJSON_BadToolJSONWrappedInReply(t *testing.T) {
	wrapped := "<reply>我需要读取这个文件：\n" + testBadToolJSON + "</reply>"
	req, err := parseCallJSON(wrapped)
	if err != nil {
		t.Fatalf("parseCallJSON(wrapped) err = %v", err)
	}
	if req == nil || req.Action != "reply" {
		t.Fatalf("action = %v, want reply", req.Action)
	}
}

// TestParseCallJSON_BadToolJSONPlain 纯坏 JSON（未包裹）应报错而非返回半成品请求。
func TestParseCallJSON_BadToolJSONPlain(t *testing.T) {
	_, err := parseCallJSON(testBadToolJSON)
	if err == nil {
		t.Fatal("expected error for raw bad JSON")
	}
	if !strings.Contains(err.Error(), "无法识别的控制指令") && !strings.Contains(err.Error(), "JSON 格式错误") && !strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParseCallJSON_WrappedValidToolJSON 有效工具 JSON 包在 <reply> 里（模型常见误格式），
// 解析为 reply 时不丢失文本（保留给用户展示/继续对话），不触发工具执行。
func TestParseCallJSON_WrappedValidToolJSON(t *testing.T) {
	valid := `{"action":"tool","tool":"read","args":"{\"path\": \"a.go\"}"}`
	wrapped := "<reply>我先读取文件：\n" + valid + "</reply>"
	req, err := parseCallJSON(wrapped)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if req.Action != "reply" {
		t.Fatalf("action = %q, want reply", req.Action)
	}
	if !strings.Contains(req.Text, "我先读取文件") {
		t.Fatalf("text = %q", req.Text)
	}
}
