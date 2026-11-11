package main

// shellcmd.go —— /shell 命令：手动切换命令执行器（主要给 Windows 用）。
//
// 背景：`bash` 工具、goal 门禁、`!` 前缀命令、插件 shell_exec 统一走
// internal/shell 包选择执行器。平台默认顺序（Windows: pwsh → powershell → cmd）
// 对大多数人是合适的，但总有例外：
//   - 想固定用 cmd（老脚本只认批处理语法）；
//   - 装了 Git-Bash / WSL，想让模型用 POSIX 语法；
//   - 公司策略把 PowerShell 删了，需要显式指定别的 shell。
//
// 以前只能靠环境变量 MIZAR_SHELL（每次启动都要设），现在提供 /shell：
//   /shell                  查看当前执行器与可选清单
//   /shell <名字|路径>       切换（写入 config.json，重启生效依旧）
//   /shell auto             恢复平台自动探测
// 切换后会立刻用一条探针命令验证，并把结果返回给用户（免得切到一个坏路径）。

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"mizar/internal/agent"
	"mizar/internal/config"
	"mizar/internal/shell"
)

// shellProbeTimeout 切换后探针命令的超时（只是验证 shell 能起来，不需要久等）。
const shellProbeTimeout = 10 * time.Second

// shellProbeCmd 各 shell 的探针命令（输出一个固定串，便于确认执行成功）。
func shellProbeCmd(s shell.Shell) string {
	if s.IsPowerShell() {
		return "Write-Output mizar-shell-ok"
	}
	return "echo mizar-shell-ok"
}

// probeShell 用指定 shell 跑一条最小命令，返回输出与错误。
// 不做任何截断/清理之外的加工，调用方直接展示。
func probeShell(s shell.Shell) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Path, s.CommandArgs(shellProbeCmd(s))...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("探针超时（%s 无响应）", s.Path)
	}
	return strings.TrimSpace(shell.CleanOutput(string(out))), err
}

