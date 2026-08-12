package server

import (
	"encoding/json"
	"net/http"
)

// ============================================================================
// 管理端点（/admin）——动态开关服务
//
//   GET  /admin/status  查询各服务开关状态
//   POST /admin/switch  动态开启/关闭服务
//       body: {"service":"http","enabled":true}
//       service: http | jsonrpc | ws
//
// 动态开关即时生效：关闭后对应端点返回 503，连接中的 WS 继续运行到断开。
// 重启后回到 Config 初始值（CLI 开关）。适合：临时维护、流量控制、安全收口。
// ============================================================================

// handleStatus GET /admin/status。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"services": map[string]bool{
			"http":    s.cfg.Services.HTTP.Load(),
			"jsonrpc": s.cfg.Services.JSONRPC.Load(),
			"ws":      s.cfg.Services.WS.Load(),
		},
		"model": s.agent.Model(),
	})
}

// switchRequest 动态开关请求体。
type switchRequest struct {
	Service string `json:"service"`
	Enabled bool   `json:"enabled"`
}

// handleSwitch POST /admin/switch。
func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req switchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	switch req.Service {
	case "http":
		s.cfg.Services.HTTP.Store(req.Enabled)
	case "jsonrpc":
		s.cfg.Services.JSONRPC.Store(req.Enabled)
	case "ws":
		s.cfg.Services.WS.Store(req.Enabled)
	default:
		errJSON(w, http.StatusBadRequest, "unknown service: "+req.Service+" (http|jsonrpc|ws)")
		return
	}
	s.logf("admin switch: %s -> %v", req.Service, req.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": req.Service, "enabled": req.Enabled})
}
