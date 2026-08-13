package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mizar/internal/plugins"
)

// skillManage 技能管理工具（参照 piagent skill-evolution 插件）。
//
// 操作：create / edit / patch / delete / list / inspect。
// 只操作 skills 目录，绝不触碰插件目录（extensions/）——插件创建必须经用户
// 明确要求后由 write 工具完成，工具语义上隔离。
func skillManage(skillsDir string) plugins.Tool {
	return plugins.Tool{
		Name: "skill_manage",
		Description: `管理技能（SKILL.md）。operation: create|edit|patch|delete|list|inspect; ` +
			`name: 技能名(小写连字符,≤64字符); description: 创建/编辑时的描述(必须说明用途与何时使用); ` +
			`content: 创建/编辑时的正文(不含frontmatter); find/replace: patch 用(唯一片段,不唯一会拒绝)。` +
			`只操作 skills 目录；创建插件请用 write 工具（需用户明确要求）。`,
		Run: func(args string) (string, error) {
			var p struct {
				Operation   string `json:"operation"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Content     string `json:"content"`
				Find        string `json:"find"`
				Replace     string `json:"replace"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("skill_manage: 参数需为 JSON: %w", err)
			}
			switch p.Operation {
			case "list":
				return skillList(skillsDir)
			case "inspect":
				return skillInspect(skillsDir, p.Name, p.Content)
			case "create", "edit":
				return skillWrite(skillsDir, p.Operation, p.Name, p.Description, p.Content)
			case "patch":
				return skillPatch(skillsDir, p.Name, p.Find, p.Replace)
			case "delete":
				return skillDelete(skillsDir, p.Name)
			}
			return "", fmt.Errorf("skill_manage: 未知操作 %q", p.Operation)
		},
	}
}

var skillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func validSkillName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("技能名需 1-64 字符")
	}
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("技能名只能小写字母/数字/连字符")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return fmt.Errorf("技能名不能以连字符开头/结尾或含连续连字符")
	}
	return nil
}

func skillPath(skillsDir, name string) string {
	return filepath.Join(skillsDir, name, "SKILL.md")
}

func skillList(skillsDir string) (string, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "技能目录不存在: " + skillsDir, nil
		}
		return "", err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := skillPath(skillsDir, e.Name())
		fm, _ := readFrontmatter(p)
		out = append(out, fmt.Sprintf("- **%s**: %s", e.Name(), fm["description"]))
	}
	if len(out) == 0 {
		return "暂无技能", nil
	}
	return strings.Join(out, "\n"), nil
}

func skillInspect(skillsDir, name, section string) (string, error) {
	if err := validSkillName(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(skillPath(skillsDir, name))
	if err != nil {
		return "", fmt.Errorf("技能 %q 不存在", name)
	}
	text := string(b)
	if section != "" && strings.Contains(text, section) {
		return section, nil
	}
	return text, nil
}

func skillWrite(skillsDir, op, name, description, content string) (string, error) {
	if err := validSkillName(name); err != nil {
		return "", err
	}
	if description == "" {
		return "", fmt.Errorf("skill_manage %s: 缺少 description", op)
	}
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := content
	if body == "" {
		body = "# " + name
	}
	md := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, strings.TrimSpace(description), strings.TrimSpace(body))
	if err := os.WriteFile(skillPath(skillsDir, name), []byte(md), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s 技能 %q -> %s", map[string]string{"create": "创建", "edit": "更新"}[op], name, skillPath(skillsDir, name)), nil
}

func skillPatch(skillsDir, name, find, replace string) (string, error) {
	if err := validSkillName(name); err != nil {
		return "", err
	}
	p := skillPath(skillsDir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("技能 %q 不存在", name)
	}
	if find == "" {
		return "", fmt.Errorf("skill_manage patch: 缺少 find")
	}
	text := string(b)
	// 字面量匹配 + 唯一性校验（参照 piagent：歧义拒绝）
	n := strings.Count(text, find)
	if n == 0 {
		return "", fmt.Errorf("patch 失败: %q 未找到（用更短唯一片段）", truncate(find, 60))
	}
	if n > 1 {
		return "", fmt.Errorf("patch 歧义: %q 出现 %d 次，请用更长片段", truncate(find, 60), n)
	}
	text = strings.Replace(text, find, replace, 1)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("已更新技能 %q", name), nil
}

func skillDelete(skillsDir, name string) (string, error) {
	if err := validSkillName(name); err != nil {
		return "", err
	}
	p := skillPath(skillsDir, name)
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("技能 %q 不存在", name)
		}
		return "", err
	}
	os.Remove(filepath.Dir(p)) // 目录空则删除，失败忽略
	return fmt.Sprintf("已删除技能 %q", name), nil
}

// readFrontmatter 读 SKILL.md frontmatter（name/description）。
func readFrontmatter(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm := map[string]string{}
	m := regexp.MustCompile(`^---\n([\s\S]*?)\n---\n`).FindStringSubmatch(string(b))
	if m == nil {
		return fm, nil
	}
	for _, line := range strings.Split(m[1], "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		fm[strings.TrimSpace(line[:idx])] = strings.TrimSpace(line[idx+1:])
	}
	return fm, nil
}
