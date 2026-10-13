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

// 多个工具调用：全部进入 calls 数组（按顺序），tool/args 冗余携带首个供旧读取方使用。
func TestToolCallsToText_KeepsAllCallsInOrder(t *testing.T) {
	out := toolCallsToText([]toolCall{
		mkTC("read", `{"path":"a"}`),
		mkTC("write", `{"path":"b"}`),
		mkTC("bash", `{"command":"ls"}`),
	}, "")
	var got struct {
		Calls []struct {
			Tool string `json:"tool"`
			Args string `json:"args"`
		} `json:"calls"`
		Tool string `json:"tool"`
		Args string `json:"args"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("多工具调用必须输出合法 JSON: %v (out=%q)", err, out)
	}
	// 顺序敏感：模型可能先读后写，不能重排
	wantTools := []string{"read", "write", "bash"}
	if len(got.Calls) != len(wantTools) {
		t.Fatalf("应保留全部 %d 个调用, got %d: %q", len(wantTools), len(got.Calls), out)
	}
	for i, w := range wantTools {
		if got.Calls[i].Tool != w {
			t.Fatalf("第 %d 个调用应为 %q, got %q（顺序不能变）", i, w, got.Calls[i].Tool)
		}
	}
	// 兼容性：单数字段仍携带首个，subagent 等旧读取方不受影响
	if got.Tool != "read" || got.Args != `{"path":"a"}` {
		t.Fatalf("单数字段应为首个调用, got tool=%q args=%q", got.Tool, got.Args)
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

// XML 工具调用必须被识别为工具调用（而不是包成 reply 让任务提前结束）。
// 实测 bug：模型输出 <tool name="read">…</tool> 时，wrapReplyJSON 只认 JSON →
// 包成 reply → 循环当成最终回答 → 任务提前结束且 XML 标签显示给用户。
// 期望：归一化为等价协议 JSON（tool/args 字段可从结果里取出）。
func TestWrapReplyJSON_XMLToolCallRecognized(t *testing.T) {
	xml := `<tool name="read">{"path": "D:\\\\codes\\\\feihe\\\\installer\\\\x.ps1"}</tool>`
	got := wrapReplyJSON(xml)

	var req struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("应输出合法协议 JSON: %v (got=%q)", err, got)
	}
	if req.Action != "tool" || req.Tool != "read" {
		t.Fatalf("应归一化为工具调用 action=tool tool=read, got %+v (raw=%q)", req, got)
	}
	if req.Text != "" {
		t.Fatalf("不应被包成 reply（会让任务提前结束）, got text=%q", req.Text)
	}
}

// XML 的 <reply> 必须被提取为协议 JSON，而不是把标签本身塞进 text。
// 实测 bug：包成 {"action":"reply","text":"<reply>答案</reply>"} → 用户看到标签原文。
func TestWrapReplyJSON_XMLReplyExtracted(t *testing.T) {
	got := wrapReplyJSON(`<reply>最终答案</reply>`)
	var req struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("应输出合法协议 JSON: %v (got=%q)", err, got)
	}
	if req.Action != "reply" || req.Text != "最终答案" {
		t.Fatalf("应提取出 text=最终答案, got %+v (raw=%q)", req, got)
	}
	if strings.Contains(req.Text, "<reply>") {
		t.Fatalf("text 不应含 XML 标签（会显示给用户）: %q", req.Text)
	}
}

// XML 形态的 arg_key/arg_value 也应被归一化为协议 JSON。
func TestWrapReplyJSON_XMLArgKeyValue(t *testing.T) {
	got := wrapReplyJSON(`<tool name="t"><arg_key>p</arg_key><arg_value>v</arg_value></tool>`)
	var req struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
		Args   string `json:"args"`
	}
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("应输出合法协议 JSON: %v (got=%q)", err, got)
	}
	if req.Tool != "t" {
		t.Fatalf("tool 应为 t, got %q", req.Tool)
	}
	if !strings.Contains(req.Args, `"p"`) || !strings.Contains(req.Args, `"v"`) {
		t.Fatalf("args 应含 p/v, got %q", req.Args)
	}
}

// 纯文本回复仍正常包装（不能被 XML 改造影响）。
func TestWrapReplyJSON_PlainTextUnchanged(t *testing.T) {
	got := wrapReplyJSON("就是一句普通回答")
	want := `{"action":"reply","text":"就是一句普通回答"}`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
