package main

// handoff.go — /handoff 斜杠命令的实现（固化进程序，非插件）。
//
// 用法:
//   /handoff [备注/下一步]
// 效果: 在当前工作目录生成 handoff.md，内容全部一手证据
// （git 分支/状态/提交记录）+ 用户备注 + 续接方式。
// 新会话首条消息输入 `@handoff.md 继续` 即可续接。

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

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
func writeHandoff(wd, note string) string {
	now := time.Now()
	ts := now.Format("2006-01-02 15:04:05")

	branch := gitOut(wd, "rev-parse", "--abbrev-ref", "HEAD")
	gitOk := branch != ""
	status := gitOut(wd, "status", "--short")
	log := gitOut(wd, "log", "--oneline", "-10")

	var sb strings.Builder
	sb.WriteString("# Handoff\n\n")
	fmt.Fprintf(&sb, "- 时间: %s\n", ts)
	if gitOk {
		fmt.Fprintf(&sb, "- 分支: `%s`\n", branch)
	} else {
		sb.WriteString("- 分支: (非 git 目录)\n")
	}
	sb.WriteString("\n")
	if gitOk {
		sb.WriteString("## 工作区状态 (git status --short)\n\n")
		if status == "" {
			sb.WriteString("(干净)\n")
		} else {
			sb.WriteString(status)
			sb.WriteString("\n")
		}
		sb.WriteString("\n## 最近提交 (git log -10)\n\n")
		if log == "" {
			sb.WriteString("(无)\n")
		} else {
			sb.WriteString(log)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	if note = strings.TrimSpace(note); note != "" {
		sb.WriteString("## 备注 / 下一步\n\n")
		sb.WriteString(note)
		sb.WriteString("\n\n")
	}
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
	return fmt.Sprintf("已生成 handoff.md (%d 字符)%s。新会话: mizar 启动后首条消息输入 `@handoff.md 继续`", len([]rune(md)), suffix)
}
