package main

// handoff.go — /handoff 斜杠命令的实现（固化进程序，非插件）。
//
// 用法:
//   /handoff [备注/下一步]
// 效果: 在当前工作目录生成 handoff.md，供新会话无缝续接。
//
// 内容分两层（此前只有第一层，导致「连当前正在做的任务都没写入」）：
//  1. 一手环境证据：git 分支/状态/提交记录
//  2. **会话上下文**：当前任务、最近对话、已执行的工具调用与结果、todo 清单、
//     会话目标、后续步骤备注
//
// 第二层才是交接文档真正有用的部分——没有它，新会话只知道「代码处于什么状态」，
// 不知道「刚才在干什么、试过什么、下一步该做什么」。

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"mizar/internal/agent"
	"mizar/internal/builtins"
)

// handoffHistoryLimit 纳入交接文档的最近消息条数（太长反而淹没重点）。
const handoffHistoryLimit = 40

// handoffMsgCap 单条消息在文档中的最大字符数（工具输出可能极长）。
const handoffMsgCap = 600

// gitOut 跑一条 git 只读命令，失败返回空串（非 git 目录不致命）。
// 参数分开传不经 shell，避免注入。
func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeHandoff 生成 handoff.md，返回用户提示文本。
// a 用于读取会话上下文（当前任务、历史、目标）；可为 nil（降级为仅环境证据）。
func writeHandoff(wd, note string, a *agent.Agent) string {
	now := time.Now()
	ts := now.Format("2006-01-02 15:04:05")

	var sb strings.Builder
	sb.WriteString("# Handoff\n\n")
	fmt.Fprintf(&sb, "- 时间: %s\n", ts)

	// ---------- 1. 会话上下文（最重要，放最前） ----------
	if a != nil {
		writeSessionContext(&sb, a)
	}

	// ---------- 2. 环境证据 ----------
	branch := gitOut(wd, "rev-parse", "--abbrev-ref", "HEAD")
	gitOk := branch != ""
	status := gitOut(wd, "status", "--short")
	log := gitOut(wd, "log", "--oneline", "-10")

	sb.WriteString("## 环境\n\n")
	if gitOk {
		fmt.Fprintf(&sb, "- 分支: `%s`\n", branch)
	} else {
		sb.WriteString("- 分支: (非 git 目录)\n")
	}
	fmt.Fprintf(&sb, "- 工作目录: `%s`\n\n", wd)
	if gitOk {
		sb.WriteString("### 工作区状态 (git status --short)\n\n")
		if status == "" {
			sb.WriteString("(干净)\n")
		} else {
			sb.WriteString("```\n" + status + "\n```\n")
		}
		sb.WriteString("\n### 最近提交 (git log -10)\n\n")
		if log == "" {
			sb.WriteString("(无)\n")
		} else {
			sb.WriteString("```\n" + log + "\n```\n")
		}
		sb.WriteString("\n")
	}

	// ---------- 3. 用户备注 ----------
	if note = strings.TrimSpace(note); note != "" {
		sb.WriteString("## 备注 / 下一步\n\n")
		sb.WriteString(note)
		sb.WriteString("\n\n")
	}

	// ---------- 4. 续接方式 ----------
	sb.WriteString("## 续接方式\n\n")
	sb.WriteString("新会话首条消息: @handoff.md 继续\n")

	path := wd + "/handoff.md"
	md := sb.String()
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "handoff.md 写入失败: " + err.Error()
	}
	suffix := ""
	if note != "" {
		suffix = "，含备注"
	}
	return fmt.Sprintf("已生成 handoff.md (%d 字符)%s。新会话: mizar 启动后首条消息输入 `@handoff.md 继续`",
		len([]rune(md)), suffix)
}

// writeSessionContext 写会话上下文：当前任务、目标、todo、最近对话、工具轨迹。
func writeSessionContext(sb *strings.Builder, a *agent.Agent) {
	sb.WriteString("## 会话上下文\n\n")

	// 当前/最近任务：这是交接文档最关键的一行
	if task := strings.TrimSpace(a.CurrentTask()); task != "" {
		sb.WriteString("### 当前任务\n\n")
		sb.WriteString("> " + oneLine(task, handoffMsgCap) + "\n\n")
	}

	// 会话目标（若有）
	if gs := a.GoalService; gs != nil {
		if g := gs.Get(); g != nil {
			fmt.Fprintf(sb, "### 会话目标\n\n- 目标: %s\n- 状态: %s\n- 轮次: %d\n\n",
				g.Objective, g.Phase, g.RoundsStarted)
		}
	}

	// todo 清单（pi 式）
	if todos := builtins.PiTodoList(); len(todos) > 0 {
		sb.WriteString("### 待办清单\n\n")
		for _, t := range todos {
			mark := "[ ]"
			if t.Done {
				mark = "[x]"
			}
			fmt.Fprintf(sb, "- %s #%d %s\n", mark, t.ID, t.Text)
		}
		sb.WriteString("\n")
	}

	// 最近对话 + 工具轨迹
	hist := a.Initial
	if len(hist) == 0 {
		sb.WriteString("### 最近对话\n\n(无历史)\n\n")
		return
	}
	if len(hist) > handoffHistoryLimit {
		hist = hist[len(hist)-handoffHistoryLimit:]
	}
	sb.WriteString("### 最近对话与工具轨迹\n\n")
	sb.WriteString("（按时间顺序，含工具调用与结果）\n\n")
	for _, m := range hist {
		switch m.Kind {
		case agent.KindToolCall:
			fmt.Fprintf(sb, "- 🔧 **%s** `%s`\n", m.ToolName, oneLine(m.ToolArgs, 200))
		case agent.KindToolResult:
			fmt.Fprintf(sb, "  - ↳ %s\n", oneLine(toolResultSummary(m.Content), handoffMsgCap))
		case agent.KindSummary:
			fmt.Fprintf(sb, "- 📝 (压缩摘要) %s\n", oneLine(m.Content, handoffMsgCap))
		default:
			who := "👤 用户"
			if m.Role == agent.RoleAssistant {
				who = "🤖 助手"
			}
			fmt.Fprintf(sb, "- %s: %s\n", who, oneLine(m.Content, handoffMsgCap))
		}
	}
	sb.WriteString("\n")
}

// toolResultSummary 从「工具结果: {...}」里取出便于阅读的摘要。
func toolResultSummary(content string) string {
	const prefix = "工具结果: "
	s := content
	if strings.HasPrefix(s, prefix) {
		s = s[len(prefix):]
	}
	if out, ok := extractToolOutput(s); ok {
		return out
	}
	return s
}

// oneLine 折叠换行并截断，避免单条消息毁掉文档结构。
func oneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
