// Package plugins 实现插件加载器：扫描 extensions/ 目录的 .ts 文件，
// esbuild 编译为 JS，goja 执行并注册导出的 tool_* 函数。
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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

// 插件生命周期钩子：插件导出 plugin_init() 在加载成功后调用（初始化），
// plugin_cleanup() 在卸载/热重载替换前调用（反初始化，如关闭连接/释放端口）。
const (
	pluginInitFn    = "plugin_init"
	pluginCleanupFn = "plugin_cleanup"
)

// Manager 管理插件加载、热重载与工具注册。
type Manager struct {
	mu         sync.RWMutex
	dir        string
	host       *engine.HostFuncs
	engines    map[string]*engine.Engine // 每个插件独立引擎（隔离）
	tools      map[string]Tool
	commands   map[string]Command   // 斜杠命令（command_* 导出）
	rpcMethods map[string]RPCMethod // 自定义 JSON-RPC 方法（rpc_* 导出）
	modTime    map[string]time.Time
	maxExec    time.Duration     // 单次插件执行超时
	onTool     func(name string) // 工具调用回调（技能统计用）
	disabled   map[string]bool   // 禁用的工具（不注册、不可调用，不影响提示词）

	// LSP 提供者注册跟踪（按文件名，热重载时清理用）
	lspDiagNames map[string][]string // filename → registered diagnostic provider names
	lspCompNames map[string][]string // filename → registered completion provider names
	// 当前正在加载的插件（供 host func 包装使用）
	loadFile string
	// 当前加载的 LSP 注册计数（用于判定 LSP-only 插件）
	lspRegCount int

	// 活跃 HTTP 服务器跟踪（host_listen 用）
	// pluginFile → 该插件启动的服务器列表
	activeServers map[string][]*http.Server

	// Hook 回调注册表（hook_on 宿主函数收集，Agent 触发挂载点时 Fire）
	hookReg *hookRegistry
}

// NewManager 创建插件管理器。
func NewManager(dir string, host *engine.HostFuncs) *Manager {
	m := &Manager{
		dir:          dir,
		host:         host,
		engines:      make(map[string]*engine.Engine),
		tools:        make(map[string]Tool),
		commands:     make(map[string]Command),
		rpcMethods:   make(map[string]RPCMethod),
		modTime:      make(map[string]time.Time),
		disabled:     make(map[string]bool),
		maxExec:      30 * time.Second,
		lspDiagNames: make(map[string][]string),
		lspCompNames: make(map[string][]string),
		activeServers: make(map[string][]*http.Server),
		hookReg:      newHookRegistry(),
	}
	// 包装 LSP 宿主函数，跟踪文件名
	if host != nil {
		origD := host.LSPRegisterDiagnostic
		origC := host.LSPRegisterCompletion
		host.LSPRegisterDiagnostic = func(name string, fn func(uri string) string) error {
			m.mu.Lock()
			m.lspDiagNames[m.loadFile] = append(m.lspDiagNames[m.loadFile], name)
			m.lspRegCount++
			m.mu.Unlock()
			if origD != nil {
				return origD(name, fn)
			}
			return nil
		}
		host.LSPRegisterCompletion = func(name string, fn func(uri string, line, col int) string) error {
			m.mu.Lock()
			m.lspCompNames[m.loadFile] = append(m.lspCompNames[m.loadFile], name)
			m.lspRegCount++
			m.mu.Unlock()
			if origC != nil {
				return origC(name, fn)
			}
			return nil
		}
		// HookOn：插件 hook_on() → Manager.hookReg 收集（按文件跟踪，热重载清理）
		host.HookOn = func(event string, cb func(ctxJSON string) (string, error)) error {
			if cb == nil {
				return fmt.Errorf("hook_on: 回调不能为 nil")
			}
			m.mu.Lock()
			file := m.loadFile
			m.mu.Unlock()
			return m.hookReg.Register(file, event, cb)
		}
		// host_listen：Manager 接管 HTTP 服务器创建，实现追踪和热重载关闭
		host.HostListen = func(addr string, handler func(string) string) (string, error) {
				m.mu.Lock()
				file := m.loadFile
				m.mu.Unlock()

				srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					reqMap := map[string]any{
						"method":  r.Method,
						"path":    r.URL.Path,
						"query":   r.URL.RawQuery,
						"body":    string(body),
						"headers": r.Header,
					}
					reqJSON, _ := json.Marshal(reqMap)
					respStr := handler(string(reqJSON))
					var resp struct {
						Status  int               `json:"status"`
						Body    string            `json:"body"`
						Headers map[string]string `json:"headers,omitempty"`
					}
					if err := json.Unmarshal([]byte(respStr), &resp); err != nil {
						http.Error(w, "handler 返回无效 JSON", 500)
						return
					}
					if resp.Status == 0 {
						resp.Status = 200
					}
					for k, v := range resp.Headers {
						w.Header().Set(k, v)
					}
					w.WriteHeader(resp.Status)
					w.Write([]byte(resp.Body))
				})}

				m.mu.Lock()
				m.activeServers[file] = append(m.activeServers[file], srv)
				m.mu.Unlock()

				go func() {
					if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						fmt.Fprintf(os.Stderr, "host_listen: %s: %v\n", addr, err)
					}
				}()
				return fmt.Sprintf("HTTP 服务器已启动: http://%s", addr), nil
		}
	}
	return m
}

