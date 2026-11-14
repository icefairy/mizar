package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ============================================================================
// 路由注册表（RouteMeta）—— 全部端点的单一数据源（P1）。
//
// 此前端点散落在 server.go / webui.go 的 mux.HandleFunc 调用里，文档
// （docs/api.md）靠人工维护，新端点加完经常忘更新。现在所有端点收进
// 这张表：
//   - GET /admin/routes  → 机器可读清单（含插件 rpc_* 动态端点）
//   - mizar -gen-openapi → OpenAPI JSON（与 api.md 同源，不再手写漂移）
// ============================================================================

// RouteMeta 一个端点的元数据。
type RouteMeta struct {
	// Group 分组标签：openai / rpc / ws / admin / web / plugin。
	Group string `json:"group"`
	// Method 逗号分隔的允许方法，如 "GET,POST"。
	Method string `json:"method"`
	// Path 路径，如 /v1/chat/completions；插件 REST 用 /ext/{name}。
	Path string `json:"path"`
	// Handler 处理函数名（如 handleChat），仅用于文档追踪。
	Handler string `json:"handler"`
	// Auth 鉴权方式：bearer / cookie / none。
	Auth string `json:"auth"`
	// EnabledByService 运行时开关名（http / jsonrpc / ws），空=常开。
	EnabledByService string `json:"enabled_by_service,omitempty"`
	// Summary 一句话说明（进 OpenAPI summary）。
	Summary string `json:"summary"`
}

// routeTable 静态路由表：与 mux.HandleFunc 注册一一对应。
// 新增端点必须同时在此登记，否则 /admin/routes 漏报。
var routeTable = []RouteMeta{
	// OpenAI 兼容（Bearer）
	{Group: "openai", Method: "GET", Path: "/v1/models", Handler: "handleModels", Auth: "bearer", EnabledByService: "http",
		Summary: "模型清单（OpenAI 兼容）"},
	{Group: "openai", Method: "POST", Path: "/v1/chat/completions", Handler: "handleChat", Auth: "bearer", EnabledByService: "http",
		Summary: "对话补全（OpenAI 兼容，流式/非流式）"},
	// JSON-RPC 2.0（Bearer）
	{Group: "rpc", Method: "POST", Path: "/rpc", Handler: "handleJSONRPC", Auth: "bearer", EnabledByService: "jsonrpc",
		Summary: "JSON-RPC 2.0：agent.run/steer/abort/running、tools.list、system.ping、插件 rpc_*"},
	// WebSocket（Bearer）
	{Group: "ws", Method: "GET", Path: "/ws", Handler: "handleWS", Auth: "bearer", EnabledByService: "ws",
		Summary: "WebSocket 实时事件流 + 中途纠正"},
	// 管理（Bearer）
	{Group: "admin", Method: "GET", Path: "/admin/status", Handler: "handleStatus", Auth: "bearer",
		Summary: "服务状态 + 开关 + 运行时统计"},
	{Group: "admin", Method: "POST", Path: "/admin/switch", Handler: "handleSwitch", Auth: "bearer",
		Summary: "运行时开关各服务（http/rpc/ws）"},
	{Group: "admin", Method: "GET", Path: "/admin/routes", Handler: "handleRoutes", Auth: "bearer",
		Summary: "本路由表（机器可读，含插件 rpc_* 动态端点）"},
	// Web UI 静态资源（cookie 会话）
	{Group: "web", Method: "GET", Path: "/web/", Handler: "webuiFileServer", Auth: "cookie",
		Summary: "Web UI 前端（embed 或外部目录，见 /web/api/frontend）"},
	{Group: "web", Method: "GET", Path: "/", Handler: "rootRedirect", Auth: "none",
		Summary: "根路径 → 302 到 /web/"},
	// Web UI API（cookie 会话）
	{Group: "web", Method: "POST", Path: "/web/api/login", Handler: "handleWebLogin", Auth: "cookie",
		Summary: "登录，下发会话 cookie"},
	{Group: "web", Method: "POST", Path: "/web/api/logout", Handler: "handleWebLogout", Auth: "cookie",
		Summary: "登出，注销会话 cookie"},
	{Group: "web", Method: "GET", Path: "/web/api/version", Handler: "handleWebVersion", Auth: "none",
		Summary: "版本号（登录页展示用，无需鉴权）"},
	{Group: "web", Method: "GET", Path: "/web/api/me", Handler: "handleWebMe", Auth: "cookie",
		Summary: "当前身份 + 模型 + 设置"},
	{Group: "web", Method: "GET,POST,DELETE", Path: "/web/api/conversations", Handler: "handleConversations", Auth: "cookie",
		Summary: "会话列表 / 新建 / 删除"},
	{Group: "web", Method: "GET,PATCH", Path: "/web/api/conversation", Handler: "handleConversation", Auth: "cookie",
		Summary: "单会话详情 / 重命名（?id=）"},
	{Group: "web", Method: "POST", Path: "/web/api/chat", Handler: "handleWebChat", Auth: "cookie",
		Summary: "普通对话（直连 LLM，无工具循环；斜杠命令在此拦截）"},
	{Group: "web", Method: "POST", Path: "/web/api/run", Handler: "handleWebRun", Auth: "cookie",
		Summary: "Agent 模式（完整工具循环；同会话忙时 409）"},
	{Group: "web", Method: "POST", Path: "/web/api/abort", Handler: "handleWebAbort", Auth: "cookie",
		Summary: "中止当前会话运行，清空待消费 steer"},
	{Group: "web", Method: "POST", Path: "/web/api/steer", Handler: "handleWebSteer", Auth: "cookie",
		Summary: "纠正消息：运行中注入，空闲时排队"},
	{Group: "web", Method: "GET", Path: "/web/api/events", Handler: "handleWebEvents", Auth: "cookie",
		Summary: "事件回放（?conversation_id=&after=<seq>，断线补齐）"},
	{Group: "web", Method: "GET", Path: "/web/api/draft", Handler: "handleWebDraft", Auth: "cookie",
		Summary: "运行中草稿（页面被杀/刷新后恢复进度）"},
	{Group: "web", Method: "POST", Path: "/web/api/answer", Handler: "handleWebAnswer", Auth: "cookie",
		Summary: "ask_user_question 作答"},
	{Group: "web", Method: "GET", Path: "/web/api/ask", Handler: "handleWebAskPending", Auth: "cookie",
		Summary: "恢复未答问题（刷新页面后重弹）"},
	{Group: "web", Method: "GET,POST", Path: "/web/api/settings", Handler: "handleSettings", Auth: "cookie",
		Summary: "设置读写（模型/温度/top_p/max_tokens/思考等级/系统提示词）"},
	{Group: "web", Method: "GET", Path: "/web/api/models", Handler: "handleModelsList", Auth: "cookie",
		Summary: "可用模型清单（拉自当前供应商）"},
	{Group: "web", Method: "GET,POST", Path: "/web/api/providers", Handler: "handleWebProviders", Auth: "cookie",
		Summary: "供应商收藏夹管理"},
	{Group: "web", Method: "GET", Path: "/web/api/status", Handler: "handleWebStatus", Auth: "cookie",
		Summary: "状态栏统计（tokens/上下文/缓存/速度）"},
	{Group: "web", Method: "GET", Path: "/web/api/frontend", Handler: "handleWebFrontend", Auth: "cookie",
		Summary: "前端资源来源探测（embed / 外部目录 + 路径）"},
	{Group: "web", Method: "GET", Path: "/web/ws", Handler: "handleWS", Auth: "cookie",
		Summary: "Web UI WebSocket（cookie 鉴权，复用广播 hub）"},
}

