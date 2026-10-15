package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// Server Mizar Server 模式：HTTP + JSON-RPC + WebSocket。
type Server struct {
	cfg     *Config
	agent   *agent.Agent
	session *session.Store
	httpSrv *http.Server
	mux     *http.ServeMux
	wsUp    *wsUpgrader
	hub     *hub
	web     *webUI
	// runMu 串行化 Agent.Run：Agent 实例内的 Initial / OnLLMStream / steer 槽位
	// 都是共享可变状态，多个入口（/v1/chat/completions、/rpc、/web/api/run）
	// 并发调用会互相覆盖。用 TryLock 而非 Lock——忙时立即返回 409，
	// 避免请求无限排队；steer/abort 不走此锁，等待期间仍可实时纠正。
	runMu sync.Mutex
}

// acquireRun 尝试独占 Agent；失败返回 false（已有 Run 在执行）。
func (s *Server) acquireRun() bool { return s.runMu.TryLock() }

// releaseRun 释放 Agent 独占。
func (s *Server) releaseRun() { s.runMu.Unlock() }

// New 创建 Server。
func New(cfg *Config, a *agent.Agent, st *session.Store) *Server {
	s := &Server{
		cfg:     cfg,
		agent:   a,
		session: st,
		mux:     http.NewServeMux(),
		hub:     newHub(),
		web:     newWebUI(webStorePath()),
	}
	s.wsUp = newWSUpgrader(cfg.Token)
	s.routes()
	s.attachHooks()
	return s
}

// routes 注册所有端点。
func (s *Server) routes() {
	// OpenAI 兼容
	s.mux.HandleFunc("/v1/models", s.auth(s.handleModels))
	s.mux.HandleFunc("/v1/chat/completions", s.auth(s.handleChat))
	// JSON-RPC 2.0
	s.mux.HandleFunc("/rpc", s.auth(s.handleJSONRPC))
	// WebSocket
	s.mux.HandleFunc("/ws", s.auth(s.handleWS))
	// 管理：动态开关
	s.mux.HandleFunc("/admin/status", s.auth(s.handleStatus))
	s.mux.HandleFunc("/admin/switch", s.auth(s.handleSwitch))
	// Web UI（浏览器界面 + cookie 登录）
	s.webRoutes()
}

// auth 认证中间件：Token 非空时校验 Bearer。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.Token {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Start 启动 HTTP 服务（阻塞）。
func (s *Server) Start() error {
	s.httpSrv = &http.Server{Addr: s.cfg.Addr, Handler: s.mux, ReadTimeout: 10 * time.Minute}
	return s.httpSrv.ListenAndServe()
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errJSON 统一错误响应。
func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": "mizar_error"},
	})
}

// logf 服务日志。
func (s *Server) logf(format string, args ...any) {
	fmt.Printf("[mizar-server] "+format+"\n", args...)
}

// EmitToWS 向所有 WS 客户端广播自定义事件（插件 ws_emit 能力）。
func (s *Server) EmitToWS(event, dataJSON string) {
	var data any
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			data = dataJSON
		}
	}
	s.hub.broadcast(map[string]any{"type": "event", "event": event, "data": data})
}

// broadcastStream 广播流式增量（step + 可显示文本增量 + 是否思考过程）。
func (s *Server) broadcastStream(step int, delta, kind string) {
	s.hub.broadcast(map[string]any{
		"type": "event", "event": "stream", "step": step, "delta": delta, "kind": kind,
	})
}
