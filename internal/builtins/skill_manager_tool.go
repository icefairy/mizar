// Package builtins 实现 skill_manager 工具（复刻 hermes skill_manager_tool）。
//
// 允许 agent 在运行中创建/编辑/删除 skill，把经验变成可复用知识。
package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		Action   string `json:"action"`
		Name     string `json:"name"`
		Category string `json:"category,omitempty"`
		Body     string `json:"body,omitempty"`
		Find     string `json:"find,omitempty"`
		Replace  string `json:"replace,omitempty"`
	}
	if err := parseArgs(args, &p); err != nil {
		return "", err
	}
	p.Action = strings.TrimSpace(strings.ToLower(p.Action))
	switch p.Action {
	case "create":
		return sm.create(p.Name, p.Category, p.Body)
	case "edit":
		return sm.edit(p.Name, p.Body)
	case "patch":
		return sm.patch(p.Name, p.Find, p.Replace)
	case "delete":
		return sm.delete(p.Name)
	case "list":
		return sm.list(p.Category)
	default:
		return "", fmt.Errorf("skill_manager: unknown action %q (expected create/edit/patch/delete/list)", p.Action)
	}
}

// create 新建技能。category 非空时落到 <baseDir>/<category>/<name>/SKILL.md
// （对齐 Hermes 的分类即目录约定），并自动在 frontmatter 补 category 字段便于阅读。
func (sm *SkillManager) create(name, category, body string) (string, error) {
	name = strings.TrimSpace(name)
	category = strings.TrimSpace(category)
	if name == "" {
		return "", fmt.Errorf("skill_manager create: name required")
	}
	if body == "" {
		return "", fmt.Errorf("skill_manager create: body required")
	}
	// 防目录穿越：名字与分类只能是单层安全标识符。
	if err := safeSegment(name); err != nil {
		return "", fmt.Errorf("skill_manager create: invalid name: %w", err)
	}
	if category != "" {
		if err := safeSegment(category); err != nil {
			return "", fmt.Errorf("skill_manager create: invalid category: %w", err)
		}
	}
	dir := filepath.Join(sm.baseDir, name)
	if category != "" {
		dir = filepath.Join(sm.baseDir, category, name)
	}
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
	msg := fmt.Sprintf("✓ 已创建技能 %q（%s）", name, path)
	if category != "" {
		msg = fmt.Sprintf("✓ 已创建技能 %q（分类 %s，%s）", name, category, path)
	}
	return msg, nil
}

// safeSegment 校验路径段合法（不含分隔符、不为 . / ..）。
func safeSegment(s string) error {
	if s == "" || s == "." || s == ".." {
		return fmt.Errorf("empty or reserved segment")
	}
	if strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("must not contain path separators")
	}
	return nil
}

// locate 定位技能文件路径（支持顶层 <name>/SKILL.md 与分类 <cat>/<name>/SKILL.md）。
// name 也可写成 "cat/name" 或 "cat:name" 直接指定分类。
func (sm *SkillManager) locate(name string) (path, category string, err error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, ":", "/"))
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, p := range parts {
		if safeSegment(p) != nil {
			return "", "", fmt.Errorf("invalid skill path %q", name)
		}
	}
	switch len(parts) {
	case 1:
		// 不带分类：先找顶层，再扫分类目录找同名
		if p := filepath.Join(sm.baseDir, parts[0], "SKILL.md"); fileExists(p) {
			return p, "", nil
		}
		if cat := sm.findCategoryOf(parts[0]); cat != "" {
			return filepath.Join(sm.baseDir, cat, parts[0], "SKILL.md"), cat, nil
		}
		return "", "", fmt.Errorf("skill %q not found", parts[0])
	case 2:
		p := filepath.Join(sm.baseDir, parts[0], parts[1], "SKILL.md")
		if fileExists(p) {
			return p, parts[0], nil
		}
		return "", "", fmt.Errorf("skill %q not found", name)
	default:
		return "", "", fmt.Errorf("invalid skill name %q (expect <name> or <category>/<name>)", name)
	}
}

