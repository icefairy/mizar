// Package plugins 实现插件加载器：扫描 extensions/ 目录的 .ts 文件，
// esbuild 编译为 JS，goja 执行并注册导出的 tool_* 函数。
package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mizar/internal/engine"
)

// Tool 是注册到 Agent 的一个工具。
type Tool struct {
	Name        string // 如 disk_watch
	Description string // 从插件 meta 或注释读取
	PluginFile  string // 来源插件文件
	// Run 在引擎内执行；args 是字符串参数。
	Run func(args string) (string, error)
}

// Manager 管理插件加载、热重载与工具注册。
type Manager struct {
	mu       sync.RWMutex
	dir      string
	host     *engine.HostFuncs
	engines  map[string]*engine.Engine // 每个插件独立引擎（隔离）
	tools    map[string]Tool
	commands map[string]Command // 斜杠命令（command_* 导出）
	rpcMethods map[string]RPCMethod // 自定义 JSON-RPC 方法（rpc_* 导出）
	modTime  map[string]time.Time
	maxExec  time.Duration // 单次插件执行超时
	onTool   func(name string) // 工具调用回调（技能统计用）
	disabled map[string]bool // 禁用的工具（不注册、不可调用，不影响提示词）
}

// NewManager 创建插件管理器。
func NewManager(dir string, host *engine.HostFuncs) *Manager {
	return &Manager{
		dir:      dir,
		host:     host,
		engines:  make(map[string]*engine.Engine),
		tools:    make(map[string]Tool),
		commands: make(map[string]Command),
		rpcMethods: make(map[string]RPCMethod),
		modTime:    make(map[string]time.Time),
		disabled:   make(map[string]bool),
		maxExec:    30 * time.Second,
	}
}

// SetMaxExec 设置插件单次执行超时（默认 30s）。
func (m *Manager) SetMaxExec(d time.Duration) { m.maxExec = d }

// LoadAll 扫描目录，加载/重载所有 .ts 插件。返回新增与失败的插件名。
func (m *Manager) LoadAll() (loaded []string, failed map[string]error) {
	return m.loadAll(false)
}

// ReloadAll 强制重载所有插件（忽略 modTime，用于 host 函数注入后同步）。
func (m *Manager) ReloadAll() (loaded []string, failed map[string]error) {
	return m.loadAll(true)
}

func (m *Manager) loadAll(force bool) (loaded []string, failed map[string]error) {
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
		if strings.HasSuffix(e.Name(), ".ts") || strings.HasSuffix(e.Name(), ".js") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		info, err := os.Stat(filepath.Join(m.dir, f))
		if err != nil {
			failed[f] = err
			continue
		}
		m.mu.Lock()
		old := m.modTime[f]
		m.mu.Unlock()
		if !force && !info.ModTime().After(old) && old.IsZero() == false {
			continue // 未变化
		}
		if err := m.loadPlugin(f); err != nil {
			failed[f] = err
		} else {
			loaded = append(loaded, f)
		}
	}
	return loaded, failed
}

