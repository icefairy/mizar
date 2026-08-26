// 斜杠命令支持：插件导出 command_<name> 函数，注册为 Agent 可调用的
// 交互命令（/name args）。与 tool_* 同一套收集/热重载机制。
package plugins

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mizar/internal/engine"
)

// Command 是注册到 Agent 的一个斜杠命令。
type Command struct {
	Name        string // 如 model（调用为 /model）
	Description string // 帮助文本
	PluginFile  string // 来源插件文件
	Run         func(args string) (string, error)
}

// collectCommands 从引擎收集 command_* 函数。
func (m *Manager) collectCommands(filename string, vm *engine.Engine) []Command {
	var cmds []Command
	names, err := vm.GlobalNames()
	if err != nil {
		return nil
	}
	src := m.pluginSourceText(filename)
	for _, name := range names {
		if !strings.HasPrefix(name, "command_") {
			continue
		}
		if !vm.Has(name) {
			continue
		}
		cmdName := strings.TrimPrefix(name, "command_")
		cmds = append(cmds, Command{
			Name:        cmdName,
			Description: describePluginFunc(src, name),
			PluginFile:  filename,
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
	return cmds
}

// removeCommandsLocked 移除某插件注册的所有命令。
func (m *Manager) removeCommandsLocked(filename string) {
	for k, c := range m.commands {
		if c.PluginFile == filename {
			delete(m.commands, k)
		}
	}
}

// Commands 返回当前所有命令（按名排序）。
func (m *Manager) Commands() []Command {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Command, 0, len(m.commands))
	for _, c := range m.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetCommand 按名取命令。
func (m *Manager) GetCommand(name string) (Command, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.commands[name]
	return c, ok
}

// CallCommand 执行命令（带超时）。
func (m *Manager) CallCommand(name, args string) (string, error) {
	c, ok := m.GetCommand(name)
	if !ok {
		return "", fmt.Errorf("command %q not found", name)
	}
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := c.Run(args)
		ch <- res{out, err}
	}()
	select {
	case r := <-ch:
		return r.out, r.err
	case <-time.After(m.maxExec):
		return "", errors.New("command execution timeout: " + name)
	}
}
