package agent

import (
	"strings"
	"testing"
)

// 序列化：工具调用应渲染成单行可读形式（pi: `[Assistant tool calls]: read(path="foo.ts")`），
// 而不是把原始 JSON 整段丢给摘要模型。
func TestSerializeToolCallLine(t *testing.T) {
	args := `{"path":"/x/main.go","edits":[{"oldText":"a","newText":"b"}]}`
	got := serializeToolCallLine("edit", args)
	if !strings.HasPrefix(got, "edit(") {
		t.Fatalf("应以工具名开头: %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Fatalf("应含关键参数: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("应为单行: %q", got)
	}
}

func TestSerializeToolCallLineBadJSON(t *testing.T) {
	// 参数不是合法 JSON 时不应 panic，退化为截断原文
	got := serializeToolCallLine("bash", "not json at all")
	if !strings.Contains(got, "bash") {
		t.Fatalf("应保留工具名: %q", got)
	}
}

// 文件追踪：从工具调用中提取读/写文件路径，累计进摘要的 <read-files>/<modified-files>。
func TestExtractFileOps(t *testing.T) {
	reads, mods := extractFileOps("read", `{"path":"/a/one.go"}`)
	if len(reads) != 1 || reads[0] != "/a/one.go" {
		t.Fatalf("read 应记为已读: %v", reads)
	}
	if len(mods) != 0 {
		t.Fatalf("read 不应记为已改: %v", mods)
	}

	for _, tool := range []string{"write", "edit"} {
		_, mods := extractFileOps(tool, `{"path":"/b/two.go"}`)
		if len(mods) != 1 || mods[0] != "/b/two.go" {
			t.Fatalf("%s 应记为已改: %v", tool, mods)
		}
	}
}

func TestExtractFileOpsIgnoresNonFileTools(t *testing.T) {
	reads, mods := extractFileOps("bash", `{"command":"ls"}`)
	if len(reads) != 0 || len(mods) != 0 {
		t.Fatalf("bash 不应产生文件追踪: %v %v", reads, mods)
	}
}

// 序列化整段对话：工具调用以单行摘要出现，工具结果截断，user/assistant 正文保留。
func TestSerializeConversation(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "改一下 main.go"},
		{Role: RoleAssistant, Kind: KindToolCall, ToolName: "edit", ToolArgs: `{"path":"/x/main.go"}`,
			Content: `{"action":"tool","tool":"edit","args":"{\"path\":\"/x/main.go\"}"}`},
		{Role: RoleUser, Kind: KindToolResult, Content: "工具结果: " + strings.Repeat("z", 5000)},
		{Role: RoleAssistant, Content: "改好了"},
	}
	out := serializeConversation(msgs)
	if !strings.Contains(out, "[User]") || !strings.Contains(out, "[Assistant]") {
		t.Fatalf("应带角色标记:\n%s", out)
	}
	if !strings.Contains(out, "edit(") {
		t.Fatalf("工具调用应为单行形式:\n%s", out)
	}
	if !strings.Contains(out, "[Tool result]") {
		t.Fatalf("工具结果应有标记:\n%s", out)
	}
	// 工具结果应被截断（5000 个 z 不应原样出现）
	if strings.Count(out, "z") > 3000 {
		t.Fatalf("工具结果未截断，长度=%d", len(out))
	}
}

// 文件追踪应累计：多个工具调用涉及的路径都要出现，且去重。
func TestCollectFileTracking(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Kind: KindToolCall, ToolName: "read", ToolArgs: `{"path":"/a.go"}`},
		{Role: RoleAssistant, Kind: KindToolCall, ToolName: "read", ToolArgs: `{"path":"/a.go"}`},
		{Role: RoleAssistant, Kind: KindToolCall, ToolName: "edit", ToolArgs: `{"path":"/b.go"}`},
		{Role: RoleAssistant, Kind: KindToolCall, ToolName: "write", ToolArgs: `{"path":"/c.go"}`},
	}
	reads, mods := collectFileTracking(msgs)
	if len(reads) != 1 || reads[0] != "/a.go" {
		t.Fatalf("已读应去重: %v", reads)
	}
	if len(mods) != 2 {
		t.Fatalf("已改应有 2 个: %v", mods)
	}
}

// 文件清单渲染成 pi 风格的 XML 段；无内容时不产生空段。
func TestRenderFileTracking(t *testing.T) {
	out := renderFileTracking([]string{"/a.go"}, []string{"/b.go"})
	if !strings.Contains(out, "<read-files>") || !strings.Contains(out, "<modified-files>") {
		t.Fatalf("应含两个段:\n%s", out)
	}
	if empty := renderFileTracking(nil, nil); empty != "" {
		t.Fatalf("无文件时应为空，得到 %q", empty)
	}
}

// SummaryPrompt 必须包含 Constraints & Preferences 节（pi 的格式要求）。
func TestSummaryPromptHasConstraintsSection(t *testing.T) {
	if !strings.Contains(SummaryPrompt, "Constraints & Preferences") {
		t.Fatal("SummaryPrompt 缺少 ## Constraints & Preferences 节")
	}
	if !strings.Contains(SummaryPrompt, "## Goal") {
		t.Fatal("SummaryPrompt 缺少 ## Goal 节")
	}
}
