package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// 建一个带两个插件的测试环境。
func pluginTestEnv(t *testing.T) (*plugins.Manager, *agent.Agent, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	write("pa.ts", `/** pa 工具 */
export function tool_pa(args: string): string { return "pa"; }`)
	write("pb.ts", `/** pb 工具 */
export function tool_pb(args: string): string { return "pb"; }`)

	pm := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	pm.LoadDisabledPlugins()
	if _, failed := pm.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}
	a := agent.New(nil, pm)
	return pm, a, dir
}

func TestPluginsList(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)
	out, err := runPluginsCommand(pm, a)("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pa.ts") || !strings.Contains(out, "pb.ts") {
		t.Errorf("应列出两个插件:\n%s", out)
	}
	if !strings.Contains(out, "子命令") {
		t.Errorf("应提示子命令:\n%s", out)
	}
}

func TestPluginsDisableAndEnable(t *testing.T) {
	pm, a, dir := pluginTestEnv(t)
	run := runPluginsCommand(pm, a)

	out, err := run("disable pa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已禁用") {
		t.Errorf("disable 输出异常: %s", out)
	}
	if !pm.PluginDisabled("pa.ts") {
		t.Error("pa.ts 应被标记禁用")
	}
	// 持久化
	b, _ := os.ReadFile(filepath.Join(dir, ".disabled.json"))
	if !strings.Contains(string(b), "pa.ts") {
		t.Errorf("禁用状态应持久化: %s", b)
	}

	out, err = run("enable pa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已启用") {
		t.Errorf("enable 输出异常: %s", out)
	}
	if pm.PluginDisabled("pa.ts") {
		t.Error("pa.ts 应已启用")
	}
}

func TestPluginsReload(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)
	out, err := runPluginsCommand(pm, a)("reload")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "重载") && !strings.Contains(out, "✓") {
		t.Errorf("reload 输出异常: %s", out)
	}
}

func TestPluginsInfo(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)
	out, err := runPluginsCommand(pm, a)("info pa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pa.ts") {
		t.Errorf("应显示插件名: %s", out)
	}
	if !strings.Contains(out, "tool_pa") && !strings.Contains(out, "pa") {
		t.Errorf("应列出该插件的工具: %s", out)
	}
}

// TestPluginsAliases 旧命令名必须保持可用（参数位置不同，靠 runPluginsAlias 补子命令）。
func TestPluginsAliases(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)

	// /list_plugins（无子命令）
	out, err := runPluginsAlias(pm, a, "")("")
	if err != nil || !strings.Contains(out, "pa.ts") {
		t.Errorf("/list_plugins 应列出插件: %v %s", err, out)
	}
	// /disable_plugin pa
	out, err = runPluginsAlias(pm, a, "disable")("pa")
	if err != nil || !strings.Contains(out, "已禁用") {
		t.Errorf("/disable_plugin pa 应禁用: %v %s", err, out)
	}
	if !pm.PluginDisabled("pa.ts") {
		t.Error("别名 disable 应真的生效")
	}
	// /enable_plugin pa
	out, err = runPluginsAlias(pm, a, "enable")("pa")
	if err != nil || !strings.Contains(out, "已启用") {
		t.Errorf("/enable_plugin pa 应启用: %v %s", err, out)
	}
	// /reload_plugins
	out, err = runPluginsAlias(pm, a, "reload")("")
	if err != nil || out == "" {
		t.Errorf("/reload_plugins 应工作: %v", err)
	}
}

// TestPluginsBareNameIsInfo /plugins <名> 直接看详情（直觉行为）。
func TestPluginsBareNameIsInfo(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)
	out, err := runPluginsCommand(pm, a)("pa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pa.ts") {
		t.Errorf("/plugins <名> 应等价 info: %s", out)
	}
}

func TestPluginsUnknownSubcommand(t *testing.T) {
	pm, a, _ := pluginTestEnv(t)
	// "enable" 无参数 → 给用法提示而不是崩
	out, err := runPluginsCommand(pm, a)("enable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "用法") {
		t.Errorf("缺参数应给用法提示: %s", out)
	}
}
