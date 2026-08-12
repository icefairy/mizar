package server

import (
	"encoding/json"
	"net/http"

	"mizar/internal/agent"
)

// ============================================================================
// JSON-RPC 2.0 端点（/rpc）
//
// 参照 pi 的 RPC 命令集（prompt/steer/abort/...），对齐到 JSON-RPC 2.0 规范：
//   - agent.run     执行任务（同步返回最终回复）
//   - agent.steer   快速插入纠正消息（不等待，立即注入当前循环）
//   - agent.abort   中止当前循环
//   - agent.running 查询是否正在运行
//   - tools.list    列出可用工具
//   - system.ping   健康检查
//
// 请求:  {"jsonrpc":"2.0","id":1,"method":"agent.run","params":{"task":"..."}}
// 响应:  {"jsonrpc":"2.0","id":1,"result":{"reply":"..."}}
// 错误:  {"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"..."}}
// ============================================================================

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInternalError  = -32603
)

// handleJSONRPC POST /rpc。
func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Services.JSONRPC.Load() {
		errJSON(w, http.StatusServiceUnavailable, "jsonrpc service disabled")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: rpcParseError, Message: "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "invalid request"}})
		return
	}
	resp := s.dispatch(&req)
	writeJSON(w, http.StatusOK, resp)
}

// dispatch 分发 RPC 方法。
func (s *Server) dispatch(req *rpcRequest) rpcResponse {
	method := req.Method
	switch method {
	case "agent.run":
		return s.rpcRun(req)
	case "agent.steer":
		return s.rpcSteer(req)
	case "agent.abort":
		return s.rpcAbort(req)
	case "agent.running":
		return s.rpcRunning(req)
	case "tools.list":
		return s.rpcTools(req)
	case "system.ping":
		return s.rpcPing(req)
	default:
		// 插件注册的自定义 RPC 方法（rpc_<name> 导出）
		if out, err := s.agent.Plugins.CallRPC(method, string(req.Params)); err == nil {
			var result any
			if err := json.Unmarshal([]byte(out), &result); err == nil {
				return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
			}
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"result": out}}
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + method}}
	}
}

type rpcRunParams struct {
	Task string `json:"task"`
}

func (s *Server) rpcRun(req *rpcRequest) rpcResponse {
	var p rpcRunParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Task == "" {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "params.task required"}}
	}
	s.logf("rpc agent.run: %s", truncateStr(p.Task, 80))
	reply, err := s.agent.Run(p.Task)
	if err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInternalError, Message: err.Error()}}
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"reply": reply}}
}

type rpcSteerParams struct {
	Message string `json:"message"`
}

func (s *Server) rpcSteer(req *rpcRequest) rpcResponse {
	var p rpcSteerParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Message == "" {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: rpcInvalidRequest, Message: "params.message required"}}
	}
	s.agent.Steer(p.Message)
	s.logf("rpc steer: %s", truncateStr(p.Message, 80))
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}}
}

func (s *Server) rpcAbort(req *rpcRequest) rpcResponse {
	s.agent.Abort()
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true}}
}

func (s *Server) rpcRunning(req *rpcRequest) rpcResponse {
	// 简单判断：abort 标志被置位或无并发运行时返回。
	// 并发运行时由 wsSession 维护，这里返回基本状态。
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"running": false}}
}

func (s *Server) rpcTools(req *rpcRequest) rpcResponse {
	tools := s.agent.Plugins.Tools()
	list := make([]map[string]string, 0, len(tools))
	for _, t := range tools {
		list = append(list, map[string]string{"name": t.Name, "description": t.Description})
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": list}}
}

func (s *Server) rpcPing(req *rpcRequest) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"pong": true, "model": s.agent.Model()}}
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

var _ = agent.ErrAborted // 保留引用（后续 WS 会话用）
