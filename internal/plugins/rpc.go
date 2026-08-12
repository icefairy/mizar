// 自定义 JSON-RPC 方法支持：插件导出 rpc_<name> 函数，
// 注册为 Server 的 JSON-RPC 方法（WS / HTTP /rpc 均可调用）。
// 与 tool_* / command_* 同一套收集/热重载机制。
package plugins

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mizar/internal/engine"
)

// RPCMethod 插件注册的一个 JSON-RPC 方法。
type RPCMethod struct {
	Name       string // 如 hello（调用为 "hello" 或 "plugin.hello"）
	PluginFile string
	Run        func(paramsJSON string) (string, error)
}

// collectRPC 从引擎收集 rpc_* 函数。
func (m *Manager) collectRPC(filename string, vm *engine.Engine) []RPCMethod {
	var out []RPCMethod
	names, err := vm.GlobalNames()
	if err != nil {
		return nil
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "rpc_") {
			continue
		}
		if !vm.Has(name) {
			continue
		}
		methodName := strings.TrimPrefix(name, "rpc_")
		out = append(out, RPCMethod{
			Name:       methodName,
			PluginFile: filename,
			Run: func(paramsJSON string) (string, error) {
				res, err := vm.Call(name, paramsJSON)
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
	return out
}

// removeRPCLocked 移除某插件注册的所有 RPC 方法。
func (m *Manager) removeRPCLocked(filename string) {
	for k, r := range m.rpcMethods {
		if r.PluginFile == filename {
			delete(m.rpcMethods, k)
		}
	}
}

// RPCMethods 返回当前所有 RPC 方法（按名排序）。
func (m *Manager) RPCMethods() []RPCMethod {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RPCMethod, 0, len(m.rpcMethods))
	for _, r := range m.rpcMethods {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CallRPC 执行插件 RPC 方法（带超时）。
func (m *Manager) CallRPC(name, paramsJSON string) (string, error) {
	m.mu.RLock()
	r, ok := m.rpcMethods[name]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("rpc method %q not found", name)
	}
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := r.Run(paramsJSON)
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(m.maxExec):
		return "", errors.New("rpc execution timeout: " + name)
	}
}
