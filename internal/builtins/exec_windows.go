//go:build windows

package builtins

import (
	"fmt"
	"os/exec"
)

// killProcessTree 杀掉进程树。Windows 用 taskkill /T /F（/T 杀整个树）。
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// 用独立的 taskkill 进程杀目标（不能杀自己）
	return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
}

// setupProcessGroup Windows 无进程组概念，no-op（taskkill /T 已覆盖树击杀）。
func setupProcessGroup(cmd *exec.Cmd) {}
