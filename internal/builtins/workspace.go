package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath 把模型/用户给的路径锚到本次调用的工作目录（Web 会话工作空间）。
//
// cwd 为空 → 原样返回，CLI/TUI/RPC 行为完全不变。
// 相对路径 → filepath.Join(cwd, p)；绝对路径原样清洗后仍需落在 cwd 内。
// 任何清洗后跳出 cwd 的路径（如 ../../etc/passwd）一律拒绝：这是 per-conv
// 工作空间的隔离边界，防止一个会话读到别的会话或系统文件。
func resolvePath(cwd, p string) (string, error) {
	if cwd == "" {
		return p, nil
	}
	if p == "" {
		p = "."
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(cwd, p)
	}
	abs = filepath.Clean(abs)
	base := filepath.Clean(cwd)
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes workspace", p)
	}
	// 工作空间目录可能尚未创建（用户还没上传过文件）。这里顺手建好，
	// 让模型的 ls/read 得到「空目录」而不是 ENOENT，也保证 bash 的 cmd.Dir 可用。
	_ = os.MkdirAll(base, 0o755)
	return abs, nil
}

// dualRun 从一份实现生成 Tool.Run（cwd==""，老行为）与 Tool.RunInDir（带 cwd）。
// 这样内置工具只有一处正体，CLI/TUI 走 Run、Web 走 RunInDir，不会逻辑漂移。
func dualRun(fn func(cwd, args string) (string, error)) (func(args string) (string, error), func(cwd, args string) (string, error)) {
	return func(args string) (string, error) { return fn("", args) },
		func(cwd, args string) (string, error) { return fn(cwd, args) }
}
