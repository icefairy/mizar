package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mizar/internal/plugins"
)

// toolGrep 内容搜索（对齐 pi 的 grep：pattern + path + glob + ignoreCase + literal + context）。
func toolGrep() plugins.Tool {
	return plugins.Tool{
		Name:        "grep",
		Description: "Search file contents. Args: {pattern: string(regex), path?: string(default cwd), glob?: string, ignoreCase?: bool, literal?: bool, context?: number(lines)}. Returns matches with line numbers.",
		Run: func(args string) (string, error) {
			var p struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Glob       string `json:"glob"`
				IgnoreCase bool   `json:"ignoreCase"`
				Literal    bool   `json:"literal"`
				Context    int    `json:"context"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Pattern == "" {
				return "", fmt.Errorf("grep: args {pattern} required")
			}
			if p.Path == "" {
				p.Path = "."
			}
			var re *regexp.Regexp
			if p.Literal {
				re, _ = regexp.Compile(regexp.QuoteMeta(p.Pattern))
			} else {
				re, _ = regexp.Compile(p.Pattern)
			}
			if re == nil {
				return "", fmt.Errorf("grep: invalid pattern")
			}
			if p.IgnoreCase {
				re, _ = regexp.Compile("(?i)" + re.String())
			}
			var sb strings.Builder
			var matched int
			err := filepath.Walk(p.Path, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				// glob 过滤（相对路径匹配）
				if p.Glob != "" {
					ok, _ := filepath.Match(p.Glob, info.Name())
					if !ok {
						return nil
					}
				}
				b, err := os.ReadFile(path)
				if err != nil || len(b) > 4<<20 {
					return nil // 跳过二进制/超大文件
				}
				lines := strings.Split(string(b), "\n")
				for i, line := range lines {
					if re.MatchString(line) {
						// context 前后行
						if p.Context > 0 {
							start := i - p.Context
							if start < 0 {
								start = 0
							}
							end := i + p.Context + 1
							if end > len(lines) {
								end = len(lines)
							}
							for j := start; j < end; j++ {
								fmt.Fprintf(&sb, "%s:%d|%s\n", path, j+1, lines[j])
							}
							fmt.Fprintln(&sb, "---")
						} else {
							fmt.Fprintf(&sb, "%s:%d|%s\n", path, i+1, line)
						}
						matched++
						if matched >= 200 {
							sb.WriteString("(truncated at 200 matches)\n")
							return filepath.SkipAll
						}
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			if matched == 0 {
				return "no matches", nil
			}
			return fmt.Sprintf("%d match(es):\n%s", matched, sb.String()), nil
		},
	}
}

// toolFind 文件查找（对齐 pi 的 find：pattern(glob) + path + limit）。
func toolFind() plugins.Tool {
	return plugins.Tool{
		Name:        "find",
		Description: "Find files by glob pattern. Args: {pattern: string(glob like '**/*.ts'), path?: string(default cwd), limit?: number(default 1000)}.",
		Run: func(args string) (string, error) {
			var p struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Limit   int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Pattern == "" {
				return "", fmt.Errorf("find: args {pattern} required")
			}
			if p.Path == "" {
				p.Path = "."
			}
			if p.Limit <= 0 {
				p.Limit = 1000
			}
			var out []string
			err := filepath.Walk(p.Path, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(p.Path, path)
				ok, _ := filepath.Match(p.Pattern, rel)
				if !ok {
					ok, _ = filepath.Match(p.Pattern, info.Name())
				}
				if ok {
					out = append(out, rel)
				}
				if len(out) >= p.Limit {
					return filepath.SkipAll
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			sort.Strings(out)
			if len(out) == 0 {
				return "no files found", nil
			}
			return fmt.Sprintf("%d file(s):\n%s", len(out), strings.Join(out, "\n")), nil
		},
	}
}
