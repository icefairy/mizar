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

// LoadAll 加载目录下所有 .md 技能文件（跳过 README.md）。
func (m *Manager) LoadAll() (loaded []string, failed map[string]error) {
	failed = make(map[string]error)
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		failed["<dir>"] = err
		return nil, failed
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") && !strings.EqualFold(name, "README.md") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join(m.dir, f))
		if err != nil {
			failed[f] = err
			continue
		}
		s, err := parseSkill(f, string(body))
		if err != nil {
			failed[f] = err
			continue
		}
		s.Path = filepath.Join(m.dir, f)
		m.skills[s.Name] = s
		loaded = append(loaded, f)
	}
	return loaded, failed
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
//   - 只列出 name/description/路径，正文由 read 工具按需加载
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
		sb.WriteString(s.RenderIndex())
		sb.WriteString("\n")
	}
	return sb.String()
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
			s.Name = strings.TrimSuffix(filepath.Base(source), ".md")
		}
		s.Body = strings.TrimSpace(content)
		if len(s.Body) > 80 {
			s.Description = s.Body[:80]
		} else {
			s.Description = s.Body
		}
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(source), ".md")
	}
	if s.Description == "" {
		s.Description = s.Name
	}
	return s, nil
}
