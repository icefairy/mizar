package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"mizar/internal/mcp"
)

// MCPCallFn 调用外部 MCP server 的工具：
// mcp_call(server, tool, argsJSON) -> 结果文本。
// server 为 MCP 配置中的服务器名（见 config.mcp_servers）。
// argsJSON 可为空字符串（无参数）。
type MCPCallFn func(server, tool, argsJSON string) (string, error)

// MCPServerConf 单个 MCP server 的配置。
// 二选一：
//   - Command/Args: stdio 传输（子进程），如 npx -y @modelcontextprotocol/server-sqlite
//   - URL: HTTP(streamable) 传输，如 http://127.0.0.1:9000/mcp
type MCPServerConf struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
	// Timeout 单次调用超时（秒），默认 30。
	Timeout int `json:"timeout,omitempty"`
}

// MCPRegistry 持有已初始化的 MCP server 连接（惰性创建）。
type MCPRegistry struct {
	mu      sync.Mutex
	conf    []MCPServerConf
	clients map[string]*mcpClient
	timeout time.Duration
}

type mcpClient struct {
	client *mcp.Client
	tr     mcp.Transport
}

// NewMCPRegistry 用配置创建注册表。nil/空配置时 mcp_call 报"未配置"。
func NewMCPRegistry(conf []MCPServerConf) *MCPRegistry {
	return &MCPRegistry{conf: conf, clients: make(map[string]*mcpClient), timeout: 30 * time.Second}
}

// Close 关闭全部已建立的连接。
func (r *MCPRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clients {
		c.tr.Close()
	}
	r.clients = nil
}

// CallFn 返回可直接装配进 HostFuncs.MCPCall 的调用函数。
// 每次调用解析 argsJSON（对象）→ 按名查 server → 惰性初始化 → CallTool。
func (r *MCPRegistry) CallFn() MCPCallFn {
	return func(server, tool, argsJSON string) (string, error) {
		if r == nil || len(r.conf) == 0 {
			return "", fmt.Errorf("mcp_call: 未配置任何 MCP server（config.json 的 mcp_servers）")
		}
		var args map[string]any
		if strings.TrimSpace(argsJSON) != "" {
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", fmt.Errorf("mcp_call: argsJSON 非法: %w", err)
			}
		}
		if tool == "" {
			return "", fmt.Errorf("mcp_call: 工具名不能为空")
		}
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()

		c, err := r.get(server, ctx)
		if err != nil {
			return "", err
		}
		res, err := c.client.CallTool(ctx, c.tr, tool, args)
		if err != nil {
			return "", fmt.Errorf("mcp_call %s/%s: %w", server, tool, err)
		}
		if res.IsError {
			return "", fmt.Errorf("mcp_call %s/%s: server error: %s", server, tool, res.Text())
		}
		return res.Text(), nil
	}
}

// get 惰性创建 server 连接（并发安全）。
func (r *MCPRegistry) get(name string, ctx context.Context) (*mcpClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[name]; ok {
		return c, nil
	}
	var conf *MCPServerConf
	for i := range r.conf {
		if r.conf[i].Name == name {
			conf = &r.conf[i]
			break
		}
	}
	if conf == nil {
		return nil, fmt.Errorf("mcp_call: 未找到 server %q（可用: %s）", name, r.names())
	}
	client := mcp.NewClient(name, "mizar", "0.1.0")
	var tr mcp.Transport
	var err error
	if conf.URL != "" {
		tr = mcp.NewHTTPTransport(conf.URL, r.timeout)
	} else if conf.Command != "" {
		cmdArgs := append([]string{}, conf.Args...)
		tr, err = mcp.NewStdioTransport(ctx, conf.Command, cmdArgs...)
		if err != nil {
			return nil, fmt.Errorf("mcp_call: 启动 %s: %w", name, err)
		}
	} else {
		return nil, fmt.Errorf("mcp_call: server %q 未配置 command 或 url", name)
	}
	if err := client.Initialize(ctx, tr); err != nil {
		tr.Close()
		return nil, fmt.Errorf("mcp_call: 握手 %s: %w", name, err)
	}
	c := &mcpClient{client: client, tr: tr}
	r.clients[name] = c
	return c, nil
}

func (r *MCPRegistry) names() string {
	var names []string
	for _, c := range r.conf {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}
