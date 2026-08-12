package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
}

// New 创建 Server。
func New(cfg *Config, a *agent.Agent, st *session.Store) *Server {
	s := &Server{
		cfg:     cfg,
		agent:   a,
		session: st,
		mux:     http.NewServeMux(),
		hub:     newHub(),
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
