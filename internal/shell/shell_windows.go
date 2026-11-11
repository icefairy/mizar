//go:build windows

package shell

import (
	"os"
	"os/exec"
)

// envShellSpec 读取 MIZAR_SHELL 环境变量（未设置返回空）。
func envShellSpec() string { return os.Getenv("MIZAR_SHELL") }

// candidates Windows：按推荐顺序返回已安装的 shell。
//
// 为什么优先 PowerShell 而不是 cmd：
//   - cmd.exe 只支持极简的批处理语法（管道/重定向/条件），没有变量表达式、
//     没有对象管道、错误处理弱；模型写出的稍复杂的命令（多步、循环、
//     JSON 处理、Unicode 输出编码）在 cmd 下几乎必然失败。
//   - PowerShell 语义完整（管道/对象/正则/-ErrorAction），Windows 10+ 默认自带
//     5.1，模型对 PowerShell 语法的先验知识也更充分，兼容性明显更好。
//   - cmd.exe 保留为最后兜底（极老系统 / PowerShell 被策略移除时）。
func candidates() []Shell {
	var out []Shell
	seen := map[string]bool{}
	add := func(name string) {
		if p, err := exec.LookPath(name); err == nil {
			key := baseName(p)
			if !seen[key] {
				seen[key] = true
				out = append(out, postProcess(withArgs(p, nameOf(p))))
			}
		}
	}
	// PowerShell 7（跨平台版）优先：更快、语法更现代
	add("pwsh.exe")
	add("powershell.exe")
	add("cmd.exe")
	// 若用户装了 WSL / Git-Bash，也列出来供 /shell 手动切换
	add("bash.exe")
	return out
}

// psPrelude 统一 PowerShell 的管道输出编码为 UTF-8，避免中文/Unicode 输出乱码
// （powershell.exe 默认按控制台代码页编码，重定向到管道后常出现 � 或乱码）。
const psPrelude = "$ProgressPreference='SilentlyContinue';" +
	"$ErrorActionPreference='Continue';" +
	"try{[Console]::OutputEncoding=[System.Text.Encoding]::UTF8}catch{};" +
	"$PSDefaultParameterValues['Out-File:Encoding']='utf8';"

// psPostfix 把最后一个原生程序的退出码回传为 PowerShell 进程退出码。
//
// 为什么需要：PowerShell 不自动把原生程序（git/go/npm…）的退出码作为自身退出码，
// 直接 -Command "git status" 失败时 powershell.exe 仍返回 0，导致 AI 误判命令成功。
// 若脚本自身调用了 exit N，本语句不可达，行为不变。
const psPostfix = "if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }"

// postProcess 给 PowerShell / cmd 注入输出编码前导（统一 UTF-8，减少中文乱码）。
func postProcess(s Shell) Shell {
	switch s.Name {
	case "pwsh", "PowerShell":
		s.Prelude = psPrelude
		s.Postfix = psPostfix
	case "cmd":
		s.Prelude = "chcp 65001>nul & "
	}
	return s
}

// fallback 兜底：cmd.exe 是任何 Windows 都有的。
func fallback() Shell {
	return postProcess(Shell{Path: "cmd.exe", Args: []string{"/D", "/C"}, Name: "cmd"})
}
