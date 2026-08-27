// Package prompts 实现 Pi 式 Prompt Templates：Markdown 片段 + 参数展开。
//
// 用法：~/.mizar/prompts/<name>.md → /<name> 触发展开。
// 参数语法：$1 $2 ... ${1:-default} ${@:-default} ${@:N} ${@:N:L}
package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Template 一个 prompt 模板。
type Template struct {
	Name        string // 不含 .md 后缀
	Description string
	Content     string
	ArgHint     string // 来自 frontmatter argument-hint
}

// Registry 模板注册表（从目录扫描）。
type Registry struct {
	dirs []string // 模板目录列表（优先全局，其次项目局部）
	tpls map[string]*Template
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tpls: make(map[string]*Template)}
}

// AddDir 添加一个扫描目录（可多次调用，先加的优先）。
func (r *Registry) AddDir(dir string) {
	for _, d := range r.dirs {
		if d == dir {
			return
		}
	}
	r.dirs = append(r.dirs, dir)
}

// Reload 重新扫描所有已注册目录（热加载用）。
func (r *Registry) Reload() {
	r.tpls = make(map[string]*Template)
	for _, dir := range r.dirs {
		r.scanDir(dir)
	}
}

func (r *Registry) scanDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(b)
		desc, argHint := parseFrontmatter(content)
		if desc == "" {
			// 取第一个非空行
			for _, line := range strings.SplitN(content, "\n", 3) {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "---") {
					desc = line
					break
				}
			}
		}
		body := stripFrontmatter(content)
		if body == "" {
			body = content
		}
		r.tpls[name] = &Template{Name: name, Description: desc, Content: body, ArgHint: argHint}
	}
}

