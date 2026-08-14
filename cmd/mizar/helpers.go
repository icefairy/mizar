package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findAtSuffix 找行末尾的 @ 部分（用于实时补全）。
func findAtSuffix(line string) string {
	parts := strings.Split(line, " ")
	last := parts[len(parts)-1]
	if idx := strings.LastIndex(last, "@"); idx >= 0 {
		return strings.TrimSpace(last[idx+1:])
	}
	return ""
}

// completeAtRaw 根据 @ 后面的输入提供补全建议。
func completeAtRaw(after string, cmdNames []string, toolNames []string) []string {
	after = strings.TrimSpace(after)
	if after == "" {
		return []string{"@cmd:", "@tool:", "@file:", "@read:", "@dir:"}
	}

	var category string
	var raw string
	switch {
	case strings.HasPrefix(after, "cmd:"):
		category = "cmd"
		raw = strings.TrimPrefix(after, "cmd:")
	case strings.HasPrefix(after, "tool:"):
		category = "tool"
		raw = strings.TrimPrefix(after, "tool:")
	case strings.HasPrefix(after, "file:"):
		category = "file"
		raw = strings.TrimPrefix(after, "file:")
	case strings.HasPrefix(after, "read:"):
		category = "file"
		raw = strings.TrimPrefix(after, "read:")
	case strings.HasPrefix(after, "dir:"):
		category = "dir"
		raw = strings.TrimPrefix(after, "dir:")
	default:
		return completeAtPath(after)
	}

	raw = strings.TrimSpace(raw)
	prefix := raw
	switch category {
	case "cmd":
		results := make([]string, 0)
		for _, n := range cmdNames {
			name := strings.TrimPrefix(n, "/")
			if strings.HasPrefix(name, prefix) || prefix == "" {
				results = append(results, "@cmd:"+name)
			}
		}
		if prefix == "cmd" && len(results) == 0 {
			return []string{"@cmd:"}
		}
		return results

	case "tool":
		results := make([]string, 0)
		for _, n := range toolNames {
			if strings.HasPrefix(n, prefix) || prefix == "" {
				results = append(results, "@tool:"+n)
			}
		}
		if prefix == "tool" && len(results) == 0 {
			return []string{"@tool:"}
		}
		return results

	case "file":
		files := completeAtPath(raw)
		results := make([]string, 0, len(files))
		for _, f := range files {
			results = append(results, "@file:"+f)
		}
		return results

	case "dir":
		dirs := completeAtPath(raw)
		results := make([]string, 0, len(dirs))
		for _, d := range dirs {
			if strings.HasSuffix(d, "/") {
				results = append(results, "@dir:"+strings.TrimSuffix(d, "/"))
			}
		}
		return results
	}
	return nil
}

// completeAtPath 文件路径补全。
func completeAtPath(prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	parent := filepath.Dir(prefix)
	name := filepath.Base(prefix)
	cdir, err := os.Getwd()
	if err != nil {
		return nil
	}
	if parent == "" || parent == "." {
		parent = cdir
	} else if !filepath.IsAbs(parent) {
		parent = filepath.Join(cdir, parent)
	}
	es, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var result []string
	for _, e := range es {
		entryName := e.Name()
		if name != "" && !strings.HasPrefix(entryName, name) {
			continue
		}
		entryPath := filepath.Join(parent, entryName)
		if e.IsDir() {
			entryPath = entryPath + "/"
		}
		result = append(result, entryPath)
	}
	if len(result) > 100 {
		result = result[:100]
	}
	sort.Strings(result)
	return result
}

// resolveAtRef 解析 @ 引用为任务文本。
func resolveAtRef(line string, cmdNames, toolNames []string) string {
	if !strings.Contains(line, "@") {
		return line
	}

	last := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '@' {
			last = i
		}
	}
	if last == 0 {
		return line
	}

	after := strings.TrimSpace(line[last+1:])
	var resolved string

	switch {
	case strings.HasPrefix(after, "cmd:"):
		name := strings.TrimPrefix(after, "cmd:")
		resolved = fmt.Sprintf("执行命令 /%s", name)
	case strings.HasPrefix(after, "tool:"):
		name := strings.TrimPrefix(after, "tool:")
		resolved = fmt.Sprintf("使用工具 %s", name)
	case strings.HasPrefix(after, "file:"):
		resolved = fmt.Sprintf("读取文件 %s", strings.TrimPrefix(after, "file:"))
	case strings.HasPrefix(after, "read:"):
		resolved = fmt.Sprintf("读取文件 %s", strings.TrimPrefix(after, "read:"))
	case strings.HasPrefix(after, "dir:"):
		resolved = fmt.Sprintf("查看目录 %s", strings.TrimPrefix(after, "dir:"))
	default:
		resolved = fmt.Sprintf("读取文件 %s", after)
	}

	return strings.TrimSpace(line[:last]) + resolved
}
