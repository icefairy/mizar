package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
)

// MCP 配置文件接受多种写法：数组、mcp_servers、mcpServers、纯 map，
// 且 timeout 允许字符串、args 允许空格分隔字符串。
func TestMCPFileFormats(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // 期望的 name
	}{
		{"array", `[{"name":"redis","command":"npx"}]`, "redis"},
		{"mcp_servers", `{"mcp_servers":[{"name":"redis","command":"npx"}]}`, "redis"},
		{"mcpServers map", `{"mcpServers":{"redis":{"command":"npx"}}}`, "redis"},
		{"bare map", `{"redis":{"command":"npx"}}`, "redis"},
		{"empty", `{}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f mcpFile
			if err := json.Unmarshal([]byte(c.in), &f); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if c.want == "" {
				if len(f.Servers) != 0 {
					t.Fatalf("应为空, got %+v", f.Servers)
				}
				return
			}
			if len(f.Servers) != 1 || f.Servers[0].Name != c.want {
				t.Fatalf("got %+v", f.Servers)
			}
		})
	}
}

// timeout 写字符串、args 写空格分隔字符串都不应报错（手写配置常见）。
func TestMCPFileLooseTypes(t *testing.T) {
	var f mcpFile
	in := `{"mcp_servers":[{"name":"a","command":"npx","args":"-y pkg","timeout":"45s"},
	                      {"name":"b","url":"http://x/mcp","timeout":"2m"}]}`
	if err := json.Unmarshal([]byte(in), &f); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(f.Servers) != 2 {
		t.Fatalf("got %+v", f.Servers)
	}
	if f.Servers[0].Timeout != 45 || strings.Join(f.Servers[0].Args, " ") != "-y pkg" {
		t.Fatalf("a: %+v", f.Servers[0])
	}
	if f.Servers[1].Timeout != 120 {
		t.Fatalf("b timeout 应为 120s: %+v", f.Servers[1])
	}
}

// mcp.json 与 config.json 相互隔离：mcp.json 写坏不影响渠道配置读取。
func TestLoadMCPServersIsolatedFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".mizar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"base_url":"http://chan/v1","api_key":"sk-x","model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcp_servers": oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadMCPServers(); err == nil {
		t.Fatal("mcp.json 语法错误应返回错误")
	}
	cfg, err := Load(DefaultPath())
	if err != nil {
		t.Fatalf("config.json 不应受影响: %v", err)
	}
	if cfg.BaseURL != "http://chan/v1" || cfg.Model != "m" {
		t.Fatalf("渠道配置应完好: %+v", cfg)
	}
}

// mcp.json 不存在时回退 config.json 的旧字段；存在时以 mcp.json 为准。
func TestLoadMCPServersLegacyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".mizar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"base_url":"http://chan/v1","mcp_servers":[{"name":"old","command":"x"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, src, err := LoadMCPServers()
	if err != nil {
		t.Fatalf("LoadMCPServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "old" || src != DefaultPath() {
		t.Fatalf("应回退旧字段: %+v src=%s", servers, src)
	}

	if _, err := SaveMCPServers([]engine.MCPServerConf{{Name: "new", Command: "x"}}); err != nil {
		t.Fatalf("SaveMCPServers: %v", err)
	}
	servers, src, err = LoadMCPServers()
	if err != nil {
		t.Fatalf("LoadMCPServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "new" || src != MCPPath() {
		t.Fatalf("应以 mcp.json 为准: %+v src=%s", servers, src)
	}

	// 迁移后清空旧字段，避免两份配置并存
	if !ClearLegacyMCPServers() {
		t.Fatal("应清理旧字段")
	}
	cfg, _ := Load(DefaultPath())
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("旧字段应已清空: %+v", cfg.MCPServers)
	}
}

// 单个字段类型写错时，其余字段照常生效；但不允许写回（会丢掉坏字段的内容）。
func TestLoadToleratesBadField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	bad := `{"base_url":"http://chan/v1","model":"m","max_steps":"many"}`
	if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("个别字段失败不应返回错误: %v", err)
	}
	if c.BaseURL != "http://chan/v1" || c.Model != "m" {
		t.Fatalf("其余字段应生效: %+v", c)
	}
	if len(c.Warnings) == 0 || !strings.Contains(c.Warnings[0], "max_steps") {
		t.Fatalf("应记录 warning: %+v", c.Warnings)
	}
	if _, err := LoadForWrite(p); err == nil {
		t.Fatal("有坏字段时应拒绝写回")
	}
}
