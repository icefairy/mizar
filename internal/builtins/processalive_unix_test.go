//go:build !windows

package builtins

import (
	"strconv"
	"syscall"
)

// processAlive 判断 pid 进程是否存活（Unix：kill -0 探测）。
func processAlive(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
