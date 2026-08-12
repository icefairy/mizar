package server

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"mizar/internal/agent"
)

// ============================================================================
// WebSocket 端点（/ws）
//
// 第三方通过 WS 长连接接入，发送 JSON-RPC 2.0 请求；同时 Agent 循环的
// 中间事件（工具调用/LLM 响应/纠正确认）实时推送给所有活跃客户端——这就是
// "看到 Agent 输出不对立刻纠正"的实时通道。
//
// 客户端 → 服务端：JSON-RPC 2.0 请求（同 /rpc 的方法集）
// 服务端 → 客户端：
//   {"type":"event","event":"llm_response","step":0,"content":"..."}
//   {"type":"event","event":"tool_call","step":0,"tool":"calc","args":"..."}
//   {"type":"event","event":"tool_result","step":0,"tool":"calc","result":"..."}
//   {"type":"result","id":<请求id>,"result":{...}}
//
// 注意：Agent 是单实例，钩子在 Server 创建时挂一次（广播模式），
// 所有连接共享中间输出流。多个客户端都会收到同一事件流。
// ============================================================================

type wsUpgrader struct {
	upgrader websocket.Upgrader
}

func newWSUpgrader(token string) *wsUpgrader {
	return &wsUpgrader{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true }, // 内网默认放开，认证由 Bearer 处理
		},
	}
}

// wsClient 一个 WS 连接。
type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex // 保护并发写
}

func (c *wsClient) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

// hub 管理活跃 WS 连接并广播事件。
type hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func newHub() *hub {
	return &hub{clients: map[*wsClient]struct{}{}}
}

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// broadcast 向所有活跃客户端推送事件（忽略已断开连接的发送错误）。
func (h *hub) broadcast(v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if err := c.send(v); err != nil {
			// 发送失败：移除（客户端已断）
			delete(h.clients, c)
			_ = c.conn.Close()
		}
	}
}

// attachHooks 在 Server 创建时挂一次广播钩子（观察者，不改消息流）。
func (s *Server) attachHooks() {
	if s.agent.Hooks == nil {
		s.agent.Hooks = agent.NewHooks()
	}
	h := s.agent.Hooks
	h.OnLLMResponse(func(ctx *agent.HookContext) error {
		s.hub.broadcast(map[string]any{"type": "event", "event": "llm_response", "step": ctx.Step, "content": ctx.Reply})
		return nil
	})
	h.OnToolCall(func(ctx *agent.HookContext) error {
		s.hub.broadcast(map[string]any{"type": "event", "event": "tool_call", "step": ctx.Step, "tool": ctx.Tool, "args": ctx.Args})
		return nil
	})
	h.OnToolResult(func(ctx *agent.HookContext) error {
		s.hub.broadcast(map[string]any{"type": "event", "event": "tool_result", "step": ctx.Step, "tool": ctx.Tool, "result": ctx.Result})
		return nil
	})
}

// handleWS GET /ws。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Services.WS.Load() {
		http.Error(w, "ws service disabled", http.StatusServiceUnavailable)
		return
	}
	conn, err := s.wsUp.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logf("ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	client := &wsClient{conn: conn}
	s.hub.add(client)
	defer s.hub.remove(client)
	s.logf("ws connected: %s (active=%d)", r.RemoteAddr, s.hub.active())

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var req rpcRequest
		if err := json.Unmarshal(data, &req); err != nil {
			client.send(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: rpcParseError, Message: "parse error"}})
			continue
		}
		if req.JSONRPC != "2.0" {
			client.send(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "invalid request"}})
			continue
		}
		// agent.run 异步执行：立即返回（不阻塞读循环），结果通过事件推回。
		// 这是 steer 能实时纠正的关键——run 在后台跑，steer/abort 走独立
		// 通道立即生效（Agent.Steer/Abort 线程安全）。
		if req.Method == "agent.run" {
			go func(req rpcRequest) {
				resp := s.dispatch(&req)
				client.send(resp)
			}(req)
			continue
		}
		// steer/abort 先回 ack，让客户端立刻确认收到
		if req.Method == "agent.steer" || req.Method == "agent.abort" {
			client.send(map[string]any{"type": "ack", "method": req.Method, "id": string(req.ID)})
		}
		resp := s.dispatch(&req)
		client.send(resp)
	}
}

// active 当前活跃连接数（日志用）。
func (h *hub) active() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
