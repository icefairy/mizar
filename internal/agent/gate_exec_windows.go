//go:build windows

package agent

import "os/exec"

// configureGateCmd Windows 下无进程组语义，保持默认取消行为（TerminateProcess）。
func configureGateCmd(cmd *exec.Cmd) {}