// 纯 JS 插件直接执行（跳过 esbuild 编译），TS 才需要转译。
// 要求：JS 文件本身是 goja 可执行的 ES 语法（避免最新的 ESNext 语法）。
func (m *Manager) loadPlugin(filename string) error {
	path := filepath.Join(m.dir, filename)
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var js string
	if strings.HasSuffix(filename, ".ts") {
		js, err = engine.CompileTS(filename, string(src))
		if err != nil {
			return err
		}
	} else {
		// .js：免编译直喂 goja（少一道 esbuild 转译）
		js = string(src)
	}
	vm, err := engine.New(m.host)
	if err != nil {
		return err
	}
	if err := vm.RunScript(filename, js); err != nil {
		vm.Close()
		return fmt.Errorf("exec %s: %w", filename, err)
	}
	// 收集导出的 tool_* 函数与 command_* 命令、rpc_* 方法
	tools := m.collectTools(filename, vm)
	cmds := m.collectCommands(filename, vm)
	rpcs := m.collectRPC(filename, vm)
	if len(tools) == 0 && len(cmds) == 0 && len(rpcs) == 0 {
		vm.Close()
		return fmt.Errorf("plugin %s: no tool_*, command_* or rpc_* exports found", filename)
	}
	// 原子替换：先收集再提交
	m.mu.Lock()
	defer m.mu.Unlock()
	// 移除旧引擎里属于该文件的工具与命令
	m.removeToolsLocked(filename)
	m.removeCommandsLocked(filename)
	m.removeRPCLocked(filename)
	for _, t := range tools {
		m.tools[t.Name] = t
	}
	for _, c := range cmds {
		m.commands[c.Name] = c
	}
	for _, r := range rpcs {
		m.rpcMethods[r.Name] = r
	}
	if old, ok := m.engines[filename]; ok {
		old.Close()
	}
	m.engines[filename] = vm
	if info, err := os.Stat(path); err == nil {
		m.modTime[filename] = info.ModTime()
	}
	return nil
}

func (m *Manager) collectTools(filename string, vm *engine.Engine) []Tool {
	var tools []Tool
	// 通过 JS 侧枚举全局函数名
	names, err := vm.GlobalNames()
	if err != nil {
		return nil
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "tool_") {
			continue
		}
		if !vm.Has(name) {
			continue
		}
		toolName := strings.TrimPrefix(name, "tool_")
		tools = append(tools, Tool{
			Name:       toolName,
			PluginFile: filename,
			Run: func(args string) (string, error) {
				res, err := vm.Call(name, args)
				if err != nil {
					return "", err
				}
				if res == nil {
					return "", nil
				}
				return fmt.Sprintf("%v", res), nil
			},
		})
	}
	return tools
}

func (m *Manager) removeToolsLocked(filename string) {
	for k, t := range m.tools {
		if t.PluginFile == filename {
			delete(m.tools, k)
		}
	}
}

// Tools 返回当前所有工具（按名排序，排除禁用的）。
func (m *Manager) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Tool, 0, len(m.tools))
	for _, t := range m.tools {
		if m.disabled[t.Name] {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetDisabledTools 设置禁用的工具集合。
// 禁用只影响工具注册表（LLM 看不到、调不到），不影响 System prompt ——
// 提示词保持字节稳定，前缀缓存不破坏。
func (m *Manager) SetDisabledTools(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range names {
		m.disabled[n] = true
	}
}

// Get 按名取工具（禁用的返回 false）。
func (m *Manager) Get(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tools[name]
	if ok && m.disabled[name] {
		return t, false
	}
	return t, ok
}

// SetOnToolCall 注册工具调用回调（每次工具被调用都会触发，用于技能统计）。
func (m *Manager) SetOnToolCall(fn func(name string)) {
	m.mu.Lock()
	m.onTool = fn
	m.mu.Unlock()
}

// Call 执行工具（带超时）。
func (m *Manager) Call(name, args string) (string, error) {
	t, ok := m.Get(name)
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	// 统计回调（技能每次使用 +1）
	m.mu.RLock()
	cb := m.onTool
	m.mu.RUnlock()
	if cb != nil {
		cb(name)
	}
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := t.Run(args)
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(m.maxExec):
		return "", errors.New("tool execution timeout: " + name)
	}
}

// PluginNames 返回已加载插件名。
func (m *Manager) PluginNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.engines))
	for f := range m.engines {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// RegisterBuiltin 注册内置工具（Go 侧实现，来源标记 <builtin>，不受插件热重载影响）。
func (m *Manager) RegisterBuiltin(t Tool) {
	if t.PluginFile == "" {
		t.PluginFile = "<builtin>"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[t.Name] = t
}

// RemoveBuiltin 移除内置工具。
func (m *Manager) RemoveBuiltin(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tools, name)
}