// parseFrontmatter 解析 frontmatter 段落，提取 description 和 argument-hint。
var fmRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---`)

// Parse description:argument 行
var kvRe = regexp.MustCompile(`(?m)^\s*(description|argument-hint):\s*(.+)$`)

func parseFrontmatter(content string) (desc, argHint string) {
	m := fmRe.FindStringSubmatch(content)
	if m == nil {
		return "", ""
	}
	fm := m[1]
	for _, sub := range kvRe.FindAllStringSubmatch(fm, -1) {
		key := sub[1]
		val := strings.TrimSpace(sub[2])
		if key == "description" {
			desc = val
		} else if key == "argument-hint" {
			argHint = val
		}
	}
	return
}

func stripFrontmatter(content string) string {
	m := fmRe.FindStringIndex(content)
	if m == nil {
		return content
	}
	// m[0] = full match start, m[1] = full match end
	// strip the frontmatter block including trailing newline
	body := strings.TrimSpace(content[m[1]:])
	return body
}

// Get 按名查找模板。
func (r *Registry) Get(name string) (*Template, bool) {
	t, ok := r.tpls[name]
	if ok {
		return t, true
	}
	// 前缀匹配（如 /review-pr 匹配 review）
	for n, t := range r.tpls {
		if strings.HasPrefix(n, name) {
			return t, true
		}
	}
	return nil, false
}

// List 列出所有模板名。
func (r *Registry) List() []string {
	out := make([]string, 0, len(r.tpls))
	for n := range r.tpls {
		out = append(out, n)
	}
	return out
}

// Expand 展开模板，替换参数（$1 $2 ${1:-default} ${@:-default} ${@:N} ${@:N:L}）。
func (r *Registry) Expand(name string, args string) (string, error) {
	tpl, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("未知模板 /%s（输入 /templates 查看所有模板）", name)
	}
	body := tpl.Content
	// 始终展开参数（即使 args 为空，${N:-default} 也需要展开默认值）
	body = expandVars(body, splitArgs(args))
	return body, nil
}

// splitArgs 按空格/引号分割参数。
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else {
				cur.WriteByte(c)
			}
		} else {
			switch c {
			case '"', '\'':
				inQuote = true
				quoteChar = c
			case ' ', '\t', '\n', '\r':
				if cur.Len() > 0 {
					out = append(out, cur.String())
					cur.Reset()
				}
			default:
				cur.WriteByte(c)
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// expandVars 展开 $N / ${N} / ${N:-default} / $@ / ${@} / ${@:-default} / ${@:N} / ${@:N:L} 语法。
func expandVars(body string, parts []string) string {
	// 1. ${@:N:L} — 切片
	reAtSlice := regexp.MustCompile(`\$\{@\:([0-9]+)\:([0-9]+)\}`)
	body = reAtSlice.ReplaceAllStringFunc(body, func(m string) string {
		sub := reAtSlice.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[1])
		l, _ := strconv.Atoi(sub[2])
		start := n - 1
		if start < 0 { start = 0 }
		end := start + l
		if end > len(parts) { end = len(parts) }
		if end <= start { return "" }
		return strings.Join(parts[start:end], " ")
	})
	// 2. ${@:N} — 从 N 起所有
	reAtStart := regexp.MustCompile(`\$\{@\:([0-9]+)\}`)
	body = reAtStart.ReplaceAllStringFunc(body, func(m string) string {
		sub := reAtStart.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[1])
		start := n - 1
		if start < 0 { start = 0 }
		if start >= len(parts) { return "" }
		return strings.Join(parts[start:], " ")
	})
	// 3. ${@:-default} — 所有参数 join，空则用默认值
	reAtDefault := regexp.MustCompile(`\$\{@(?::-([^}]*))?\}`)
	body = reAtDefault.ReplaceAllStringFunc(body, func(m string) string {
		sub := reAtDefault.FindStringSubmatch(m)
		defaultVal := sub[1]
		if len(parts) == 0 {
			if defaultVal != "" { return strings.TrimPrefix(defaultVal, "-") }
			return ""
		}
		result := strings.Join(parts, " ")
		if result == "" && defaultVal != "" {
			return strings.TrimPrefix(defaultVal, "-")
		}
		return result
	})
	// 3b. $@ 裸形式（无花括号）— 所有参数 join，捕获后跟字符以便保留
	reAtPlain := regexp.MustCompile(`\$@([^0-9A-Za-z_]|$)`)
	body = reAtPlain.ReplaceAllStringFunc(body, func(m string) string {
		if len(m) > 2 {
			return strings.Join(parts, " ") + m[2:]
		}
		return strings.Join(parts, " ")
	})
	// 4. ${N:-default} — 带默认值的单参数
	reBraced := regexp.MustCompile(`\$\{([0-9]+)(?::-([^}]*))?\}`)
	body = reBraced.ReplaceAllStringFunc(body, func(m string) string {
		sub := reBraced.FindStringSubmatch(m)
		idx, _ := strconv.Atoi(sub[1])
		defaultVal := sub[2]
		if idx >= 1 && idx <= len(parts) {
			val := parts[idx-1]
			if val != "" { return val }
		}
		if defaultVal != "" {
			return strings.TrimPrefix(defaultVal, "-")
		}
		return ""
	})
	// 5. $N — 简单位置参数
	reSimple := regexp.MustCompile(`\$(\d+)`)
	body = reSimple.ReplaceAllStringFunc(body, func(m string) string {
		sub := reSimple.FindStringSubmatch(m)
		idx, _ := strconv.Atoi(sub[1])
		if idx >= 1 && idx <= len(parts) {
			return parts[idx-1]
		}
		return m
	})
	return body
}

// Help 生成可用模板列表（/templates 命令输出）。
func (r *Registry) Help() string {
	if len(r.tpls) == 0 {
		return "暂无模板（在 ~/.mizar/prompts/ 放置 .md 文件即可）"
	}
	var sb strings.Builder
	sb.WriteString("可用模板（输入 /name [args] 展开）：\n")
	for _, n := range r.List() {
		t := r.tpls[n]
		hint := t.ArgHint
		fmt.Fprintf(&sb, "  /%-15s %s%s\n", n, t.Description, hint)
	}
	return sb.String()
}
