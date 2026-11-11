//go:build windows

package agent

import (
	"fmt"
	"os/exec"
)

// configureGateCmd Windows 下让门禁命令可整树终止。
//
// 默认 context 取消只 TerminateProcess 直接子进程（PowerShell/cmd），其派生进程
// 仍可能持有输出管道，导致 CombinedOutput 等到自然结束、超时形同虚设。
// 这里改用 taskkill /T /F 杀整棵进程树（等待其中的 cmd.WaitDelay 兜底）。
func configureGateCmd(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	}
}
