package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/plugins"
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
// 当 prefix 以 / 结尾时，列出目录下的所有条目（供用户浏览）。
// 否则按文件名前缀过滤。返回最多 100 个候选。
func completeAtPath(prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}

	cdir, err := os.Getwd()
	if err != nil {
		return nil
	}

	// 判断是否为目录浏览模式（以 / 结尾）
	isBrowseMode := strings.HasSuffix(prefix, "/")

	// 取父目录
	var parent string
	if isBrowseMode {
		parent = prefix // 保留末尾 / 供后续判断
	} else {
		parent = filepath.Dir(prefix)
	}

	if parent == "" || parent == "." {
		parent = cdir
	} else if !filepath.IsAbs(parent) {
		parent = filepath.Join(cdir, parent)
	}

	// 计算文件名过滤前缀
	var namePrefix string
	if isBrowseMode {
		namePrefix = "" // 浏览模式不过滤
	} else {
		namePrefix = filepath.Base(prefix)
	}

	es, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var result []string
	for _, e := range es {
		entryName := e.Name()
		// 浏览模式（/ 结尾）：列出所有
		// 匹配模式：按 namePrefix 过滤
		if namePrefix != "" && !strings.HasPrefix(entryName, namePrefix) {
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

// reloadPlugins 强制重载插件目录中的全部插件，并同步斜杠命令、失效系统提示缓存。
// 返回给调用方（模型/命令）的人类可读摘要。供 write/edit 自动热重载、/reload 命令、
// 以及模型显式调用 reload_plugins 工具共用，保证各处行为一致。
func reloadPlugins(pm *plugins.Manager, a *agent.Agent) string {
	loaded, failed := pm.ReloadAll()
	// 同步插件导出的斜杠命令到注册表
	cmds := make([]agent.Command, 0, len(pm.Commands()))
	for _, c := range pm.Commands() {
		cc := c
		cmds = append(cmds, agent.Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
	}
	a.Commands.SyncFromPlugins(cmds)
	// 失效系统提示缓存（工具列表已变化，下次 SystemPrompt 重建）
	a.ReloadTools()

	var sb strings.Builder
	if len(loaded) > 0 {
		fmt.Fprintf(&sb, "✓ 插件已重载: %s", strings.Join(loaded, ", "))
	}
	for f, e := range failed {
		fmt.Fprintf(&sb, "\n✗ 插件加载失败: %s: %v", f, e)
	}
	if sb.Len() == 0 {
		return "插件目录无变更，未触发重载"
	}
	return sb.String()
}

// onPluginFileWritten 判断 write/edit 写入的文件是否位于插件目录内的 .ts/.js，
// 是则触发插件热重载并返回给模型的说明（否则返回空，不追加输出）。
func onPluginFileWritten(pluginDir string, pm *plugins.Manager, a *agent.Agent) func(string) string {
	return func(path string) string {
		if pluginDir == "" || path == "" {
			return ""
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ts" && ext != ".js" {
			return ""
		}
		// 插件管理器只扫描插件目录顶层，因此文件必须直接位于插件目录下
		dir := filepath.Dir(path)
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return ""
		}
		if absDir != pluginDir {
			return ""
		}
		return "\n[自动热重载] " + reloadPlugins(pm, a)
	}
}