// renderShellList 渲染当前 shell + 可选清单。
func renderShellList(cur shell.Shell, override string, candidates []shell.Shell, envOverride string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "当前命令执行器: %s（%s）\n", cur.String(), cur.Path)
	switch {
	case override != "":
		fmt.Fprintf(&sb, "选择来源: /shell 手动指定（%s）\n", override)
	case envOverride != "":
		fmt.Fprintf(&sb, "选择来源: 环境变量 MIZAR_SHELL=%s\n", envOverride)
	default:
		sb.WriteString("选择来源: 平台自动探测\n")
	}
	sb.WriteString("提示: " + cur.Hint() + "\n")

	if len(candidates) == 0 {
		sb.WriteString("\n未探测到其它可用 shell。可直接指定路径: /shell C:\\路径\\bash.exe\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "\n本机可用 shell（%d 个，* 为当前）：\n", len(candidates))
	for _, c := range candidates {
		mark := " "
		if c.Path == cur.Path || c.Name == cur.Name {
			mark = "*"
		}
		fmt.Fprintf(&sb, "  %s %-12s %s\n", mark, c.Name, c.Path)
	}
	// 切换示例用本机真实存在的名字，避免在 Unix 上提示 /shell cmd 这类不可用项。
	examples := make([]string, 0, 2)
	for _, c := range candidates {
		if c.Name == cur.Name || len(examples) >= 2 {
			continue
		}
		examples = append(examples, "/shell "+c.Name)
	}
	exampleText := "/shell <名字|路径>"
	if len(examples) > 0 {
		exampleText += "（本机如 " + strings.Join(examples, "、") + "）"
	}
	sb.WriteString("\n切换: " + exampleText + "\n")
	if runtimeIsWindows() {
		sb.WriteString("     路径示例: /shell \"C:\\Program Files\\Git\\bin\\bash.exe\"\n")
	}
	sb.WriteString("恢复自动: /shell auto")
	return sb.String()
}

// newShellCommand 构造 /shell 命令。
//
// persist 把选择写入 config.json（spec 为空 = 恢复自动）；onSwitch 在切换后
// 触发系统提示刷新（「命令行环境」一节要跟着变）。
func newShellCommand(persist func(spec string) error, onSwitch func()) agent.Command {
	return agent.Command{
		Name:        "shell",
		Description: "查看/切换命令执行器（bash 工具与 ! 命令用什么 shell）：/shell | /shell <名字|路径> | /shell auto",
		// 切换后会跑一条探针命令验证 shell 能用，比默认 5s 宽松些。
		Timeout: 30 * time.Second,
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)

			// 无参数：只展示
			if args == "" {
				return renderShellList(shell.Current(), shell.Override(), shell.Candidates(), shell.EnvOverride()), nil
			}

			target := parseShellTarget(args)

			// 恢复自动探测
			if strings.EqualFold(target, "auto") || strings.EqualFold(target, "reset") ||
				strings.EqualFold(target, "default") {
				got, err := shell.SetOverride("")
				if err != nil {
					return "", err
				}
				if persist != nil {
					if err := persist(""); err != nil {
						return "", fmt.Errorf("已恢复自动探测（%s），但写入配置失败: %w", got.String(), err)
					}
				}
				if onSwitch != nil {
					onSwitch()
				}
				return fmt.Sprintf("✓ 已恢复平台自动探测\n当前: %s（%s）\n%s", got.String(), got.Path, got.Hint()), nil
			}

			got, err := shell.SetOverride(target)
			if err != nil {
				// 失败时不动现有选择，并给出可选清单帮助纠正
				return "", fmt.Errorf("%w\n\n%s", err,
					renderShellList(shell.Current(), shell.Override(), shell.Candidates(), shell.EnvOverride()))
			}

			// 探针验证：切换成功但 shell 起不来（路径错/被策略禁用）时立刻告知
			out, perr := probeShell(got)
			probeLine := "✓ 探针通过: " + out
			if perr != nil {
				probeLine = fmt.Sprintf("⚠ 探针失败: %v（输出: %s）— 已切换但命令可能不可用，可 /shell auto 回退", perr, out)
			}

			if persist != nil {
				if err := persist(shell.Override()); err != nil {
					return "", fmt.Errorf("已切换到 %s，但写入配置失败（重启后会丢失）: %w", got.String(), err)
				}
			}
			if onSwitch != nil {
				onSwitch()
			}
			return fmt.Sprintf("✓ 已切换命令执行器: %s（%s）\n%s\n%s", got.String(), got.Path, probeLine, got.Hint()), nil
		},
	}
}

// runtimeIsWindows 当前是否 Windows（示例文案用）。
func runtimeIsWindows() bool { return runtime.GOOS == "windows" }

// parseShellTarget 从命令参数中解析目标 shell。
//
// 两个坑：
//  1. 路径含空格（C:\Program Files\Git\bin\bash.exe）——命令层的字段切分会把它
//     拆成两段，所以含路径分隔符时整串都当目标；
//  2. 用户可能习惯给路径加引号，这里去掉首尾引号。
func parseShellTarget(args string) string {
	t := strings.TrimSpace(args)
	if len(t) >= 2 {
		if (t[0] == '"' && t[len(t)-1] == '"') || (t[0] == '\'' && t[len(t)-1] == '\'') {
			t = t[1 : len(t)-1]
		} else if t[0] == '"' || t[0] == '\'' {
			// 只有开头引号（字段切分会留下分隔符）：去掉开头引号并截到下一个同款引号
			q := t[0]
			rest := t[1:]
			if i := strings.IndexByte(rest, q); i >= 0 {
				t = rest[:i]
			}
		}
	}
	if strings.ContainsAny(t, `/\`) {
		return t
	}
	if f := strings.Fields(t); len(f) > 0 {
		return f[0]
	}
	return t
}

// persistShell 把 shell 选择写入 config.json（spec 为空 = 清除，恢复自动）。
func persistShell(spec string) error {
	path := config.DefaultPath()
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	cfg.Shell = spec
	return config.Save(path, cfg)
}
