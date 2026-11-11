//go:build !windows

package caps

import "os"

// isRoot 判断当前进程是否以 root（uid 0）运行。
func isRoot() bool { return os.Geteuid() == 0 }
