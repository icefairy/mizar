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
}

// WebAuth Web UI 登录鉴权配置。
type WebAuth struct {
	Enabled  bool   // 是否启用 Web UI（默认 true）
	Username string // 登录用户名（空=admin）
	Password string // 登录密码（空=不校验密码，仅本地开发用）
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
