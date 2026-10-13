package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

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

// isAtTokenChar 判断字符是否属于「词内字符」。@ 紧跟这类字符时（p@ssw0rd、
// user@example.com）说明 @ 是词的一部分，不是文件引用起点（对齐 pi 的边界规则）。
func isAtTokenChar(r rune) bool {
	return r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// lastAtMentionIndex 返回可作为引用起点的最后一个 @ 的下标，没有则返回 -1。
// 要求 @ 处于 token 边界（行首或前一个字符是空白/标点），
// 否则粘贴带 @ 的密码、邮箱会被误当成「读取文件」。
func lastAtMentionIndex(line string) int {
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] != '@' {
			continue
		}
		if i == 0 {
			return i
		}
		prev, _ := utf8.DecodeLastRuneInString(line[:i])
		if !isAtTokenChar(prev) {
			return i
		}
	}
	return -1
}

// atToken 从 atIdx 处的 @ 提取引用 token，返回 token、结束偏移（字节）与是否有效。
// 支持 @path（到空白为止，尾部中英标点剔除）与 @"带 空格 的路径" 两种写法。
func atToken(line string, atIdx int) (string, int, bool) {
	i := atIdx + 1
	if i < len(line) && line[i] == '"' {
		if close := strings.IndexByte(line[i+1:], '"'); close >= 0 {
			token := line[i+1 : i+1+close]
			if strings.TrimSpace(token) == "" {
				return "", 0, false
			}
			return token, i + 2 + close, true
		}
		return "", 0, false
	}

	start := i
	for i < len(line) {
		r, size := utf8.DecodeRuneInString(line[i:])
		if unicode.IsSpace(r) || r == '"' {
			break
		}
		i += size
	}
	token := strings.TrimRight(line[start:i], ",;，；、。！？!")
	if token == "" {
		return "", 0, false
	}
	return token, start + len(token), true
}

// looksLikePath 判断 token 是否值得当成文件路径解析。
// 路径真实存在，或带明确路径形态（含 / 、以 ./ ../ / ~/ 开头）才解析；
// 其余（如行首的 @ssw0rd）视为普通文本，避免吞掉密码。
func looksLikePath(token string) bool {
	if token == "" {
		return false
	}
	if _, err := os.Stat(token); err == nil {
		return true
	}
	for _, p := range []string{"./", "../", "/", "~/"} {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return strings.Contains(token, "/")
}

// atAfterToken 提取 @ 之后用于补全的输入片段：
// 显式前缀（cmd:/tool:/file:/read:/dir:）保持原样（含冒号）以便分类匹配，
// 否则返回已输入的部分路径（引号形式取引号内内容）。
func atAfterToken(line string, atIdx int) string {
	rest := line[atIdx+1:]
	if strings.HasPrefix(rest, "\"") {
		// 引号形式（含未闭合的输入中状态）：取引号内内容用于补全
		return strings.Trim(rest, "\"")
	}
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		rest = rest[:i]
	}
	for _, p := range []string{"cmd:", "tool:", "file:", "read:", "dir:"} {
		if strings.HasPrefix(rest, p) {
			return rest
		}
	}
	if token, _, ok := atToken(line, atIdx); ok {
		return token
	}
	return ""
}

// resolveAtRef 解析 @ 引用为任务文本。
// 仅当 @ 处于 token 边界且后面是显式前缀（cmd:/tool:/file:/read:/dir:）
// 或真实路径时才改写，否则原样返回——避免把密码、邮箱中的 @ 当成文件引用。
func resolveAtRef(line string, cmdNames, toolNames []string) string {
	atIdx := lastAtMentionIndex(line)
	if atIdx < 0 {
		return line
	}
	token, end, ok := atToken(line, atIdx)
	if !ok {
		return line
	}
	rest := line[end:]

	var resolved string
	switch {
	case strings.HasPrefix(token, "cmd:"):
		resolved = fmt.Sprintf("执行命令 /%s", strings.TrimPrefix(token, "cmd:"))
	case strings.HasPrefix(token, "tool:"):
		resolved = fmt.Sprintf("使用工具 %s", strings.TrimPrefix(token, "tool:"))
	case strings.HasPrefix(token, "file:"):
		resolved = fmt.Sprintf("读取文件 %s", strings.TrimPrefix(token, "file:"))
	case strings.HasPrefix(token, "read:"):
		resolved = fmt.Sprintf("读取文件 %s", strings.TrimPrefix(token, "read:"))
	case strings.HasPrefix(token, "dir:"):
		resolved = fmt.Sprintf("查看目录 %s", strings.TrimPrefix(token, "dir:"))
	case looksLikePath(token):
		resolved = fmt.Sprintf("读取文件 %s", token)
	default:
		return line
	}

	return strings.TrimSpace(line[:atIdx] + resolved + rest)
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
