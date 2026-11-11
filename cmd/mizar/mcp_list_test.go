package main

import (
	"strings"
	"testing"

	"mizar/internal/config"
	"mizar/internal/engine"
)

// /mcp list 必须列出每个已配置 server 的传输方式、地址与当前连接状态。
func TestMCPListText(t *testing.T) {
	runtimeMCPRegistry = engine.NewMCPRegistry(nil)
	defer func() { runtimeMCPRegistry = nil }()

	cfg := &config.Config{MCPServers: []engine.MCPServerConf{
		{Name: "ones", URL: "http://127.0.0.1:3001/mcp"},
		{Name: "sqlite", Command: "npx", Args: []string{"-y", "server-sqlite"}, Timeout: 10},
	}}
	out := mcpListText(cfg)
	for _, want := range []string{
		"配置 2 个", "ones", "[http]", "http://127.0.0.1:3001/mcp",
		"sqlite", "[stdio]", "npx", "args: -y server-sqlite", "timeout=10s",
		"未连接", "/mcp add", "/mcp test", "/mcp reload",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q：\n%s", want, out)
		}
	}
	// 连接是惰性的：未握手前不应误报已连接
	if strings.Contains(out, "已连接") {
		t.Fatalf("未握手时不应显示已连接：\n%s", out)
	}
}

// 无配置时给出添加入口而不是空白输出。
func TestMCPListTextEmpty(t *testing.T) {
	runtimeMCPRegistry = engine.NewMCPRegistry(nil)
	defer func() { runtimeMCPRegistry = nil }()
	out := mcpListText(&config.Config{})
	if !strings.Contains(out, "未配置") || !strings.Contains(out, "/mcp add") {
		t.Fatalf("空配置应提示添加方式：\n%s", out)
	}
}

// 状态栏统计：无注册表时返回 0/0 且不 panic。
func TestRuntimeMCPStatsNil(t *testing.T) {
	runtimeMCPRegistry = nil
	if a, b := runtimeMCPStats(); a != 0 || b != 0 {
		t.Fatalf("got %d/%d, want 0/0", a, b)
	}
	if runtimeMCPConnected("x") {
		t.Fatal("无注册表时不应报告已连接")
	}
}
