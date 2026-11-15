package main

import (
	"fmt"
	"sort"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// runPluginsCommand 统一的插件管理命令（合并原 /list_plugins、/enable_plugin、
// /disable_plugin、/reload_plugins 四个分散命令）。
//
// 用法：
//
//	/plugins                 列出（同旧 /list_plugins）
//	/plugins enable <名>     启用（同旧 /enable_plugin）
//	/plugins disable <名>    禁用（同旧 /disable_plugin）
//	/plugins reload          重载全部（同旧 /reload_plugins）
//	/plugins info <名>       查看单个插件的详情
//
// 旧命令名注册为别名、走同一个函数，所以行为完全一致。
func runPluginsCommand(pm *plugins.Manager, a *agent.Agent) func(string) (string, error) {
	return pluginsDispatch(pm, a, "")
}

// runPluginsAlias 注册旧命令名的兼容别名：把旧命令隐含的子命令补上。
//
//	/list_plugins     → 列出（无子命令）
//	/enable_plugin X  → enable X
//	/disable_plugin X → disable X
//	/reload_plugins   → reload
//
// 注意不能直接把别名指到 runPluginsCommand：旧命令的参数是「插件名」，
// 而 /plugins 的参数是「子命令 + 插件名」，两者位置不同，必须补上子命令。
func runPluginsAlias(pm *plugins.Manager, a *agent.Agent, sub string) func(string) (string, error) {
	return func(args string) (string, error) {
		arg := strings.TrimSpace(args)
		if sub == "" {
			return pluginsDispatch(pm, a, "")(arg)
		}
		// 旧命令的 reload 不接受参数
		if sub == "reload" {
			return pluginsDispatch(pm, a, "reload")("")
		}
		return pluginsDispatch(pm, a, sub)(arg)
	}
}

// pluginsDispatch 以 defaultSub 为「无显式子命令时的默认动作」执行。
// defaultSub 为空表示需要用户显式给子命令（/plugins 本身）。
func pluginsDispatch(pm *plugins.Manager, a *agent.Agent, defaultSub string) func(string) (string, error) {
	return func(args string) (string, error) {
		trimmed := strings.TrimSpace(args)
		fields := strings.Fields(trimmed)
		sub := defaultSub
		rest := trimmed
		if len(fields) > 0 {
			first := strings.ToLower(fields[0])
			// 只有在没有强制默认子命令时，才把第一个词当子命令
			if defaultSub == "" && isPluginSubcommand(first) {
				sub = first
				rest = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			} else if defaultSub == "" && first != "" {
				// /plugins <名> → 视为 info（顺手给个直觉行为）
				sub = "info"
				rest = trimmed
			}
		}

		switch sub {
		case "", "list", "ls":
			return listPlugins(pm), nil
		case "enable", "on":
			return enablePlugin(pm, a, rest)
		case "disable", "off":
			return disablePlugin(pm, a, rest)
		case "reload", "rl":
			return reloadPlugins(pm, a), nil
		case "info", "show":
			return pluginInfo(pm, rest)
		default:
			return "", fmt.Errorf("用法: /plugins ｜ /plugins enable <名> ｜ /plugins disable <名> ｜ /plugins reload ｜ /plugins info <名>")
		}
	}
}

// isPluginSubcommand 判断是否已知子命令（用于区分「/plugins enable」和「/plugins memory.ts」）。
func isPluginSubcommand(s string) bool {
	switch s {
	case "", "list", "ls", "enable", "on", "disable", "off", "reload", "rl", "info", "show":
		return true
	}
	return false
}

// listPlugins 列出插件及状态。
func listPlugins(pm *plugins.Manager) string {
	all := pm.PluginFiles()
	if len(all) == 0 {
		return fmt.Sprintf("插件目录 %s 无插件", pm.Dir())
	}
	sort.Strings(all)
	loaded := map[string]bool{}
	for _, f := range pm.LoadedPlugins() {
		loaded[f] = true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "插件目录: %s\n共 %d 个:\n", pm.Dir(), len(all))
	for _, f := range all {
		status := "✅ 已加载"
		if pm.PluginDisabled(f) {
			status = "⛔ 已禁用"
		} else if !loaded[f] {
			status = "⚠️ 未加载（加载失败？）"
		}
		fmt.Fprintf(&sb, "  %s %s\n", status, f)
	}
	sb.WriteString("\n子命令: enable <名> · disable <名> · reload · info <名>")
	return sb.String()
}

// enablePlugin 启用（取消禁用并加载）。
func enablePlugin(pm *plugins.Manager, a *agent.Agent, arg string) (string, error) {
	name := resolvePluginName(pm, arg)
	if name == "" {
		return "用法: /plugins enable <文件名，可省略 .ts>（/plugins 查看清单）", nil
	}
	if !pm.PluginDisabled(name) {
		return fmt.Sprintf("插件 %s 本就处于启用状态", name), nil
	}
	pm.SetPluginDisabled(name, false)
	summary := reloadPlugins(pm, a)
	return fmt.Sprintf("✅ 已启用 %s\n%s", name, summary), nil
}

// disablePlugin 禁用（卸载并持久化）。
func disablePlugin(pm *plugins.Manager, a *agent.Agent, arg string) (string, error) {
	name := resolvePluginName(pm, arg)
	if name == "" {
		return "用法: /plugins disable <文件名，可省略 .ts>（/plugins 查看清单）", nil
	}
	if pm.PluginDisabled(name) {
		return fmt.Sprintf("插件 %s 本就处于禁用状态", name), nil
	}
	pm.SetPluginDisabled(name, true)
	// 同步命令注册表（该插件导出的命令需移除）
	cmds := make([]agent.Command, 0, len(pm.Commands()))
	for _, c := range pm.Commands() {
		cc := c
		cmds = append(cmds, agent.Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
	}
	a.Commands.SyncFromPlugins(cmds)
	a.ReloadTools()
	return fmt.Sprintf("⛔ 已禁用 %s（已卸载，配置已持久化）", name), nil
}

// pluginInfo 查看单个插件详情：状态 + 它导出的工具/命令。
func pluginInfo(pm *plugins.Manager, arg string) (string, error) {
	name := resolvePluginName(pm, arg)
	if name == "" {
		return "用法: /plugins info <文件名，可省略 .ts>", nil
	}
	status := "✅ 已加载"
	if pm.PluginDisabled(name) {
		status = "⛔ 已禁用"
	} else {
		loaded := false
		for _, f := range pm.LoadedPlugins() {
			if f == name {
				loaded = true
				break
			}
		}
		if !loaded {
			status = "⚠️ 未加载"
		}
	}
	var tools, cmds []string
	for _, t := range pm.Tools() {
		if t.PluginFile == name {
			tools = append(tools, t.Name)
		}
	}
	for _, c := range pm.Commands() {
		if c.PluginFile == name {
			cmds = append(cmds, "/"+c.Name)
		}
	}
	sort.Strings(tools)
	sort.Strings(cmds)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s  %s\n", status, name)
	if len(tools) > 0 {
		fmt.Fprintf(&sb, "  工具 %d: %s\n", len(tools), strings.Join(tools, ", "))
	} else {
		sb.WriteString("  工具: （无）\n")
	}
	if len(cmds) > 0 {
		fmt.Fprintf(&sb, "  命令 %d: %s\n", len(cmds), strings.Join(cmds, ", "))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
