package exporter

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
)

func TestToHTMLBasic(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "帮我列一下文件", Timestamp: "2026-11-15T10:00:00.000+08:00"},
		{Role: agent.RoleAssistant, Content: "好的，我执行 ls。"},
	}
	out := ToHTML(msgs, Options{Title: "测试会话", SessionID: "abcdef1234567890", Model: "gpt-x"})
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Error("缺少 doctype")
	}
	if !strings.Contains(out, "测试会话") || !strings.Contains(out, "abcdef12") {
		t.Errorf("标题/会话 id 缺失:\n%.200s", out)
	}
	if !strings.Contains(out, "帮我列一下文件") || !strings.Contains(out, "好的，我执行 ls") {
		t.Error("消息正文缺失")
	}
	if strings.Contains(out, "<script") {
		t.Error("不应含外部脚本（离线自包含）")
	}
}

func TestToHTMLEscapesXSS(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Content: "<script>alert(1)</script> & \"quote\""},
	}
	out := ToHTML(msgs, Options{})
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("未转义 XSS：<script> 原样出现")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("应转义为 &lt;script&gt;")
	}
}

func TestToHTMLToolCallPairing(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash", ToolArgs: `{"command":"ls"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, Content: `工具结果: {"tool_name":"bash","args":"{}","output":"a.txt\nb.txt"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, Content: `工具结果: {"tool_name":"bash","output":"","error":"permission denied"}`},
		{Role: agent.RoleAssistant, Content: "完成"},
	}
	out := ToHTML(msgs, Options{})
	if !strings.Contains(out, "bash") {
		t.Error("工具名缺失")
	}
	if !strings.Contains(out, "a.txt") {
		t.Error("工具输出缺失")
	}
	if !strings.Contains(out, "permission denied") {
		t.Error("工具错误缺失")
	}
	if !strings.Contains(out, "details") {
		t.Error("工具块应折叠")
	}
	// 两个 result 应合并到一个 details（不产生第二个工具块）
	if strings.Count(out, `summary>🔧 bash`) != 1 {
		t.Errorf("工具调用应只渲染一个折叠块，实际 %d", strings.Count(out, "🔧 bash"))
	}
}

func TestToHTMLCodeFence(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleAssistant, Content: "示例：\n```go\nfunc main() { return }\n```\n结束"},
	}
	out := ToHTML(msgs, Options{})
	if !strings.Contains(out, `class="code"`) {
		t.Error("代码块未渲染")
	}
	if !strings.Contains(out, `class="kw"`) {
		t.Error("关键字未高亮")
	}
}

func TestToHTMLSummary(t *testing.T) {
	msgs := []agent.Message{{Role: agent.RoleUser, Kind: agent.KindSummary, Content: "早期对话摘要"}}
	out := ToHTML(msgs, Options{})
	if !strings.Contains(out, "会话摘要") || !strings.Contains(out, "早期对话摘要") {
		t.Error("摘要段缺失")
	}
}

func TestToHTMLEmpty(t *testing.T) {
	out := ToHTML(nil, Options{})
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Error("空会话也应产出合法文档")
	}
}

func TestToHTMLDefaultTimeAndTitle(t *testing.T) {
	opt := Options{}
	out := ToHTML([]agent.Message{{Role: agent.RoleAssistant, Content: "x"}}, opt)
	if !strings.Contains(out, "Session") {
		t.Error("默认标题应为 Session")
	}
	if !strings.Contains(out, "导出:") {
		t.Error("应含导出时间")
	}
}

func TestHighlightDoesNotBreakEscapedHTML(t *testing.T) {
	in := "if a < b then return c"
	out := highlight(in)
	// 原始 < 应被转义为 &lt;，且高亮后仍然只有转义形式
	if strings.Contains(out, "< b") {
		t.Errorf("高亮破坏了 HTML 转义: %s", out)
	}
	if !strings.Contains(out, "&lt;") {
		t.Errorf("< 未转义: %s", out)
	}
}

func TestShortHelpers(t *testing.T) {
	if shortID("abcdefghijkl") != "abcdefgh" {
		t.Error("shortID")
	}
	if shortID("abc") != "abc" {
		t.Error("shortID 短串")
	}
	if shortTime("2026-11-15T10:30:00.000+08:00") != "2026-11-15 10:30" {
		t.Errorf("shortTime=%q", shortTime("2026-11-15T10:30:00.000+08:00"))
	}
}

func TestExportTimeZeroUsesNow(t *testing.T) {
	start := time.Now().Add(-time.Second)
	out := ToHTML(nil, Options{})
	if !strings.Contains(out, time.Now().Format("2006-01-02")) && !strings.Contains(out, start.Format("2006-01-02")) {
		t.Error("应使用当前日期")
	}
}
