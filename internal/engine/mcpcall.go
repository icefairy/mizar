package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"mizar/internal/mcp"
)

// MCPCallFn 调用外部 MCP server 的工具：
// mcp_call(server, tool, argsJSON) -> 结果文本。
// server 为 MCP 配置中的服务器名（见 ~/.mizar/mcp.json 的 mcp_servers）。
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
	// Headers HTTP 传输附加请求头（如 Authorization: Bearer ...）。
	Headers map[string]string `json:"headers,omitempty"`
	// Timeout 单次调用超时（秒），默认 30。
	Timeout int `json:"timeout,omitempty"`
}

// UnmarshalJSON 宽容解析单条 MCP 配置。手写配置时下面两种写法都很常见，
// 若按严格类型解析会抛出错误并连累整份配置文件：
//   - timeout 写成字符串（"30" / "30s" / "2m"）
//   - args 写成以空格分隔的字符串
func (s *MCPServerConf) UnmarshalJSON(data []byte) error {
	type alias struct {
		Name    string            `json:"name"`
		Command string            `json:"command"`
		Args    json.RawMessage   `json:"args"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Timeout json.RawMessage   `json:"timeout"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	s.Name, s.Command, s.URL, s.Headers = a.Name, a.Command, a.URL, a.Headers
	s.Args, s.Timeout = nil, 0
	if len(a.Args) > 0 && string(a.Args) != "null" {
		var arr []string
		if err := json.Unmarshal(a.Args, &arr); err == nil {
			s.Args = arr
		} else {
			var str string
			if err := json.Unmarshal(a.Args, &str); err != nil {
				return fmt.Errorf("mcp server %q 的 args 需为字符串数组或空格分隔的字符串", a.Name)
			}
			s.Args = strings.Fields(str)
		}
	}
	if len(a.Timeout) > 0 && string(a.Timeout) != "null" {
		n, err := parseTimeoutSeconds(a.Timeout)
		if err != nil {
			return fmt.Errorf("mcp server %q: %w", a.Name, err)
		}
		s.Timeout = n
	}
	return nil
}

// parseTimeoutSeconds 解析 timeout：数字按秒；字符串支持 "30"、"30s"、"2m"、"1h"。
func parseTimeoutSeconds(raw json.RawMessage) (int, error) {
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil {
		if num < 0 {
			return 0, fmt.Errorf("timeout 不能为负数: %v", num)
		}
		return int(num), nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return 0, fmt.Errorf("timeout 需为秒数或时长字符串（如 30 / \"30s\"）")
	}
	str = strings.TrimSpace(strings.ToLower(str))
	if str == "" {
		return 0, nil
	}
	mult := time.Second
	switch str[len(str)-1] {
	case 's':
		str = strings.TrimSpace(str[:len(str)-1])
	case 'm':
		str, mult = strings.TrimSpace(str[:len(str)-1]), time.Minute
	case 'h':
		str, mult = strings.TrimSpace(str[:len(str)-1]), time.Hour
	}
	f, err := strconv.ParseFloat(str, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("timeout 无法解析: %q", str)
	}
	return int(f * float64(mult) / float64(time.Second)), nil
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

// Reload 用新配置替换 server 列表：关闭所有已建立的连接（杀掉 stdio 子进程），
// 下次 mcp_call 时按新配置重新握手。
//
// 这是「MCP server 热加载」的实现要点：Zig 编译出的二进制本身没法在进程内被替换，
// 但**杀掉旧子进程、按同一路径重新拉起**就能吃到新二进制——等价于热更新。
// 因此改完 Zig 源码后：zig build-exe 覆盖旧文件 → /mcp reload → 立即生效，无需重启 Mizar。
//
// 若某次 get() 正在建立连接（持锁），Reload 会等它完成；连接超时默认 30s，
// 最坏情况等到超时，不会死锁。
func (r *MCPRegistry) Reload(conf []MCPServerConf) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clients {
		c.tr.Close()
	}
	r.clients = make(map[string]*mcpClient)
	r.conf = conf
}

// Conf 返回当前生效的配置快照。
func (r *MCPRegistry) Conf() []MCPServerConf {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MCPServerConf, len(r.conf))
	copy(out, r.conf)
	return out
}

// Stats 返回（配置数, 已建立连接数）。
// 连接是惰性的：只有真正被 mcp_call 用过（握手成功）的 server 才计入 connected，
// 状态栏据此展示「已连/配置」而不是「配了几个」。
func (r *MCPRegistry) Stats() (configured, connected int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conf), len(r.clients)
}

// Connected 判断某个 server 当前是否已建立连接（已握手）。
func (r *MCPRegistry) Connected(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.clients[name]
	return ok
}

// CallFn 返回可直接装配进 HostFuncs.MCPCall 的调用函数。
// 每次调用解析 argsJSON（对象）→ 按名查 server → 惰性初始化 → CallTool。
func (r *MCPRegistry) CallFn() MCPCallFn {
	return func(server, tool, argsJSON string) (string, error) {
		if r == nil || len(r.conf) == 0 {
			return "", fmt.Errorf("mcp_call: 未配置任何 MCP server（~/.mizar/mcp.json 的 mcp_servers）")
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
		tr = mcp.NewHTTPTransportWithHeaders(conf.URL, r.timeout, conf.Headers)
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
