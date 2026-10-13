package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func mkTC(name, args string) toolCall {
	var tc toolCall
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

// 单工具调用：正常转换，reason 落入协议 JSON。
func TestToolCallsToText_Single(t *testing.T) {
	out := toolCallsToText([]toolCall{mkTC("read", `{"path":"x"}`)}, "我读一下这个文件")
	var req struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
		Args   string `json:"args"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatalf("输出应是合法 JSON: %v (out=%q)", err, out)
	}
	if req.Action != "tool" || req.Tool != "read" || req.Reason != "我读一下这个文件" {
		t.Fatalf("字段不符: %+v", req)
	}
	if req.Args != `{"path":"x"}` {
		t.Fatalf("args 应是未转义的原始参数字符串, got %q", req.Args)
	}
}

// 多个工具调用：mizar 的 callRequest 是单数结构，只能执行首个。
// 这里锁定「取首个」的行为，并保证结果仍是合法 JSON（丢弃不能污染协议）。
func TestToolCallsToText_DropsExtraButStaysValid(t *testing.T) {
	out := toolCallsToText([]toolCall{
		mkTC("read", `{"path":"a"}`),
		mkTC("write", `{"path":"b"}`),
		mkTC("bash", `{"command":"ls"}`),
	}, "")
	var req struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatalf("多工具调用时必须仍输出合法 JSON: %v (out=%q)", err, out)
	}
	if req.Tool != "read" {
		t.Fatalf("应取首个工具调用 read, got %q", req.Tool)
	}
	if strings.Contains(out, "write") || strings.Contains(out, "bash") {
		t.Fatalf("被丢弃的工具名不应出现在输出中: %q", out)
	}
}

// reason 过长必须截断（避免把大段分析灌进协议 JSON，弱模型易写畸形）。
func TestToolCallsToText_ReasonTruncated(t *testing.T) {
	long := strings.Repeat("分析", 600) // 1200 runes，超过 reasonMaxLen(500)
	out := toolCallsToText([]toolCall{mkTC("read", `{}`)}, long)

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatalf("截断后必须仍是合法 JSON: %v", err)
	}
	got := []rune(req.Reason)
	if len(got) > reasonMaxLen+3 { // 允许省略号
		t.Fatalf("reason 应被截断到 ~%d runes, got %d", reasonMaxLen, len(got))
	}
	if !strings.HasSuffix(req.Reason, "..") {
		t.Fatalf("截断后应带省略号, got %q", req.Reason[len(req.Reason)-10:])
	}
}

// 空 reason 不应产生 reason 字段（保持与旧行为一致）。
func TestToolCallsToText_EmptyReasonOmitsField(t *testing.T) {
	out := toolCallsToText([]toolCall{mkTC("read", `{}`)}, "")
	if strings.Contains(out, `"reason"`) {
		t.Fatalf("空 reason 不应输出 reason 字段: %q", out)
	}
}
