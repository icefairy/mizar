//go:build !windows

package builtins

import (
	"os/exec"
	"syscall"
)

// killProcessTree 杀掉进程及其整个进程组（防子进程孤儿化）。
// Unix：通过 Setpgid 建立进程组，kill 负 PID 杀整组。
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// setupProcessGroup 让子进程成为独立进程组组长，超时 kill 可整组清除。
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
