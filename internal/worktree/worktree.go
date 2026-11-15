// Package worktree 提供 git worktree 隔离封装（C-8）。
//
// 与 delegate_task 的 working_dir 配合：把一个子任务放进独立 worktree 里跑，
// 实验性改动不污染主工作区，失败可直接丢弃（worktree remove --force）。
//
// 设计取舍：
//   - 只调 git 官方命令（`git worktree add/list/remove`），不自己维护目录结构；
//     这样 `.git/worktrees/` 元数据始终一致，rm -rf 目录也不会把仓库弄坏。
//   - 全部操作以「仓库根目录」为 -C 参数，避免调用方 cwd 漂移。
//   - 不自动 merge / 不自动清理分支：合并与删除分支是用户的决定，工具只做隔离。
package worktree

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info 一个 worktree 的信息（来自 `git worktree list --porcelain`）。
type Info struct {
	Path     string // 绝对路径
	Head     string // 提交 SHA
	Branch   string // refs/heads/xxx 的短名；detached 时为空
	Bare     bool
	Locked   bool
	Prunable bool
}

// gitBin 返回 git 可执行文件路径（查找失败返回错误，附安装提示）。
func gitBin() (string, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("未找到 git 可执行文件（worktree 隔离依赖 git，请先安装并加入 PATH）")
	}
	return p, nil
}

// runGit 在 dir 下执行 git 命令，返回 stdout（失败时附带 stderr）。
func runGit(dir string, args ...string) (string, error) {
	git, err := gitBin()
	if err != nil {
		return "", err
	}
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command(git, full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if msg == "" {
			msg = err.Error()
		}
		return string(out), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// Root 返回 dir 所属 git 仓库的根目录（工作区根，不是 .git 目录）。
func Root(dir string) (string, error) {
	if dir == "" {
		d, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("取当前目录失败: %w", err)
		}
		dir = d
	}
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s 不是 git 仓库（或 git 不可用）: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// List 列出仓库所有 worktree。repoDir 为空时用当前目录。
func List(repoDir string) ([]Info, error) {
	root, err := Root(repoDir)
	if err != nil {
		return nil, err
	}
	out, err := runGit(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parsePorcelain(out), nil
}

// parsePorcelain 解析 `git worktree list --porcelain` 输出为 []Info。
func parsePorcelain(out string) []Info {
	var res []Info
	var cur Info
	flush := func() {
		if cur.Path != "" {
			res = append(res, cur)
		}
		cur = Info{}
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			cur.Bare = true
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.Locked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			cur.Prunable = true
		}
	}
	flush()
	return res
}

// CreateOptions worktree 创建参数。
type CreateOptions struct {
	// Branch 新分支名（必填）。已存在时返回错误（不自动切换已有分支，避免两处同分支互相干扰）。
	Branch string
	// Path 目标目录；为空时默认放在仓库同级 <repo>.worktrees/<branch>。
	Path string
	// Base 起始提交（默认 HEAD）。
	Base string
}

// Create 创建 worktree 并返回其绝对路径。
func Create(repoDir string, opt CreateOptions) (string, error) {
	branch := strings.TrimSpace(opt.Branch)
	if branch == "" {
		return "", fmt.Errorf("branch 不能为空")
	}
	if strings.ContainsAny(branch, " \t\n") {
		return "", fmt.Errorf("branch 名不能含空白: %q", branch)
	}
	root, err := Root(repoDir)
	if err != nil {
		return "", err
	}
	target := opt.Path
	if target == "" {
		target = filepath.Join(filepath.Dir(root), filepath.Base(root)+".worktrees", branch)
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("目标目录已存在: %s（请换 path 或先 worktree_leave 清理）", target)
	}
	base := strings.TrimSpace(opt.Base)
	if base == "" {
		base = "HEAD"
	}
	// git worktree add -b <branch> <path> <base>
	if _, err := runGit(root, "worktree", "add", "-b", branch, target, base); err != nil {
		return "", err
	}
	return target, nil
}

// Remove 删除 worktree（force=true 时丢弃未提交改动）。不删除分支。
func Remove(repoDir, path string, force bool) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path 不能为空")
	}
	root, err := Root(repoDir)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err = runGit(root, args...)
	return err
}

// Prune 清理已失效的 worktree 元数据（目录被手动删除后的残留）。
func Prune(repoDir string) error {
	root, err := Root(repoDir)
	if err != nil {
		return err
	}
	_, err = runGit(root, "worktree", "prune")
	return err
}

// Find 按路径前缀找 worktree（供 worktree_leave 支持短路径）。
func Find(repoDir, path string) (Info, bool) {
	list, err := List(repoDir)
	if err != nil {
		return Info{}, false
	}
	clean := filepath.Clean(path)
	for _, w := range list {
		if w.Path == clean {
			return w, true
		}
	}
	// 后缀匹配（允许只给目录名）
	for _, w := range list {
		if filepath.Base(w.Path) == path || strings.HasSuffix(w.Path, string(filepath.Separator)+path) {
			return w, true
		}
	}
	return Info{}, false
}
