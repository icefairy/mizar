package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
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
	PluginFile  string // 来源插件文件（空=内置命令）
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

// SyncFromPlugins 全量同步插件命令：先移除所有插件来源命令，再注册最新一批。
func (r *CommandRegistry) SyncFromPlugins(cmds []Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, c := range r.commands {
		if c.PluginFile != "" {
			delete(r.commands, name)
		}
	}
	for _, c := range cmds {
		if c.Name != "" {
			r.commands[c.Name] = c
		}
	}
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

// Help 生成 /help 文本（兜底实现；若注册了 help 命令则优先用注册命令）。
// 注册了 help 命令时列表已含 /help 项，因此不再追加硬编码行，避免重复。
func (r *CommandRegistry) Help() string {
	var sb strings.Builder
	sb.WriteString("可用命令：\n")
	for _, c := range r.List() {
		fmt.Fprintf(&sb, "  /%-10s %s\n", c.Name, c.Description)
	}
	if _, ok := r.Get("help"); !ok {
		sb.WriteString("  /help      显示本帮助")
	}
	return sb.String()
}

// Dispatch 解析并执行 "/name args" 行。非命令行返回 (false, "", nil)。
// 注意：Run 回调必须快速返回，不可阻塞主事件循环（网络请求请放 goroutine）。
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
		// 优先执行注册的 help 命令（描述更完整）；未注册时回退兜底文本。
		if c, ok := r.Get("help"); ok && c.Run != nil {
			out, err = c.Run(args)
			return true, out, err
		}
		return true, r.Help(), nil
	}
	c, ok := r.Get(name)
	if !ok {
		return true, "", fmt.Errorf("未知命令 /%s（输入 /help 查看可用命令）", name)
	}
	out, err = c.Run(args)
	return true, out, err
}

// cmdAsyncResult 异步命令执行的结果。
type cmdAsyncResult struct {
	Handled bool
	Out     string
	Err     error
}

// DispatchAsync 异步执行命令：立即返回，结果通过 done channel 传出。
// 用于网络 IO 型命令（如 /model、/reload），避免阻塞 TUI 主线程。
// timeout=0 表示无超时限制。
func (r *CommandRegistry) DispatchAsync(line string, timeout time.Duration) <-chan cmdAsyncResult {
	ch := make(chan cmdAsyncResult, 1)
	go func() {
		result := cmdAsyncResult{}
		defer func() {
			ch <- result
		}()
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "/") {
			result.Handled = false
			return
		}
		fields := strings.Fields(trimmed)
		name := strings.TrimPrefix(fields[0], "/")
		args := ""
		if len(fields) > 1 {
			args = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		}
		if name == "help" {
			if c, ok := r.Get("help"); ok && c.Run != nil {
				out, err := c.Run(args)
				result.Handled = true
				result.Out, result.Err = out, err
				return
			}
			result.Handled = true
			result.Out = r.Help()
			return
		}
		c, ok := r.Get(name)
		if !ok {
			result.Handled = true
			result.Err = fmt.Errorf("未知命令 /%s（输入 /help 查看可用命令）", name)
			return
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := c.Run(args)
			result.Handled = true
			result.Out, result.Err = out, err
		}()
		if timeout > 0 {
			select {
			case <-time.After(timeout):
				result.Handled = true
				result.Err = fmt.Errorf("命令 /%s 执行超时（%s）", name, timeout)
			case <-resultChTimeout(nil, &wg):
				// 等待完成
			}
		} else {
			wg.Wait()
		}
	}()
	return ch
}

// resultChTimeout 辅助：等待 wg 完成或超时。
func resultChTimeout(done chan struct{}, wg *sync.WaitGroup) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}
