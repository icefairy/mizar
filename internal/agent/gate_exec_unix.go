//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// configureGateCmd 让门禁命令成为独立进程组组长。
// 原因：`sh -c "sleep 5"` 只杀 sh 时，子进程 sleep 仍持有管道，
// CombinedOutput 会一直阻塞到子进程自然结束，导致超时/取消形同虚设。
func configureGateCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
