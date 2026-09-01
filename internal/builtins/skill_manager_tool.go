// Package builtins 实现 skill_manager 工具（复刻 hermes skill_manager_tool）。
//
// 允许 agent 在运行中创建/编辑/删除 skill，把经验变成可复用知识。
package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
)

// ============================================================================
// SkillManager：agent 可管理的技能 CRUD
// ============================================================================

// SkillManager 管理用户创建的 skill 文件。
type SkillManager struct {
	baseDir string // ~/.mizar/skills 或项目 skills/ 目录
}

// NewSkillManager 创建 skill 管理器。
func NewSkillManager(baseDir string) *SkillManager {
	return &SkillManager{baseDir: baseDir}
}

// Tool 返回 skill_manager 工具。
func (sm *SkillManager) Tool() plugins.Tool {
	return plugins.Tool{
		Name:        "skill_manager",
		Description: "Create, edit, patch, or delete agent skills. Skills capture procedural knowledge as SKILL.md files in the skills directory. Actions: create (new skill), edit (full rewrite), patch (find-and-replace), delete (remove), list (show all).",
		Run:         sm.run,
	}
}

func (sm *SkillManager) run(args string) (string, error) {
	var p struct {
		Action  string `json:"action"`
		Name    string `json:"name"`
		Body    string `json:"body,omitempty"`
		Find    string `json:"find,omitempty"`
		Replace string `json:"replace,omitempty"`
	}
	if err := parseArgs(args, &p); err != nil {
		return "", err
	}
	p.Action = strings.TrimSpace(strings.ToLower(p.Action))
	switch p.Action {
	case "create":
		return sm.create(p.Name, p.Body)
	case "edit":
		return sm.edit(p.Name, p.Body)
	case "patch":
		return sm.patch(p.Name, p.Find, p.Replace)
	case "delete":
		return sm.delete(p.Name)
	case "list":
		return sm.list()
	default:
		return "", fmt.Errorf("skill_manager: unknown action %q (expected create/edit/patch/delete/list)", p.Action)
	}
}

func (sm *SkillManager) create(name, body string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill_manager create: name required")
	}
	if body == "" {
		return "", fmt.Errorf("skill_manager create: body required")
	}
	dir := filepath.Join(sm.baseDir, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("skill_manager: skill %q already exists", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("skill_manager create dir: %w", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("skill_manager create file: %w", err)
	}
	return fmt.Sprintf("✓ 已创建技能 %q（%s）", name, path), nil
}

func (sm *SkillManager) edit(name, body string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill_manager edit: name required")
	}
	if body == "" {
		return "", fmt.Errorf("skill_manager edit: body required")
	}
	path := filepath.Join(sm.baseDir, name, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("skill_manager: skill %q not found", name)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("skill_manager edit file: %w", err)
	}
	return fmt.Sprintf("✓ 已更新技能 %q", name), nil
}

func (sm *SkillManager) patch(name, find, replace string) (string, error) {
	name = strings.TrimSpace(name)
	find = strings.TrimSpace(find)
	replace = strings.TrimSpace(replace)
	if name == "" || find == "" {
		return "", fmt.Errorf("skill_manager patch: name and find required")
	}
	path := filepath.Join(sm.baseDir, name, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("skill_manager: skill %q not found: %w", name, err)
	}
	newData := strings.Replace(string(data), find, replace, 1)
	if newData == string(data) {
		return "", fmt.Errorf("skill_manager patch: find text not found in %q", name)
	}
	if err := os.WriteFile(path, []byte(newData), 0o644); err != nil {
		return "", fmt.Errorf("skill_manager patch write: %w", err)
	}
	return fmt.Sprintf("✓ 已修补技能 %q", name), nil
}

func (sm *SkillManager) delete(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill_manager delete: name required")
	}
	path := filepath.Join(sm.baseDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("skill_manager: skill %q not found", name)
	}
	if err := os.RemoveAll(path); err != nil {
		return "", fmt.Errorf("skill_manager delete: %w", err)
	}
	return fmt.Sprintf("✓ 已删除技能 %q", name), nil
}

func (sm *SkillManager) list() (string, error) {
	entries, err := os.ReadDir(sm.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "（技能目录不存在，请先创建）", nil
		}
		return "", fmt.Errorf("skill_manager list: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("已注册技能：\n")
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillPath := filepath.Join(sm.baseDir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		found = true
		data, err := os.ReadFile(skillPath)
		desc := ""
		if err == nil {
			// 提取 frontmatter 中的 description
			lines := strings.Split(string(data), "\n")
			inFM := false
			for _, line := range lines {
				if line == "---" {
					inFM = !inFM
					continue
				}
				if inFM && strings.HasPrefix(line, "description:") {
					desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					break
				}
			}
		}
		sb.WriteString(fmt.Sprintf("  • %s", e.Name()))
		if desc != "" {
			sb.WriteString(fmt.Sprintf(" — %s", desc))
		}
		sb.WriteString("\n")
	}
	if !found {
		sb.WriteString("  （暂无技能）")
	}
	return sb.String(), nil
}

// ============================================================================
// 辅助函数
// ============================================================================

func parseArgs(args string, v interface{}) error {
	args = strings.TrimSpace(args)
	if args == "" || args == "{}" {
		return fmt.Errorf("args required")
	}
	if err := jsonUnmarshalSafe([]byte(args), v); err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	return nil
}

// ============================================================================
// Tool 工厂函数
// ============================================================================

// SkillManagerTool 返回 skill_manager 工具（兼容现有 builtins 风格）。
func SkillManagerTool(baseDir string) plugins.Tool {
	sm := NewSkillManager(baseDir)
	return sm.Tool()
}
