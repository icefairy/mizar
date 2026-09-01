// Package context 提供 SOUL.md + AGENTS.md 自动读取（全局 + 局部相加）。
//
// 规则（参照 Claude Code / Hermes 的上下文文件约定）：
//   - SOUL.md：~/.mizar/SOUL.md（用户个人化身份声明，stable 层，跨会话稳定）
//   - 全局 AGENTS.md：~/.mizar/AGENTS.md（所有项目共享的全局指令）
//   - 局部文件：从工作目录向上递归查找 AGENTS.md（最近者优先，类似 git
//     向上查找）；也可用 --agents-file 显式指定
//   - 扩展文件：.cursorrules、USER.md（可选，同目录查找）
//   - 注入顺序：SOUL.md → 全局 AGENTS.md → 局部 AGENTS.md → .cursorrules → USER.md
package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/config"
)

// SoulPath 返回 SOUL.md 路径（~/.mizar/SOUL.md）。
func SoulPath() string {
	return filepath.Join(config.ConfigDir(), "SOUL.md")
}

// LoadSoul 读取 ~/.mizar/SOUL.md，不存在返回空字符串。
func LoadSoul() string {
	b, err := os.ReadFile(SoulPath())
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return ""
	}
	return content
}

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

// ContextFiles 返回所有上下文文件路径，按注入优先级排序：
// [全局 AGENTS.md, 局部 AGENTS.md, .cursorrules, USER.md]。
// 文件不存在则跳过。
func ContextFiles(workDir string) []string {
	var files []string
	for _, name := range []string{"AGENTS.md", ".cursorrules", "USER.md"} {
		if local := findUpwards(workDir, name); local != "" {
			files = append(files, local)
		}
	}
	// 全局 AGENTS.md 固定在前（即使局部不存在）
	global := filepath.Join(config.ConfigDir(), "AGENTS.md")
	if fileExists(global) && !contains(files, global) {
		files = append([]string{global}, files...)
	}
	return files
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// Load 读取全局 + 局部 AGENTS.md 并相加渲染（向后兼容）。
// 返回渲染后的提示文本；文件不存在或为空返回空字符串。
func Load(workDir string) string {
	return LoadContextFiles(workDir)
}

// LoadContextFiles 读取所有上下文文件（含全局/局部 AGENTS.md、.cursorrules、USER.md）
// 并相加渲染。SOUL.md 需单独调用 LoadSoul()。
// 返回渲染后的提示文本；无文件时返回空字符串。
func LoadContextFiles(workDir string) string {
	labels := map[string]string{
		"AGENTS.md":   "全局",
		".cursorrules": "项目配置",
		"USER.md":     "用户档案",
	}
	var sb strings.Builder
	// 先放全局 AGENTS.md
	global := filepath.Join(config.ConfigDir(), "AGENTS.md")
	if fileExists(global) {
		appendFile(&sb, global, "全局 AGENTS.md")
	}
	// 再放局部文件（工作目录向上查找）
	for _, name := range []string{"AGENTS.md", ".cursorrules", "USER.md"} {
		if local := findUpwards(workDir, name); local != "" {
			label := labels[name]
			if name == "AGENTS.md" && global == local {
				continue // 全局已处理，跳过重复
			}
			appendFile(&sb, local, label)
		}
	}
	return sb.String()
}

func appendFile(sb *strings.Builder, path, label string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return
	}
	fmt.Fprintf(sb, "\n=== %s (%s) ===\n%s\n", label, path, content)
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
