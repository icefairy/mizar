//go:build !windows

package builtins

import "mizar/internal/plugins"

// platformTools 返回当前平台的额外内置工具。
// 非 Windows 平台无 Win32 桌面自动化工具。
func platformTools() []plugins.Tool {
	return nil
}
