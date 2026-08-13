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
	"syscall"
	"time"

	"mizar/internal/plugins"
)

// All 返回全部内置工具。skillsDir 为技能目录（skill_manage 用）。
func All(skillsDir string) []plugins.Tool {
	return []plugins.Tool{
		toolBash(),
		toolGrep(),
		toolFind(),
		toolRead(),
		toolWrite(),
		toolEdit(),
		toolLS(),
		skillManage(skillsDir),
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
			// 设置进程组：超时 kill 时杀掉整个进程树（含子进程），防孤儿化
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
				case <-done:
				case <-time.After(time.Duration(p.Timeout) * time.Second):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					return "", fmt.Errorf("bash: timeout after %ds", p.Timeout)
				}
			} else {
				<-done
			}
			return strings.TrimSpace(out.String()), nil
		},
	}
}
