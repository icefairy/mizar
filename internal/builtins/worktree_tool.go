// worktree 隔离工具（C-8）：为实验性改动创建独立 git worktree，不污染主工作区。
package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/plugins"
	"mizar/internal/worktree"
)

// WorktreeTools 返回 worktree_create / worktree_leave / worktree_list 三个工具。
func WorktreeTools(repoDir string) []plugins.Tool {
	if repoDir == "" {
		return nil
	}
	return []plugins.Tool{
		worktreeCreateTool(repoDir),
		worktreeLeaveTool(repoDir),
		worktreeListTool(repoDir),
	}
}

func worktreeCreateTool(repoDir string) plugins.Tool {
	return plugins.Tool{
		Name: "worktree_create",
		Description: "在独立 git worktree 中隔离实验性改动（不污染主工作区）。何时用：需要大改/重构/试错、" +
			"希望失败可整体丢弃时。创建后把返回路径作为 delegate_task 的 working_dir 在该目录里干活。" +
			"参数: {branch: string 新分支名, path?: string 目标目录(默认 <repo>.worktrees/<branch>), base?: string 起始提交(默认 HEAD)}",
		Run: func(args string) (string, error) {
			var p struct {
				Branch string `json:"branch"`
				Path   string `json:"path"`
				Base   string `json:"base"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("worktree_create: 参数解析失败: %w", err)
			}
			target, err := worktree.Create(repoDir, worktree.CreateOptions{
				Branch: p.Branch, Path: p.Path, Base: p.Base,
			})
			if err != nil {
				return "", fmt.Errorf("worktree_create: %w", err)
			}
			return fmt.Sprintf("✓ 已创建隔离 worktree\n  分支: %s\n  路径: %s\n\n"+
				"后续在该目录操作（如传 delegate_task 的 working_dir=%s）。\n"+
				"改动满意后自行 git merge；不要时 worktree_leave path=%s force=true 丢弃。",
				p.Branch, target, target, target), nil
		},
	}
}

func worktreeLeaveTool(repoDir string) plugins.Tool {
	return plugins.Tool{
		Name: "worktree_leave",
		Description: "删除一个 git worktree（离开隔离环境）。注意：只删 worktree 目录与元数据，不删分支、" +
			"不做 merge。有未提交改动时需 force=true 才丢弃。参数: {path: string worktree 路径(可只给目录名), force?: bool}",
		Run: func(args string) (string, error) {
			var p struct {
				Path  string `json:"path"`
				Force bool   `json:"force"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("worktree_leave: 参数解析失败: %w", err)
			}
			if strings.TrimSpace(p.Path) == "" {
				return "", fmt.Errorf("worktree_leave: 需要 path")
			}
			// 解析成绝对路径（允许只给目录名）
			target := p.Path
			if w, ok := worktree.Find(repoDir, p.Path); ok {
				target = w.Path
			}
			if err := worktree.Remove(repoDir, target, p.Force); err != nil {
				// 有未提交改动时 git 会拒绝，给出 force 提示
				if !p.Force {
					return "", fmt.Errorf("%w\n（若确认丢弃未提交改动，加 force=true）", err)
				}
				return "", fmt.Errorf("worktree_leave: %w", err)
			}
			return fmt.Sprintf("✓ 已删除 worktree: %s（分支保留，如需删除请 git branch -d）", target), nil
		},
	}
}

func worktreeListTool(repoDir string) plugins.Tool {
	return plugins.Tool{
		Name:        "worktree_list",
		Description: "列出当前仓库的所有 git worktree（含主工作区与隔离环境）。参数: {}",
		Run: func(args string) (string, error) {
			list, err := worktree.List(repoDir)
			if err != nil {
				return "", fmt.Errorf("worktree_list: %w", err)
			}
			if len(list) == 0 {
				return "（无 worktree）", nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "worktree 共 %d 个：\n", len(list))
			for _, w := range list {
				branch := w.Branch
				if branch == "" {
					branch = "(detached)"
				}
				flags := ""
				if w.Locked {
					flags += " [locked]"
				}
				if w.Prunable {
					flags += " [prunable]"
				}
				fmt.Fprintf(&sb, "  %s  →  %s%s\n", branch, w.Path, flags)
			}
			return strings.TrimSpace(sb.String()), nil
		},
	}
}
