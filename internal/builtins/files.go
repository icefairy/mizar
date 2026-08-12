package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mizar/internal/plugins"
)

// toolRead 读文件（对齐 pi 的 read：path + offset + limit）。
func toolRead() plugins.Tool {
	return plugins.Tool{
		Name:        "read",
		Description: "Read a text file. Args: {path: string, offset?: number(1-indexed), limit?: number(lines)}. Returns content with line numbers.",
		Run: func(args string) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("read: args {path} required")
			}
			b, err := os.ReadFile(p.Path)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(b), "\n")
			start, end := 0, len(lines)
			if p.Offset > 0 {
				start = p.Offset - 1
			}
			if p.Limit > 0 {
				end = start + p.Limit
				if end > len(lines) {
					end = len(lines)
				}
			}
			if start >= len(lines) {
				start = len(lines) - 1
			}
			if start < 0 {
				start = 0
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d lines (total %d)\n", end-start, len(lines))
			for i := start; i < end; i++ {
				fmt.Fprintf(&sb, "%d|%s\n", i+1, lines[i])
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	}
}

// toolWrite 写文件（对齐 pi 的 write：path + content，自动建父目录）。
func toolWrite() plugins.Tool {
	return plugins.Tool{
		Name:        "write",
		Description: "Write content to a file (creates parent dirs, overwrites). Args: {path: string, content: string}.",
		Run: func(args string) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("write: args {path, content} required")
			}
			if dir := filepath.Dir(p.Path); dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "", err
				}
			}
			if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	}
}

// toolEdit 精准替换（对齐 pi 的 edit：path + oldText + newText，可多组 edits）。
func toolEdit() plugins.Tool {
	return plugins.Tool{
		Name:        "edit",
		Description: "Edit a file with targeted replacements. Args: {path: string, oldText: string, newText: string} or {path: string, edits: [{oldText, newText}]}. oldText must be unique. Returns diff summary.",
		Run: func(args string) (string, error) {
			var p struct {
				Path    string `json:"path"`
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
				Edits   []struct {
					OldText string `json:"oldText"`
					NewText string `json:"newText"`
				} `json:"edits"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("edit: args {path} required")
			}
			b, err := os.ReadFile(p.Path)
			if err != nil {
				return "", err
			}
			content := string(b)
			edits := p.Edits
			if len(edits) == 0 && p.OldText != "" {
				edits = []struct {
					OldText string `json:"oldText"`
					NewText string `json:"newText"`
				}{{OldText: p.OldText, NewText: p.NewText}}
			}
			if len(edits) == 0 {
				return "", fmt.Errorf("edit: oldText or edits required")
			}
			applied := 0
			for _, e := range edits {
				if e.OldText == "" {
					continue
				}
				count := strings.Count(content, e.OldText)
				if count != 1 {
					return "", fmt.Errorf("edit: oldText %q appears %d times (must be unique)", truncate(e.OldText, 50), count)
				}
				content = strings.Replace(content, e.OldText, e.NewText, 1)
				applied++
			}
			if applied == 0 {
				return "", fmt.Errorf("edit: no edits applied")
			}
			if err := os.WriteFile(p.Path, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("applied %d edit(s) to %s", applied, p.Path), nil
		},
	}
}

// toolLS 列目录（对齐 pi 的 ls：path + limit）。
func toolLS() plugins.Tool {
	return plugins.Tool{
		Name:        "ls",
		Description: "List directory entries. Args: {path?: string, limit?: number(default 500)}. Directories suffixed with /.",
		Run: func(args string) (string, error) {
			var p struct {
				Path  string `json:"path"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", err
			}
			if p.Path == "" {
				p.Path = "."
			}
			if p.Limit <= 0 {
				p.Limit = 500
			}
			entries, err := os.ReadDir(p.Path)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > p.Limit {
				names = names[:p.Limit]
			}
			return fmt.Sprintf("%d entries:\n%s", len(entries), strings.Join(names, "\n")), nil
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
