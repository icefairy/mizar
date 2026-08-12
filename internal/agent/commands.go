package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ============================================================================
// 斜杠命令注册表（CommandRegistry）
//
// 支持在 Agent 中注册 "/xxx" 形式的交互命令：
//   - 内置命令（wizard：/provider /model /think /context /save 等）
//   - 插件命令（插件导出 command_<name> 函数，plugins.Manager 收集）
//   - 用户程序（Server 模式、嵌入场景）调用 RegisterCommand 动态注册
//
// 同一命令名可被覆盖（后注册者胜出），适合插件热重载。
// ============================================================================

// Command 一个斜杠命令。
type Command struct {
	Name        string // 如 provider（调用为 /provider）
	Description string // 帮助文本
	Run         func(args string) (string, error)
}

// CommandRegistry 斜杠命令注册表（线程安全）。
type CommandRegistry struct {
	mu       sync.RWMutex
	commands map[string]Command
}

// NewCommandRegistry 创建命令注册表。
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{commands: make(map[string]Command)}
}

// Register 注册命令（覆盖同名）。
func (r *CommandRegistry) Register(c Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[c.Name] = c
}

// RegisterFromPlugins 把插件管理器收集的 command_* 全部注册进来。
func (r *CommandRegistry) RegisterFromPlugins(getCommands func() []Command) {
	for _, c := range getCommands() {
		r.Register(c)
	}
}

// Unregister 移除命令（插件热重载/卸载用）。
func (r *CommandRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.commands, name)
}

// Get 取命令。
func (r *CommandRegistry) Get(name string) (Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.commands[name]
	return c, ok
}

// List 列出所有命令（按名排序）。
func (r *CommandRegistry) List() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Command, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Help 生成 /help 文本。
func (r *CommandRegistry) Help() string {
	var sb strings.Builder
	sb.WriteString("可用命令：\n")
	for _, c := range r.List() {
		fmt.Fprintf(&sb, "  /%-10s %s\n", c.Name, c.Description)
	}
	sb.WriteString("  /help      显示本帮助")
	return sb.String()
}

// Dispatch 解析并执行 "/name args" 行。非命令行返回 (false, "", nil)。
func (r *CommandRegistry) Dispatch(line string) (handled bool, out string, err error) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return false, "", nil
	}
	fields := strings.Fields(trimmed)
	name := strings.TrimPrefix(fields[0], "/")
	args := ""
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	}
	if name == "help" {
		return true, r.Help(), nil
	}
	c, ok := r.Get(name)
	if !ok {
		return true, "", fmt.Errorf("未知命令 /%s（输入 /help 查看可用命令）", name)
	}
	out, err = c.Run(args)
	return true, out, err
}