// SetMaxExec 设置插件单次执行超时（默认 30s）。
func (m *Manager) SetMaxExec(d time.Duration) { m.maxExec = d }

// FireHook 触发某挂载点事件：向所有注册该事件的插件回调分发 ctxJSON。
// 供 Agent 的 Hooks 桥接调用（见 cmd/mizar/main.go 的 On* 注册）。
// 返回 (成功数, 错误列表)；无回调时返回 (0, nil)。
func (m *Manager) FireHook(event string, ctxJSON string) (int, []error) {
	return m.hookReg.Fire(event, ctxJSON)
}

// HookCount 返回某事件已注册的回调数（测试/日志用）。
func (m *Manager) HookCount(event string) int {
	return m.hookReg.Count(event)
}

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
	// 清理已删除的插件（文件消失 → 调 plugin_cleanup + 注销工具/命令/RPC/hook/服务器）
	for f := range m.pluginFilesLocked() {
		if _, ok := filesSet(files)[f]; !ok {
			m.unloadPlugin(f)
		}
	}
	return loaded, failed
}

// filesSet 把文件名切片转成集合（删除检测用）。
func filesSet(files []string) map[string]bool {
	s := make(map[string]bool, len(files))
	for _, f := range files {
		s[f] = true
	}
	return s
}

// pluginFilesLocked 返回当前已加载插件的文件名集合。
func (m *Manager) pluginFilesLocked() map[string]struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]struct{}, len(m.engines))
	for f := range m.engines {
		out[f] = struct{}{}
	}
	return out
}

// unloadPlugin 卸载插件：调 plugin_cleanup + 注销全部注册项 + 关闭引擎。
func (m *Manager) unloadPlugin(filename string) {
	m.mu.Lock()
	if old, ok := m.engines[filename]; ok {
		delete(m.engines, filename)
		delete(m.modTime, filename)
		m.removeToolsLocked(filename)
		m.removeCommandsLocked(filename)
		m.removeRPCLocked(filename)
		m.removeLSPLocked(filename)
		m.removeServersLocked(filename)
		m.hookReg.RemoveFile(filename)
		m.mu.Unlock()
		// 生命周期回调（锁外）
		if old.Has(pluginCleanupFn) {
			if _, err := old.Call(pluginCleanupFn); err != nil {
				log.Printf("plugin_cleanup %s: %v", filename, err)
			}
		}
		old.Close()
		return
	}
	m.mu.Unlock()
}

