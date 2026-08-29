package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
	"mizar/internal/skills"
)

// toolSkill 按名加载技能全文（复刻 dsh tool-skill 的渐进披露语义）。
// dsh：目录只注入 name+description，模型通过 skill 工具按名加载全文。
// mizar 现状：索引注入 name/desc/路径，模型用 read 工具读。本工具提供直接一次性返回全文的路径。
func toolSkill(dir string) plugins.Tool {
	return plugins.Tool{
		Name:        "skill",
		Description: "Load the full body of a registered skill by name. Use this when you need the detailed instructions of a skill rather than just its summary. Skills are discovered automatically from skills/ directories and injected into system prompt as catalog entries.",
		Run: func(args string) (string, error) {
			name := strings.TrimSpace(args)
			if name == "" {
				return "", fmt.Errorf("skill: name required")
			}
			// 从技能目录查找：先找精确匹配，再忽略大小写
			file := findSkillFile(dir, name)
			if file == "" {
				return "", fmt.Errorf("skill %q not found in %s (check /reload to refresh)", name, dir)
			}
			body, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				return "", fmt.Errorf("read skill %q: %w", name, err)
			}
			// 解析 frontmatter + 正文（复用 skills.parseSkill）
			s, err := parseSkillInline(file, string(body))
			if err != nil {
				return "", fmt.Errorf("parse skill %q: %w", name, err)
			}
			return fmt.Sprintf("[技能:%s]\n%s", s.Name, s.Body), nil
		},
	}
}

func findSkillFile(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	// 优先精确匹配
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), name+".md") {
			return e.Name()
		}
	}
	// 其次按名称前缀/包含匹配
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".md")
		if strings.EqualFold(base, name) {
			return e.Name()
		}
	}
	return ""
}

// parseSkillInline 复用 skills 包的 frontmatter 解析逻辑（不引入循环依赖，直接复制最小实现）。
func parseSkillInline(source, body string) (skills.Skill, error) {
	var s skills.Skill
	s.Source = source
	idx := strings.Index(body, "\n---\n")
	if idx < 0 {
		idx = strings.Index(body, "\n--\n")
	}
	if idx >= 0 {
		fm := body[:idx]
		s.Body = strings.TrimSpace(body[idx+5:])
		lines := strings.SplitN(fm, "\n", 3)
		if len(lines) >= 2 {
			for _, line := range lines[1:] {
				line = strings.TrimSpace(line)
				colon := strings.Index(line, ":")
				if colon < 0 {
					continue
				}
				k := strings.TrimSpace(line[:colon])
				v := strings.TrimSpace(line[colon+1:])
				switch k {
				case "name":
					s.Name = v
				case "description":
					s.Description = v
				}
			}
		}
	} else {
		s.Body = strings.TrimSpace(body)
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(source, ".md")
	}
	if s.Description == "" {
		s.Description = "Inline skill"
	}
	return s, nil
}