// allRoutes 返回静态表 + 插件动态端点（rpc_* / ext/*）。
func (s *Server) allRoutes() []RouteMeta {
	out := make([]RouteMeta, len(routeTable))
	copy(out, routeTable)
	// 插件动态端点：rpc_<name>（经 /rpc）与 /ext/<name>（REST 直达）。
	if s.agent != nil && s.agent.Plugins != nil {
		for _, m := range s.agent.Plugins.RPCMethods() {
			out = append(out,
				RouteMeta{Group: "plugin", Method: "POST", Path: "/rpc (method=rpc_" + m.Name + ")",
					Handler: "plugins.CallRPC", Auth: "bearer",
					Summary: "插件 RPC：rpc_" + m.Name + "（来自 " + m.PluginFile + "）"},
				RouteMeta{Group: "plugin", Method: "GET,POST", Path: "/ext/" + m.Name,
					Handler: "handleExtRPC", Auth: "bearer",
					Summary: "插件 RPC REST 直达（来自 " + m.PluginFile + "）"},
			)
		}
	}
	return out
}

// handleRoutes GET /admin/routes → 机器可读路由清单（含插件动态端点）。
func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.cfg.Version,
		"routes":  s.allRoutes(),
	})
}

// ============================================================================
// 导出版：供 main 包 -gen-openapi 使用（静态表，不依赖完整 Server 实例）。
// 插件 rpc_* 动态端点由运行时 /admin/routes 覆盖，生成物不含。
// ============================================================================

// PublicRoutes 返回静态路由表副本。
func PublicRoutes() []RouteMeta {
	return append([]RouteMeta{}, routeTable...)
}

// OpenAPIFromTable 从静态表生成 OpenAPI 3.0 JSON。
func OpenAPIFromTable(version string) (string, error) {
	routes := PublicRoutes()
	paths := map[string]any{}
	for _, rt := range routes {
		if strings.Contains(rt.Path, " (") {
			continue
		}
		entry, ok := paths[rt.Path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[rt.Path] = entry
		}
		entry[strings.ToLower(firstMethod(rt.Method))] = map[string]any{
			"summary": rt.Summary,
			"responses": map[string]any{
				"200": map[string]any{"description": "OK"},
			},
		}
	}
	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "Mizar (开阳) Server API",
			"version":     version,
			"description": "生成自路由注册表，非手写；运行时全量清单（含插件 rpc_*）见 GET /admin/routes。",
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"},
				"cookieAuth": map[string]any{"type": "apiKey", "in": "cookie", "name": "mizar_web_session"},
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// MarkdownTable 静态表的 markdown 渲染（-gen-openapi 同时出 api 路由节用）。
func MarkdownTable() string {
	var b strings.Builder
	routes := PublicRoutes()
	sort.Slice(routes, func(i, j int) bool { return routes[i].Path < routes[j].Path })
	b.WriteString("| 分组 | 方法 | 路径 | 鉴权 | 说明 |\n|---|---|---|---|---|\n")
	for _, rt := range routes {
		b.WriteString(fmt.Sprintf("| %s | %s | `%s` | %s | %s |\n",
			rt.Group, rt.Method, rt.Path, rt.Auth, rt.Summary))
	}
	return b.String()
}

// firstMethod 取 "GET,POST" 里的第一个方法（OpenAPI path item 按首方法落键）。
func firstMethod(m string) string {
	if i := strings.IndexByte(m, ','); i >= 0 {
		return m[:i]
	}
	return m
}
