// Package builtins 提供 pi 式内置工具（Go 实现，注册为 Agent 可直接调用的工具）。
//
// 与插件导出的 tool_* 同一通道（plugins.Manager），LLM 循环中可直接使用：
//
//	| 工具   | 参数                                            | 说明               |
//	|--------|-------------------------------------------------|--------------------|
//	| bash   | command, timeout(可选)                          | 执行 shell 命令    |
//	| grep   | pattern, path, glob, ignoreCase, literal, context | 内容搜索           |
//	| find   | pattern, path, limit                            | 文件查找（glob）   |
//	| read   | path, offset, limit                             | 读文件             |
//	| write  | path, content                                   | 写文件             |
//	| edit   | path, oldText, newText                          | 精准替换（可多次） |
//	| ls     | path, limit                                     | 列目录             |
//
// 每个工具 Run(args string) 接收 JSON 参数（与插件 tool_* 一致）。
package builtins

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"mizar/internal/plugins"
)

// All 返回全部内置工具。skillsDir 为技能目录（skill_manage / skill 用）。
// onPluginFileChanged 可选回调：write/edit 写入插件目录内文件后触发（插件自动热重载，nil = 不启用）；
// 返回值会追加到工具输出（如“✓ 插件已重载”或编译错误），让模型立即感知插件生效与否。
func All(skillsDir string, onPluginFileChanged func(path string) string) []plugins.Tool {
	return []plugins.Tool{
		toolBash(),
		toolGrep(),
		toolFind(),
		toolRead(),
		toolWrite(onPluginFileChanged),
		toolEdit(onPluginFileChanged),
		toolLS(),
		toolRepoMap(),
		skillManage(skillsDir),
		toolTodoWrite(),
		toolAskUser(),
		toolSkill(skillsDir),
		toolExitPlanMode(),
	}
}

// toolBash 执行 shell 命令（对齐 pi 的 bash 工具：timeout 可选，无默认超时）。
// 需要限制执行时间时，由模型自行传 timeout 或在命令前加 `timeout` 命令。
func toolBash() plugins.Tool {
	return plugins.Tool{
		Name:        "bash",
		Description: "Execute a bash/shell command. Args: {command: string, timeout?: number(seconds)}. Returns combined stdout+stderr. No default timeout; pass timeout if the command may hang, or prefix the command with `timeout <sec>`. Use for building, running tests, git, package managers.",
		Run: func(args string) (string, error) {
			var p struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Command == "" {
				return "", fmt.Errorf("bash: args {command} required")
			}
			cmd := exec.Command("bash", "-c", p.Command)
			// 设置进程组：超时 kill 时杀掉整个进程树（含子进程），防孤儿化（平台抽象）
			setupProcessGroup(cmd)
			var out strings.Builder
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Start(); err != nil {
				return "", err
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			if p.Timeout > 0 {
				select {
				case err := <-done:
					if err != nil {
						return out.String(), err
					}
				case <-time.After(time.Duration(p.Timeout) * time.Second):
					_ = killProcessTree(cmd)
					return out.String(), fmt.Errorf("bash: timeout after %ds", p.Timeout)
				}
			} else {
				// 默认 60s 超时，防止命令无限阻塞
				select {
				case err := <-done:
					if err != nil {
						return out.String(), err
					}
				case <-time.After(60 * time.Second):
					_ = killProcessTree(cmd)
					return out.String(), fmt.Errorf("bash: timeout after 60s (default)")
				}
			}
			full := out.String()
			// 输出截断（对齐 pi truncateHead）：50KB / 2000 行双限，保留头部（bash 输出按时间顺序看头部最有意义）
			truncated, show, truncBy, totalLines, totalBytes := truncateOutput(full)
			if truncated {
				path, werr := dumpToTemp(full)
				if werr != nil {
					return fmt.Sprintf("truncated (%s): showing %d of %d lines / %d of %d bytes (temp dump failed: %v)",
						truncBy, show, totalLines, bashMaxBytes, totalBytes, werr), nil
				}
				warn := fmt.Sprintf("truncated (%s): showing %d of %d lines / %d of %d bytes. Full output: %s",
					truncBy, show, totalLines, bashMaxBytes, totalBytes, path)
				return warn, nil
			}
			return strings.TrimSpace(full), nil
		},
	}
}

// truncateOutput 截断 bash 输出，保留头部（对齐 pi truncateHead）。
// 返回 (truncated, shownLines, reason, totalLines, totalBytes)。
// reason = "lines" | "bytes" | "both"。
func truncateOutput(content string) (bool, int, string, int, int) {
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	totalBytes := len(content)
	if totalLines <= readMaxLines && totalBytes <= bashMaxBytes {
		return false, totalLines, "", totalLines, totalBytes
	}
	var sb strings.Builder
	byteUsed := 0
	shown := 0
	for i, line := range lines {
		lineBytes := len(line) + 1 // +1 for \n
		if byteUsed+lineBytes > bashMaxBytes && i > 0 {
			break
		}
		sb.WriteString(line)
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
		byteUsed += lineBytes
		shown++
		if shown >= readMaxLines {
			break
		}
	}
	var truncBy string
	if shown >= readMaxLines && byteUsed > bashMaxBytes {
		truncBy = "both"
	} else if shown >= readMaxLines {
		truncBy = "lines"
	} else {
		truncBy = "bytes"
	}
	return true, shown, truncBy, totalLines, totalBytes
}
