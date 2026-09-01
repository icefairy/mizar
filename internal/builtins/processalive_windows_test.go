//go:build windows

package builtins

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// processAlive 判断 pid 进程是否存活（Windows：tasklist /FI 过滤）。
func processAlive(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid)).Output()
	return err == nil && strings.Contains(string(out), fmt.Sprintf("%d", pid))
}
