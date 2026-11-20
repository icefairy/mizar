package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mizar/internal/engine"
)

// MCP 配置独立存放于 ~/.mizar/mcp.json：与 LLM 渠道配置（config.json）隔离，
// 改错 MCP 配置不会再连累 base_url / api_key / model。
//
// 兼容三种写法（便于从其它客户端的配置直接拷贝）：
//
//	[{"name":"redis","command":"npx","args":["-y","redis-mcp"]}]
//	{"mcp_servers":[{"name":"redis","command":"npx"}]}
//	{"mcpServers":{"redis":{"command":"npx","args":["-y","redis-mcp"]}}}
//
// 另外仍兼容旧位置：mcp.json 不存在时，回退读取 config.json 的 mcp_servers。
// 首次 /mcp add 会把旧位置的内容迁移过来并清空旧字段。

// MCPPath MCP 独立配置文件路径（~/.mizar/mcp.json）。
func MCPPath() string {
	return filepath.Join(ConfigDir(), "mcp.json")
}

// mcpFile mcp.json 的内容。
type mcpFile struct {
	Servers []engine.MCPServerConf `json:"mcp_servers"`
}

// UnmarshalJSON 兼容「顶层数组 / {mcp_servers:[...]} / 以名字为键的 map」三种形态。
func (f *mcpFile) UnmarshalJSON(data []byte) error {
	if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		return json.Unmarshal(data, &f.Servers)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("MCP 配置需为数组或对象: %w", err)
	}
	for _, key := range []string{"mcp_servers", "mcpServers", "servers"} {
		if raw, ok := obj[key]; ok {
			return decodeMCPServers(raw, &f.Servers)
		}
	}
	// 纯 map 形态：{"redis": {"command": "..."}}
	return decodeMCPServerMap(obj, &f.Servers)
}

// decodeMCPServers 解析 server 列表：数组，或以名字为键的对象。
func decodeMCPServers(raw json.RawMessage, out *[]engine.MCPServerConf) error {
	var arr []engine.MCPServerConf
	if err := json.Unmarshal(raw, &arr); err == nil {
		*out = arr
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("mcp_servers 需为数组或以名字为键的对象")
	}
	return decodeMCPServerMap(m, out)
}

// decodeMCPServerMap 把 {"名字": {...}} 转成列表（名字键会补进 name 字段，
// 条目里显式写的 name 优先），排序保证输出稳定。
func decodeMCPServerMap(m map[string]json.RawMessage, out *[]engine.MCPServerConf) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	list := make([]engine.MCPServerConf, 0, len(keys))
	for _, k := range keys {
		raw := strings.TrimSpace(string(m[k]))
		if !strings.HasPrefix(raw, "{") {
			continue // 跳过 version 之类的非 server 键
		}
		var s engine.MCPServerConf
		if err := json.Unmarshal(m[k], &s); err != nil {
			return fmt.Errorf("mcp server %q: %w", k, err)
		}
		if s.Name == "" {
			s.Name = k
		}
		list = append(list, s)
	}
	*out = list
	return nil
}

// LoadMCPServers 读取 MCP server 列表，返回配置来源（便于展示与报错）。
// 优先 mcp.json；不存在时回退 config.json 的旧字段 mcp_servers。
// config.json 自身解析失败时视为「无 MCP 配置」，错误由 Load 在启动时报告。
func LoadMCPServers() ([]engine.MCPServerConf, string, error) {
	path := MCPPath()
	data, err := os.ReadFile(path)
	if err == nil {
		var f mcpFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, path, fmt.Errorf("解析 %s: %w", path, err)
		}
		return f.Servers, path, nil
	}
	if !os.IsNotExist(err) {
		return nil, path, fmt.Errorf("读取 %s: %w", path, err)
	}
	cfg, lerr := Load(DefaultPath())
	if lerr != nil || cfg == nil || len(cfg.MCPServers) == 0 {
		return nil, path, nil
	}
	return cfg.MCPServers, DefaultPath(), nil
}

// SaveMCPServers 写入 mcp.json，返回写入路径。
func SaveMCPServers(list []engine.MCPServerConf) (string, error) {
	path := MCPPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, fmt.Errorf("mkdir: %w", err)
	}
	if list == nil {
		list = []engine.MCPServerConf{}
	}
	data, err := json.MarshalIndent(mcpFile{Servers: list}, "", "  ")
	if err != nil {
		return path, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return path, err
	}
	return path, nil
}

// ClearLegacyMCPServers 清掉 config.json 里旧位置的 mcp_servers
// （迁移到 mcp.json 后避免两份配置并存产生歧义）。返回是否发生了清理。
// config.json 无法解析时不动它，交由 Load/LoadForWrite 报错。
func ClearLegacyMCPServers() bool {
	cfg, err := LoadForWrite(DefaultPath())
	if err != nil || len(cfg.MCPServers) == 0 {
		return false
	}
	cfg.MCPServers = nil
	return Save(DefaultPath(), cfg) == nil
}
