// Package server 提供 Mizar 的 Server 模式：
// OpenAI ChatCompletion 兼容 HTTP + JSON-RPC 2.0 + WebSocket + 动态开关。
package server

import "sync/atomic"

// Services 各服务开关状态（原子，支持运行时动态切换）。
type Services struct {
	HTTP    atomic.Bool // OpenAI 兼容 HTTP 端点 (/v1/chat/completions)
	JSONRPC atomic.Bool // JSON-RPC 2.0 端点 (/rpc)
	WS      atomic.Bool // WebSocket 端点 (/ws)
}

// Config Server 配置。
type Config struct {
	Addr     string   // 监听地址，如 ":3003"
	Token    string   // Bearer 认证 token（空=不认证，内网默认建议设置）
	Services Services // 各服务开关

	// Web Web UI：浏览器界面 + 会话登录（与 Bearer Token 独立）。
	Web WebAuth

	// Version 程序版本号（v0.7.6 之类），Web UI 标题展示用（空=不展示）。
	Version string

	// MaxConcurrentRuns 跨会话并发上限（多会话真并发）。0 或负数 = 不限。
	// 对齐 Hermes gateway 的 max_concurrent_sessions：默认不限，
	// 想限制上游 LLM 压力时设成 2~4。超过上限的请求返回 409，前端转排队。
	MaxConcurrentRuns int

	// MCPStats 可选：返回（已配置数, 已连接数），供 Web UI 状态栏展示 MCP 状态。
	// 由调用方注入（MCP 注册表在 main 包），nil 时 Web UI 显示「-」。
	MCPStats func() (configured, connected int)
}

// WebAuth Web UI 登录鉴权 + 前端资源配置。
type WebAuth struct {
	Enabled  bool   // 是否启用 Web UI（默认 true）
	Username string // 登录用户名（空=admin）
	Password string // 登录密码（空=不校验密码，仅本地开发用）
	// FrontendDir 自定义前端目录（P0 前端外部化）。
	// 优先级：显式配置 > ~/.mizar/webui/ > 二进制内嵌 embed（fallback）。
	// 目录存在且非空时服务它——改 HTML/JS/CSS 不必重新编译二进制；
	// 目录不存在或为空时自动回退 embed，行为与旧版完全一致。
	FrontendDir string
}

// NewConfig 默认配置：三服务全开。
func NewConfig(addr, token string) *Config {
	c := &Config{Addr: addr, Token: token}
	c.Services.HTTP.Store(true)
	c.Services.JSONRPC.Store(true)
	c.Services.WS.Store(true)
	return c
}

// SetAll 批量设置开关（启动时用）。
func (c *Config) SetAll(httpOn, rpcOn, wsOn bool) {
	c.Services.HTTP.Store(httpOn)
	c.Services.JSONRPC.Store(rpcOn)
	c.Services.WS.Store(wsOn)
}
