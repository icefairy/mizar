//go:build windows

package caps

// isRoot Windows 没有 POSIX uid 概念（管理员权限由 token 判定），
// 这里统一返回 false，避免为探测引入额外的 syscall 依赖。
func isRoot() bool { return false }
