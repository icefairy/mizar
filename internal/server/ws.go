package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"mizar/internal/agent"
)

// wsWriteTimeout 单条消息写超时。
//
// 手机浏览器切后台后，页面被冻结、TCP 接收窗口读空，服务端 WriteJSON 会被
// 内核缓冲区堵住（不再返回）。若不设超时，广播就会卡在这条死连接上——
// 更糟的是旧实现是在持 hub 锁的情况下逐条写，一次阻塞会让**所有**客户端
// 的事件流全部停摆，表现为「手机切后台，电脑上也不动了」。
const wsWriteTimeout = 5 * time.Second

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

// wsSendQueue 每个连接的发送队列长度。
//
// 慢客户端（手机切后台、页面被冻结）不能阻塞广播：事件先入队，队列满说明
// 该连接确实收不动了，直接判定为死连接清理掉（前端会用 /web/api/events
// 按 seq 重新补齐，不会丢内容）。
const wsSendQueue = 512

// wsClient 一个 WS 连接（带独立写泵，广播方永不直接写 socket）。
type wsClient struct {
	conn      *websocket.Conn
	out       chan []byte
	sendMu    sync.Mutex // 保护「发送 vs 关闭」竞争（向已关闭 channel 发送会 panic）
	closed    bool
	closeOnce sync.Once
	dead      atomic.Bool
}

func newWSClient(conn *websocket.Conn) *wsClient {
	c := &wsClient{conn: conn, out: make(chan []byte, wsSendQueue)}
	go c.writePump()
	return c
}

// writePump 单 goroutine 顺序发送：保证同一连接的事件顺序，同时把「写阻塞」
// 隔离在这一个连接内（带写超时），完全不影响广播方和其它客户端。
func (c *wsClient) writePump() {
	for b := range c.out {
		_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
			c.dead.Store(true)
			_ = c.conn.Close()
			// 继续排空队列（丢弃），避免广播方 enqueue 时永久阻塞
			for range c.out {
			}
			return
		}
	}
}

// send 序列化入队（非阻塞）。队列满 → 判定死连接。
func (c *wsClient) send(v any) error {
	if c.dead.Load() {
		return errWsDead
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return errWsDead
	}
	select {
	case c.out <- b:
		return nil
	default:
		// 队列满：这个连接已经跟不上了。关掉它，让前端重连后用 seq 补齐。
		c.closed = true
		c.close()
		c.dead.Store(true)
		return errWsSlow
	}
}

func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		c.sendMu.Lock()
		if !c.closed {
			c.closed = true
			close(c.out)
		}
		c.sendMu.Unlock()
		_ = c.conn.Close()
	})
}

var (
	errWsDead = errors.New("ws client closed")
	errWsSlow = errors.New("ws client too slow")
)

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
	delete(h.clients, c)
	h.mu.Unlock()
	c.close()
}

// broadcast 向所有活跃客户端推送事件。
//
// 关键：**只做非阻塞入队，不在这里写 socket**。旧实现在持 hub 锁的情况下
// 逐个 WriteJSON，只要有一个客户端（典型是切到后台的手机）写阻塞，所有
// 客户端的事件流都会被一起卡住——表现为「手机切后台，电脑上也不动了」。
// 现在每个连接有自己的写泵和队列，慢客户端的后果仅限于它自己。
func (h *hub) broadcast(v any) {
	h.mu.Lock()
	targets := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	var dead []*wsClient
	for _, c := range targets {
		if err := c.send(v); err != nil {
			dead = append(dead, c)
		}
	}
	if len(dead) == 0 {
		return
	}
	h.mu.Lock()
	for _, c := range dead {
		delete(h.clients, c)
	}
	h.mu.Unlock()
	for _, c := range dead {
		c.close()
	}
}

// attachHooks 在 Server 创建时挂一次广播钩子（观察者，不改消息流）。
func (s *Server) attachHooks() {
	if s.agent.Hooks == nil {
		s.agent.Hooks = agent.NewHooks()
	}
	h := s.agent.Hooks
	// 事件携带 conversation_id（当前运行的 Web 会话）：前端据此过滤，避免串台。
	h.OnLLMResponse(func(ctx *agent.HookContext) error {
		s.emit(map[string]any{"type": "event", "event": "llm_response", "step": ctx.Step, "content": ctx.Reply, "conversation_id": s.RunConv()})
		return nil
	})
	h.OnToolCall(func(ctx *agent.HookContext) error {
		s.emit(map[string]any{"type": "event", "event": "tool_call", "step": ctx.Step, "tool": ctx.Tool, "args": ctx.Args, "conversation_id": s.RunConv()})
		return nil
	})
	h.OnToolResult(func(ctx *agent.HookContext) error {
		s.emit(map[string]any{"type": "event", "event": "tool_result", "step": ctx.Step, "tool": ctx.Tool, "result": ctx.Result, "conversation_id": s.RunConv()})
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
	client := newWSClient(conn)
	s.hub.add(client)
	defer s.hub.remove(client)
	s.logf("ws connected: %s (active=%d)", r.RemoteAddr, s.hub.active())
	// Web UI 连接建立时先推一条 hello：告知当前运行归属，前端据此判断是否
	// 需要用 /web/api/events 补齐「断线/切后台期间」缺失的事件。
	// 只在 /web/ws 上推——/ws 是对第三方开放的 JSON-RPC 通道，不能插入
	// 非 JSON-RPC 帧打扰客户端。
	if strings.HasPrefix(r.URL.Path, "/web/") {
		client.send(map[string]any{
			"type": "hello", "running": s.agent.Running(), "running_conv": s.RunConv(),
			"time": time.Now().Format(time.RFC3339),
		})
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		// 应用层心跳：移动网络下 NAT/代理会回收长时间无数据的连接（且浏览器
		// 收不到 close），前端定时发 ping，服务端回 pong，用来看住链路。
		if isPingFrame(data) {
			client.send(map[string]any{"type": "pong", "time": time.Now().Format(time.RFC3339)})
			continue
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

// isPingFrame 判断是否为应用层心跳帧（{"type":"ping"}）。
func isPingFrame(data []byte) bool {
	var m struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	return m.Type == "ping"
}
