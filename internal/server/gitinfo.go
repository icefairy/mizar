package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitInfo 工作目录的版本控制状态（供 Web UI 状态栏展示，对齐 TUI 项目行）。
type gitInfo struct {
	IsGit  bool   `json:"is_git"`
	Branch string `json:"branch,omitempty"` // detached 时为短 SHA
	Dirty  int    `json:"dirty,omitempty"`  // 未提交改动条数
}

// collectGitInfo 采集工作目录的 git 状态。非仓库或 git 不可用时返回零值。
// 注意：每次调用会 fork git 进程，调用方应做节流/缓存（状态栏轮询间隔 ≥1.5s 可接受）。
func collectGitInfo(dir string) gitInfo {
	var g gitInfo
	if dir == "" || !isDir(dir) {
		return g
	}
	out, err := runGitIn(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return g // 非 git 仓库 / 无 git
	}
	g.IsGit = true
	branch := strings.TrimSpace(out)
	if branch == "HEAD" {
		if sha, err2 := runGitIn(dir, "rev-parse", "--short", "HEAD"); err2 == nil {
			branch = strings.TrimSpace(sha)
		}
	}
	g.Branch = branch
	if st, err := runGitIn(dir, "status", "--porcelain"); err == nil {
		if st = strings.TrimSpace(st); st != "" {
			g.Dirty = len(strings.Split(st, "\n"))
		}
	}
	return g
}

// shortDirPath 只保留路径末两级（对齐 TUI 项目行，避免长路径挤满一行）。
func shortDirPath(p string) string {
	trimmed := strings.TrimRight(p, "/\\")
	if trimmed == "" {
		return p
	}
	parent := filepath.Base(filepath.Dir(trimmed))
	base := filepath.Base(trimmed)
	if parent == "." || parent == base || parent == string(filepath.Separator) {
		return base
	}
	return parent + "/" + base
}

func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
