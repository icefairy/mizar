package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"mizar/internal/mcp"
)

// ============================================================================
// MCP server（HTTP / Streamable HTTP 传输）
//
// Mizar 作为 MCP 服务端，把已注册的工具（内置 + 插件）通过 POST /mcp 暴露给
// Claude Desktop / CodeBuddy / Cursor 等 MCP 客户端。仅实现工具能力：
// initialize / ping / tools/list / tools/call。
//
// 传输是「单个 JSON-RPC 请求 → 单个 JSON 响应」，与 mcp.HTTPTransport 对应；
// 不提供 GET SSE 长连接（客户端用 POST 已足够，且避免并发长连接管理）。
//
// 安全：MCP 客户端只能带 Bearer token，没有 cookie 登录通道；因此 /mcp 强制
// 要求 --token 非空。否则等于把 bash 等工具无认证暴露到网络。
// ============================================================================

const mcpMaxBody = 4 << 20 // 单次请求体上限（工具参数，不需要更大）

// mcpRPCRequest 兼容 id 为字符串或数字：用 RawMessage 原样回显。
type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcp.MCPError   `json:"error,omitempty"`
}

type mcpToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpToolContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// handleMCP POST /mcp → MCP JSON-RPC（Streamable HTTP 的单请求/单响应形态）。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// s.auth 在 token 非空时已校验；这里再拒绝空 token 的部署，避免无认证暴露工具。
	if s.cfg == nil || s.cfg.Token == "" {
		errJSON(w, http.StatusForbidden, "MCP 端点需要 --token（拒绝无认证暴露工具）")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody))
	if err != nil {
		writeMCPError(w, nil, -32700, "read body: "+err.Error())
		return
	}
	var req mcpRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeMCPError(w, nil, -32700, "parse error")
		return
	}
	if req.JSONRPC != mcp.JSONRPCVersion || req.Method == "" {
		writeMCPError(w, req.ID, -32600, "invalid request")
		return
	}
	// 通知（无 id）不需要响应体，按 Streamable HTTP 规范回 202。
	if req.ID == nil || strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr := s.mcpDispatch(req.Method, req.Params)
	if rpcErr != nil {
		writeMCPError(w, req.ID, rpcErr.Code, rpcErr.Message)
		return
	}
	writeJSON(w, http.StatusOK, mcpRPCResponse{JSONRPC: mcp.JSONRPCVersion, ID: req.ID, Result: result})
}

// mcpDispatch 执行一个 MCP 方法，返回 result 或协议错误。
func (s *Server) mcpDispatch(method string, params json.RawMessage) (any, *mcp.MCPError) {
	switch method {
	case mcp.MethodInitialize:
		return map[string]any{
			"protocolVersion": mcp.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "mizar",
				"version": s.cfg.Version,
			},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case mcp.MethodToolsList:
		return map[string]any{"tools": s.mcpTools()}, nil
	case mcp.MethodToolsCall:
		return s.mcpCallTool(params)
	default:
		return nil, &mcp.MCPError{Code: -32601, Message: "method not found: " + method}
	}
}

// mcpTools 把注册表里的工具转成 MCP tools/list 结构。
// 工具入参本来就是 JSON 字符串（与插件 tool_* 约定一致），没有字段级 schema，
// 所以给一个宽松的 object schema，由 tool description 说明具体参数。
func (s *Server) mcpTools() []map[string]any {
	out := []map[string]any{}
	if s.agent == nil || s.agent.Plugins == nil {
		return out
	}
	for _, t := range s.agent.Plugins.Tools() {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
		})
	}
	return out
}

// mcpCallTool 执行 tools/call。工具执行错误按 MCP 规范放在 result.isError，
// 而不是 JSON-RPC error（后者表示协议层失败）。
func (s *Server) mcpCallTool(params json.RawMessage) (any, *mcp.MCPError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &mcp.MCPError{Code: -32602, Message: "tools/call requires name"}
	}
	if s.agent == nil || s.agent.Plugins == nil {
		return mcpToolResult{Content: []mcpToolContent{{Type: "text", Text: "agent 未初始化"}}, IsError: true}, nil
	}
	args := mcpArgsString(p.Arguments)
	out, err := s.agent.Plugins.CallInDir(p.Name, args, s.agent.WorkDir)
	if err != nil {
		msg := err.Error()
		if out != "" {
			msg = out + "\n" + msg
		}
		return mcpToolResult{Content: []mcpToolContent{{Type: "text", Text: msg}}, IsError: true}, nil
	}
	return mcpToolResult{Content: []mcpToolContent{{Type: "text", Text: out}}}, nil
}

// mcpArgsString 把 MCP arguments 转成工具期望的 JSON 字符串。
// 对象 → 原样 JSON；字符串 → 直接使用（兼容客户端把参数当字符串传）；
// 空 → "{}"（工具自行报缺参）。
func mcpArgsString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if strings.TrimSpace(str) == "" {
			return "{}"
		}
		return str
	}
	return string(raw)
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, http.StatusOK, mcpRPCResponse{
		JSONRPC: mcp.JSONRPCVersion,
		ID:      id,
		Error:   &mcp.MCPError{Code: code, Message: msg},
	})
}
