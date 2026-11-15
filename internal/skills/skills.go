// Package skills 实现 SKILL.md 技能加载：解析 frontmatter，渲染进系统提示。
// 兼容 Anthropic Agent Skills / Hermes / Claude Code 的 SKILL.md 格式。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Skill 一个技能。
type Skill struct {
	Name        string
	Description string
	Body        string // frontmatter 之后的正文
	Source      string // 文件名
	Path        string // 绝对路径（索引注入时供 read 工具加载）
	// Category 分类（来自目录布局 <category>/<skill>/SKILL.md 的父目录名）。
	// 空串表示未分类（顶层 `<skill>/SKILL.md` 或 `<name>.md`）。
	// 对齐 Hermes 的做法：分类即目录，不写进 frontmatter，避免两处真相。
	Category string
	// Related 关联技能名（frontmatter `related_skills`，对齐 hermes learning_graph）。
	// 用于技能图谱连边；引用不存在的技能忽略。
	Related []string
	// CreatedBy 创建者（frontmatter `created_by`，如 agent / user / learn）。
	// 技能图谱用它区分「学来的」与「预置的」。
	CreatedBy string
}

// Render 生成注入 system prompt 的文本。
func (s *Skill) Render() string {
	return fmt.Sprintf("[技能:%s]\n%s", s.Name, s.Body)
}

// RenderIndex 生成索引式注入文本（缓存友好，参照 pi/pi-cache-guardian）：
// 只列出 name/description/路径，正文由 Agent 用 read 工具按需加载。
// 技能文件内容变化不会破坏 System prompt 前缀缓存。
func (s *Skill) RenderIndex() string {
	return fmt.Sprintf("[技能:%s] %s (路径: %s)", s.Name, s.Description, s.Path)
}

// Manager 管理技能目录。
type Manager struct {
	mu     sync.RWMutex
	dir    string
	skills map[string]Skill
}

// NewManager 创建技能管理器。
func NewManager(dir string) *Manager {
	return &Manager{dir: dir, skills: make(map[string]Skill)}
}

// LoadAll 加载技能目录下的技能。
//
// 支持三种布局（可混用）：
//  1. 顶层单文件：`<name>.md`
//  2. 子目录（可携带 references/ 等附属文件）：`<name>/SKILL.md`
//  3. 分类子目录（对齐 Hermes）：`<category>/<name>/SKILL.md`，Category=category
//
// 同名时层级深的优先。跳过 README.md 与不含 SKILL.md 的目录（分类目录本身不是技能）。
func (m *Manager) LoadAll() (loaded []string, failed map[string]error) {
	failed = make(map[string]error)
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		failed["<dir>"] = err
		return nil, failed
	}
	// 收集候选：{相对路径（相对 m.dir）, 完整路径}
	type cand struct {
		rel  string // 相对技能目录的路径，如 "devops/x/SKILL.md"、"handoff/SKILL.md"、"db.md"
		file string // 完整路径
	}
	var cands []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			sub := filepath.Join(m.dir, name)
			// 2. 本层是技能目录？
			if p := findSkillDoc(sub); p != "" {
				rel, _ := filepath.Rel(m.dir, p)
				cands = append(cands, cand{rel: rel, file: p})
				continue
			}
			// 3. 分类目录：扫一层子目录找 SKILL.md（分类目录自身不算技能）
			subs, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			for _, se := range subs {
				if !se.IsDir() {
					continue
				}
				if p := findSkillDoc(filepath.Join(sub, se.Name())); p != "" {
					rel, _ := filepath.Rel(m.dir, p)
					cands = append(cands, cand{rel: rel, file: p})
				}
			}
			continue
		}
		if strings.HasSuffix(name, ".md") && !strings.EqualFold(name, "README.md") {
			cands = append(cands, cand{rel: name, file: filepath.Join(m.dir, name)})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		di := strings.Count(cands[i].rel, string(os.PathSeparator))
		dj := strings.Count(cands[j].rel, string(os.PathSeparator))
		if di != dj {
			return di < dj // 浅层在前，深层在后（深层优先，最后写入覆盖同名）
		}
		return cands[i].rel < cands[j].rel
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cands {
		body, err := os.ReadFile(c.file)
		if err != nil {
			failed[c.rel] = err
			continue
		}
		s, err := parseSkill(c.rel, string(body))
		if err != nil {
			failed[c.rel] = err
			continue
		}
		s.Path = c.file
		s.Category = categoryFromRel(c.rel)
		m.skills[s.Name] = s
		loaded = append(loaded, c.rel)
	}
	return loaded, failed
}