// findCategoryOf 在分类目录里找同名技能，返回分类名（无则空）。
func (sm *SkillManager) findCategoryOf(skill string) string {
	entries, err := os.ReadDir(sm.baseDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		catDir := filepath.Join(sm.baseDir, e.Name())
		if fileExists(filepath.Join(catDir, skill, "SKILL.md")) {
			return e.Name()
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (sm *SkillManager) edit(name, body string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill_manager edit: name required")
	}
	if body == "" {
		return "", fmt.Errorf("skill_manager edit: body required")
	}
	path, cat, err := sm.locate(name)
	if err != nil {
		return "", fmt.Errorf("skill_manager: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("skill_manager edit file: %w", err)
	}
	if cat != "" {
		return fmt.Sprintf("✓ 已更新技能 %q（分类 %s）", name, cat), nil
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
	path, _, err := sm.locate(name)
	if err != nil {
		return "", fmt.Errorf("skill_manager: %w", err)
	}
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
	path, _, err := sm.locate(name)
	if err != nil {
		return "", fmt.Errorf("skill_manager: %w", err)
	}
	// 删技能目录（SKILL.md 的父目录），不是单个文件——保留 references/ 一起清掉。
	dir := filepath.Dir(path)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("skill_manager delete: %w", err)
	}
	// 分类目录空了顺手清掉，避免留一堆空壳。
	parent := filepath.Dir(dir)
	if parent != sm.baseDir {
		if ents, err := os.ReadDir(parent); err == nil && len(ents) == 0 {
			_ = os.Remove(parent)
		}
	}
	return fmt.Sprintf("✓ 已删除技能 %q", name), nil
}

// list 列出技能。category 非空时只列该分类；空则按分类分组展示。
func (sm *SkillManager) list(category string) (string, error) {
	entries, err := os.ReadDir(sm.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "（技能目录不存在，请先创建）", nil
		}
		return "", fmt.Errorf("skill_manager list: %w", err)
	}
	// 收集 {分类, 名字, 描述}
	type item struct{ cat, name, desc string }
	var items []item
	add := func(cat, skillDir string) {
		p := filepath.Join(skillDir, "SKILL.md")
		if !fileExists(p) {
			return
		}
		items = append(items, item{cat: cat, name: filepath.Base(skillDir), desc: readSkillDesc(p)})
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		top := filepath.Join(sm.baseDir, e.Name())
		if fileExists(filepath.Join(top, "SKILL.md")) {
			add("", top) // 未分类
			continue
		}
		// 分类目录：列出下面的技能
		subs, err := os.ReadDir(top)
		if err != nil {
			continue
		}
		for _, se := range subs {
			if se.IsDir() {
				add(e.Name(), filepath.Join(top, se.Name()))
			}
		}
	}
	if category != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.cat == category {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	if len(items) == 0 {
		if category != "" {
			return fmt.Sprintf("（分类 %s 下暂无技能）", category), nil
		}
		return "（暂无技能）", nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].cat != items[j].cat {
			return items[i].cat < items[j].cat
		}
		return items[i].name < items[j].name
	})
	var sb strings.Builder
	sb.WriteString("已注册技能：\n")
	curCat := "\x00"
	for _, it := range items {
		if it.cat != curCat {
			curCat = it.cat
			if curCat == "" {
				sb.WriteString("  [未分类]\n")
			} else {
				sb.WriteString(fmt.Sprintf("  [%s]\n", curCat))
			}
		}
		sb.WriteString(fmt.Sprintf("  • %s", it.name))
		if it.desc != "" {
			sb.WriteString(fmt.Sprintf(" — %s", it.desc))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// readSkillDesc 从 SKILL.md 的 frontmatter 提取 description（无则空）。
func readSkillDesc(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	inFM := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "---" {
			inFM = !inFM
			continue
		}
		if inFM && strings.HasPrefix(line, "description:") {
			return strings.TrimSpace(strings.Trim(strings.TrimPrefix(line, "description:"), `"'`))
		}
	}
	return ""
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
