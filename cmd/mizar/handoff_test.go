package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
)

// 交接文档必须包含「当前正在做的任务」——这是用户明确反馈缺失的信息。
// 仅写 git 信息的 handoff 无法回答「刚才在干什么、下一步做什么」。
func TestHandoffIncludesCurrentTask(t *testing.T) {
	wd := t.TempDir()
	_, a := newTestAgentForReload(t, t.TempDir())
	a.SetCurrentTask("修复 uninstall_tip.ps1 的 API 调用")

	msg := writeHandoff(wd, "下一步：验证注册表清理效果", a)
	if !strings.Contains(msg, "已生成") {
		t.Fatalf("应报告生成成功: %s", msg)
	}

	b, err := os.ReadFile(filepath.Join(wd, "handoff.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	for _, want := range []string{
		"## 会话上下文",
		"### 当前任务",
		"修复 uninstall_tip.ps1 的 API 调用",
		"## 备注 / 下一步",
		"验证注册表清理效果",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("handoff.md 应包含 %q，实际内容:\n%s", want, md)
		}
	}
}

// 会话历史里的工具调用与结果要写进文档（新会话才知道「试过什么」）。
func TestHandoffIncludesToolTrajectory(t *testing.T) {
	wd := t.TempDir()
	_, a := newTestAgentForReload(t, t.TempDir())
	a.SetCurrentTask("查一下 input.dll 的导出")
	a.Initial = []agent.Message{
		{Role: agent.RoleUser, Content: "帮我确认 UninstallLayoutOrTip 是否存在"},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash", ToolArgs: `{"command":"strings input.dll"}`,
			Content: `{"action":"tool","tool":"bash","args":"{\"command\":\"strings input.dll\"}"}`},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, ToolCallID: "c1",
			Content: `工具结果: {"tool_name":"bash","output":"UninstallLayoutOrTip NOT FOUND"}`},
		{Role: agent.RoleAssistant, Content: "结论：该导出不存在。"},
	}
	writeHandoff(wd, "", a)

	b, _ := os.ReadFile(filepath.Join(wd, "handoff.md"))
	md := string(b)
	for _, want := range []string{"### 最近对话与工具轨迹", "bash", "UninstallLayoutOrTip NOT FOUND", "结论：该导出不存在"} {
		if !strings.Contains(md, want) {
			t.Fatalf("文档应含 %q，实际:\n%s", want, md)
		}
	}
}

// a 为 nil（非会话场景）时不能 panic，仍应产出环境信息。
func TestHandoffNilAgentStillWorks(t *testing.T) {
	wd := t.TempDir()
	msg := writeHandoff(wd, "", nil)
	if !strings.Contains(msg, "已生成") {
		t.Fatalf("nil agent 应仍能生成: %s", msg)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "handoff.md"))
	if !strings.Contains(string(b), "## 环境") {
		t.Fatalf("应含环境段:\n%s", string(b))
	}
}

// 超长消息必须折叠成单行并截断，不能破坏文档结构。
func TestHandoffFoldsLongContent(t *testing.T) {
	wd := t.TempDir()
	_, a := newTestAgentForReload(t, t.TempDir())
	long := strings.Repeat("很长的输出行\n", 500)
	a.Initial = []agent.Message{{Role: agent.RoleAssistant, Content: long}}
	writeHandoff(wd, "", a)

	b, _ := os.ReadFile(filepath.Join(wd, "handoff.md"))
	md := string(b)
	// 关键：在文档里这条消息只占 **一行**（折叠掉换行），总长度受 handoffMsgCap 限制。
	// （不能断言“关键词只出现几次”——601 字符里本来就含很多重复词。）
	var msgLines []string
	for _, ln := range strings.Split(md, "\n") {
		if strings.Contains(ln, "很长的输出行") {
			msgLines = append(msgLines, ln)
		}
	}
	if len(msgLines) != 1 {
		t.Fatalf("超长消息应折叠为 1 行，实际占 %d 行", len(msgLines))
	}
	if n := len([]rune(msgLines[0])); n > handoffMsgCap+100 {
		t.Fatalf("折叠后应受限长约束（~%d），实际 %d runes", handoffMsgCap, n)
	}
	// 整份文档不应被单条消息撑爆
	if n := len(strings.Split(md, "\n")); n > 60 {
		t.Fatalf("文档不应被单条消息撑成 %d 行", n)
	}
}