// categoryFromRel 从技能文档相对路径推导分类：
//   - "devops/docker-ops/SKILL.md" → "devops"
//   - "handoff/SKILL.md"           → ""（单层目录=未分类）
//   - "db.md"                      → ""
func categoryFromRel(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 {
		return parts[0]
	}
	return ""
}

// findSkillDoc 在技能子目录里找 SKILL.md（大小写不敏感），返回完整路径（无则空串）。
func findSkillDoc(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(e.Name(), "SKILL.md") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// Get 按名取技能。
func (m *Manager) Get(name string) (Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// All 返回全部技能（按名排序）。
func (m *Manager) All() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Skill, 0, len(m.skills))
	for _, s := range m.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RenderAll 渲染所有技能为提示文本。
func (m *Manager) RenderAll() string {
	all := m.All()
	var sb strings.Builder
	for _, s := range all {
		sb.WriteString(s.Render())
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// RenderIndex 渲染索引式技能列表（缓存友好）：
//   - 只列出 name/description/分类/路径，正文由 read 工具按需加载
//   - 技能内容变化不破坏 System prompt 前缀缓存
//
// 对应 system prompt 中的技能说明段（与 pi 的 skills 注入一致）。
func (m *Manager) RenderIndex() string {
	all := m.All()
	if len(all) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 可用技能\n下面列出的技能提供特定任务的专业指令。任务匹配其描述时，用 read 工具读取路径对应的文件来加载完整指令。\n")
	for _, s := range all {
		sb.WriteString("- ")
		if s.Category != "" {
			sb.WriteString("[" + s.Category + "] ")
		}
		sb.WriteString(s.RenderIndex())
		sb.WriteString("\n")
	}
	return sb.String()
}

// Categories 返回已加载技能的分类清单（去重、排序；不含空分类）。
func (m *Manager) Categories() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := make(map[string]struct{}, 4)
	for _, s := range m.skills {
		if s.Category != "" {
			set[s.Category] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// AllByCategory 返回指定分类下的技能（按名排序）；category 为空则返回未分类技能。
func (m *Manager) AllByCategory(category string) []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Skill, 0, 8)
	for _, s := range m.skills {
		if s.Category == category {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseSkill 解析 SKILL.md：frontmatter（--- 分隔的 YAML 子集）+ 正文。
func parseSkill(source, body string) (Skill, error) {
	s := Skill{Source: source}
	content := strings.TrimPrefix(body, "\uFEFF") // 去 BOM
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		// 找结束的 ---
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return s, fmt.Errorf("unterminated frontmatter in %s", source)
		}
		for _, line := range lines[1:end] {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "{") {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			switch key {
			case "name":
				s.Name = val
			case "description":
				s.Description = val
			case "created_by", "createdBy":
				s.CreatedBy = val
			case "related_skills", "related":
				s.Related = parseRelatedList(val)
			}
		}
		rest := strings.Join(lines[end+1:], "\n")
		s.Body = strings.TrimSpace(rest)
	} else {
		// 无 frontmatter：用第一个 # 标题做 name，第一段做描述
		first := strings.TrimSpace(lines[0])
		if strings.HasPrefix(first, "#") {
			s.Name = strings.TrimSpace(strings.TrimPrefix(first, "#"))
		} else {
			s.Name = defaultSkillName(source)
		}
		s.Body = strings.TrimSpace(content)
		if len(s.Body) > 80 {
			s.Description = s.Body[:80]
		} else {
			s.Description = s.Body
		}
	}
	if s.Name == "" {
		s.Name = defaultSkillName(source)
	}
	if s.Description == "" {
		s.Description = s.Name
	}
	return s, nil
}

// parseRelatedList 解析 related_skills 的几种写法：
//
//	[a, b] / [a,b] / a, b / a
func parseRelatedList(val string) []string {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "[")
	val = strings.TrimSuffix(val, "]")
	var out []string
	for _, part := range strings.Split(val, ",") {
		p := strings.Trim(strings.TrimSpace(part), `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// defaultSkillName 从来源路径推断技能名：
//   - "handoff/SKILL.md" → "handoff"（子目录名）
//   - "db.md"            → "db"
func defaultSkillName(source string) string {
	base := filepath.Base(source)
	if strings.EqualFold(base, "SKILL.md") {
		// 取父目录名
		return filepath.Base(filepath.Dir(source))
	}
	return strings.TrimSuffix(base, ".md")
}