// 纯 JS 插件直接执行（跳过 esbuild 编译），TS 才需要转译。
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
		js = string(src)
	}
	vm, err := engine.New(m.host)
	if err != nil {
		return err
	}

	// 设置当前加载文件名，以便 LSP/hook 宿主函数跟踪注册
	// 先快照并清理旧 hook（RunScript 会注册新 hook，须先移除旧文件残留）
	oldHooks := m.hookReg.SnapshotFile(filename)
	m.hookReg.RemoveFile(filename)
	m.mu.Lock()
	m.loadFile = filename
	m.lspRegCount = 0
	m.mu.Unlock()

	err = vm.RunScript(filename, js)

	m.mu.Lock()
	m.loadFile = ""
	lspRegs := m.lspRegCount
	m.mu.Unlock()

	if err != nil {
		vm.Close()
		m.hookReg.RestoreFile(oldHooks) // 回滚旧 hook
		return fmt.Errorf("exec %s: %w", filename, err)
	}

	// 收集导出的 tool_* 函数与 command_* 命令、rpc_* 方法
	tools := m.collectTools(filename, vm)
	cmds := m.collectCommands(filename, vm)
	rpcs := m.collectRPC(filename, vm)

	if len(tools) == 0 && len(cmds) == 0 && len(rpcs) == 0 && lspRegs == 0 {
		vm.Close()
		return fmt.Errorf("plugin %s: no tool_*, command_*, rpc_* exports or LSP providers found", filename)
	}
	// 原子替换：先收集再提交
	m.mu.Lock()
	// 移除旧引擎里属于该文件的工具、命令、RPC、LSP 提供者、Hook 回调
	m.removeToolsLocked(filename)
	m.removeCommandsLocked(filename)
	m.removeRPCLocked(filename)
	m.removeLSPLocked(filename)
	m.removeServersLocked(filename) // 关闭旧 HTTP 服务器，释放端口
	for _, t := range tools {
		m.tools[t.Name] = t
	}
	for _, c := range cmds {
		m.commands[c.Name] = c
	}
	for _, r := range rpcs {
		m.rpcMethods[r.Name] = r
	}
	var old *engine.Engine
	if old, _ = m.engines[filename]; old != nil {
		delete(m.engines, filename) // 先移出，plugin_cleanup 失败也不阻塞替换
	}
	m.engines[filename] = vm
	if info, err := os.Stat(path); err == nil {
		m.modTime[filename] = info.ModTime()
	}
	m.mu.Unlock()

	// 生命周期回调（锁外执行，插件内可自由调宿主函数）：
	// 1) 旧引擎 plugin_cleanup —— 热重载反初始化
	if old != nil {
		if old.Has(pluginCleanupFn) {
			if _, err := old.Call(pluginCleanupFn); err != nil {
				return fmt.Errorf("plugin_cleanup %s: %w", filename, err)
			}
		}
		old.Close()
	}
	// 2) 新引擎 plugin_init —— 加载初始化
	if vm.Has(pluginInitFn) {
		if _, err := vm.Call(pluginInitFn); err != nil {
			return fmt.Errorf("plugin_init %s: %w", filename, err)
		}
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

// removeLSPLocked 移除某插件注册的所有 LSP 提供者。
func (m *Manager) removeLSPLocked(filename string) {
	for _, name := range m.lspDiagNames[filename] {
		if m.host != nil && m.host.LSPUnregisterDiagnostic != nil {
			m.host.LSPUnregisterDiagnostic(name)
		}
	}
	for _, name := range m.lspCompNames[filename] {
		if m.host != nil && m.host.LSPUnregisterCompletion != nil {
			m.host.LSPUnregisterCompletion(name)
		}
	}
	delete(m.lspDiagNames, filename)
	delete(m.lspCompNames, filename)
}

// removeServersLocked 关闭某插件启动的所有 HTTP 服务器（热重载时调用）。
func (m *Manager) removeServersLocked(filename string) {
	for _, srv := range m.activeServers[filename] {
		if srv != nil {
			// 优雅关闭：等待现有连接完成（最多 2s），强制关闭监听
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
		}
	}
	delete(m.activeServers, filename)
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
