// Package context 提供 AGENTS.md 自动读取（全局 + 局部相加）。
//
// 规则（参照 Claude Code / Hermes 的 AGENTS.md 约定）：
//   - 全局文件：~/.mizar/AGENTS.md（所有项目共享的全局指令）
//   - 局部文件：从工作目录向上递归查找 AGENTS.md（最近者优先，类似 git
//     向上查找）；也可用 --agents-file 显式指定
//   - 相加方式：全局内容 + 局部内容拼接（全局在前），都注入 System prompt
package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/config"
)

// AGENTSFiles 返回 [全局, 局部] 两个 AGENTS.md 文件路径（不存在的返回空）。
// 全局文件与配置文件同目录：~/.mizar/AGENTS.md。
func AGENTSFiles(workDir string) []string {
	global := filepath.Join(config.ConfigDir(), "AGENTS.md")
	files := []string{}
	if fileExists(global) {
		files = append(files, global)
	}
	if local := findUpwards(workDir, "AGENTS.md"); local != "" {
		files = append(files, local)
	}
	return files
}

// Load 读取全局 + 局部 AGENTS.md 并相加渲染。
// 返回渲染后的提示文本；文件不存在或为空返回空字符串。
func Load(workDir string) string {
	var sb strings.Builder
	for i, f := range AGENTSFiles(workDir) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(b))
		if content == "" {
			continue
		}
		label := "全局"
		if i == 1 {
			label = "项目"
		}
		fmt.Fprintf(&sb, "\n=== %s AGENTS.md (%s) ===\n%s\n", label, f, content)
	}
	return sb.String()
}

// findUpwards 从 dir 开始向上递归查找 filename，返回第一个命中的路径。
func findUpwards(dir, filename string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, filename)
		if fileExists(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
