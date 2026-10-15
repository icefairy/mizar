package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mizar/internal/agent"
	"mizar/internal/config"
	"mizar/internal/llm"
)

//go:embed webui
var webuiFS embed.FS

// webSession 一次 Web 登录会话。
type webSession struct {
	username string
	expires  time.Time
}

// webUI Web UI 运行时状态：登录会话表 + 会话存储。
type webUI struct {
	mu       sync.Mutex
	sessions map[string]webSession // token -> session
	sessTTL  time.Duration
	store    *webStore
}

func newWebUI(storePath string) *webUI {
	return &webUI{
		sessions: map[string]webSession{},
		sessTTL:  24 * time.Hour,
		store:    newWebStore(storePath),
	}
}

// cookieName 会话 cookie 名。
const webCookieName = "mizar_web_session"

// newToken 生成随机会话 token。
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randHex 生成 n 字节随机十六进制串（会话 id 用）。
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createSession 创建会话并返回 token。
func (u *webUI) createSession(username string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	tok := newToken()
	u.sessions[tok] = webSession{username: username, expires: time.Now().Add(u.sessTTL)}
	return tok
}

// validSession 校验 token 是否有效（顺带清理过期）。
func (u *webUI) validSession(tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.sessions[tok]
	if !ok {
		return "", false
	}
	if time.Now().After(s.expires) {
		delete(u.sessions, tok)
		return "", false
	}
	return s.username, true
}

// dropSession 注销 token。
func (u *webUI) dropSession(tok string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.sessions, tok)
}

// webStorePath Web UI 数据文件路径（~/.mizar/webui.json）。
func webStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".mizar", "webui.json")
}

// routes 注册 Web UI 端点（在 Server.routes 中调用）。
func (s *Server) webRoutes() {
	if !s.cfg.Web.Enabled {
		return
	}
	// 静态资源（登录页本身不需要鉴权；数据接口才需要）
	sub, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		s.logf("webui embed 失败: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(sub))
	s.mux.HandleFunc("/web/", func(w http.ResponseWriter, r *http.Request) {
		// 去掉 "/web" 前缀（保留前导 /），让 FileServer 自己处理目录索引。
		// 不能改写成 /index.html——那会触发 FileServer 的 canonical redirect 循环。
		p := strings.TrimPrefix(r.URL.Path, "/web")
		if p == "" {
			p = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = p
		fileServer.ServeHTTP(w, r2)
	})
	// 根路径重定向到 Web UI
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/web/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	// 登录 / 登出 / 当前身份
	s.mux.HandleFunc("/web/api/login", s.handleWebLogin)
	s.mux.HandleFunc("/web/api/logout", s.handleWebLogout)
	s.mux.HandleFunc("/web/api/me", s.webAuth(s.handleWebMe))
	// 会话 CRUD
	s.mux.HandleFunc("/web/api/conversations", s.webAuth(s.handleConversations))
	s.mux.HandleFunc("/web/api/conversation", s.webAuth(s.handleConversation))
	// 对话 / Agent 运行 / 中止 / 纠正
	s.mux.HandleFunc("/web/api/chat", s.webAuth(s.handleWebChat))
	s.mux.HandleFunc("/web/api/run", s.webAuth(s.handleWebRun))
	s.mux.HandleFunc("/web/api/abort", s.webAuth(s.handleWebAbort))
	s.mux.HandleFunc("/web/api/steer", s.webAuth(s.handleWebSteer))
	// 设置（模型 + 对话参数）
	s.mux.HandleFunc("/web/api/settings", s.webAuth(s.handleSettings))
	s.mux.HandleFunc("/web/api/models", s.webAuth(s.handleModelsList))
	// WebSocket（cookie 鉴权，复用广播 hub）
	s.mux.HandleFunc("/web/ws", s.webAuth(s.handleWS))
}

// webAuth Web UI 鉴权中间件（cookie 会话）。
func (s *Server) webAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.web == nil {
			errJSON(w, http.StatusServiceUnavailable, "web ui disabled")
			return
		}
		c, err := r.Cookie(webCookieName)
		if err != nil {
			errJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if _, ok := s.web.validSession(c.Value); !ok {
			errJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// handleWebLogin POST /web/api/login {username,password}
func (s *Server) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		errJSON(w, http.StatusServiceUnavailable, "web ui disabled")
		return
	}
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}
	wantUser := s.cfg.Web.Username
	if wantUser == "" {
		wantUser = "admin"
	}
	// 常量时间比较，避免时序侧信道
	userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(wantUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.cfg.Web.Password)) == 1
	if !userOK || !passOK {
		s.logf("web login failed: user=%q from %s", body.Username, r.RemoteAddr)
		errJSON(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	tok := s.web.createSession(body.Username)
	http.SetCookie(w, &http.Cookie{
		Name:     webCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.web.sessTTL / time.Second),
	})
	s.logf("web login ok: user=%q from %s", body.Username, r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": body.Username})
}

// handleWebLogout POST /web/api/logout
func (s *Server) handleWebLogout(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		errJSON(w, http.StatusServiceUnavailable, "web ui disabled")
		return
	}
	if c, err := r.Cookie(webCookieName); err == nil {
		s.web.dropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: webCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWebMe GET /web/api/me → 当前登录用户 + 模型 + 设置
func (s *Server) handleWebMe(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie(webCookieName)
	user := ""
	if c != nil {
		user, _ = s.web.validSession(c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user,
		"model":    s.agent.Model(),
		"settings": s.web.store.getSettings(),
	})
}

// handleConversations GET(列表) / POST(新建) / DELETE(删除)
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	st := s.web.store
	switch r.Method {
	case http.MethodGet:
		list := st.list()
		// 只返回摘要（不含完整消息），减小响应体
		out := make([]map[string]any, 0, len(list))
		for _, c := range list {
			out = append(out, map[string]any{
				"id": c.ID, "title": c.Title, "mode": c.Mode,
				"updated_at": c.UpdatedAt, "count": len(c.Messages),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
	case http.MethodPost:
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Mode == "" {
			body.Mode = "chat"
		}
		c := st.create(body.Mode)
		writeJSON(w, http.StatusOK, map[string]any{"conversation": c})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" || !st.delete(id) {
			errJSON(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleConversation GET(单会话) / PATCH(重命名)
func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	st := s.web.store
	id := r.URL.Query().Get("id")
	if id == "" {
		errJSON(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		c := st.get(id)
		if c == nil {
			errJSON(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversation": c})
	case http.MethodPatch:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
			errJSON(w, http.StatusBadRequest, "title required")
			return
		}
		if !st.rename(id, strings.TrimSpace(body.Title)) {
			errJSON(w, http.StatusNotFound, "conversation not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleWebChat POST /web/api/chat {conversation_id,message} → 普通对话（不调用工具）
func (s *Server) handleWebChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		errJSON(w, http.StatusBadRequest, "message required")
		return
	}
	// 斜杠命令拦截：/compact、/list_plugins 等由命令注册表处理，不发给 LLM。
	if out, ok := s.dispatchWebCommand(body.Message, body.ConversationID); ok {
		writeJSON(w, http.StatusOK, map[string]any{"reply": out, "command": true})
		return
	}
	reply, err := s.chatOnce(body.ConversationID, body.Message)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

// dispatchWebCommand 尝试把消息当作斜杠命令执行。
// 命中返回（输出文本, true）；非命令返回 ("", false)。
// 未知命令也返回 true（提示「未知命令」），避免把 /xxx 当普通消息误发 LLM。
// convID 用于需要操作当前会话历史的命令（如 /compact）。
func (s *Server) dispatchWebCommand(msg, convID string) (string, bool) {
	trimmed := strings.TrimSpace(msg)
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	if s.agent == nil || s.agent.Commands == nil {
		return "", false
	}
	// /compact 特判：Web 的会话历史在 webStore，需压缩当前会话并回写，
	// 而不是操作 agent 内存里的 a.Initial（那是 TUI/单会话语义）。
	if name, args := splitCommand(trimmed); name == "compact" {
		return s.compactWebConversation(convID, args), true
	}
	handled, out, err := s.agent.Commands.Dispatch(trimmed)
	if !handled {
		return "", false
	}
	if err != nil {
		return "✗ " + err.Error(), true
	}
	if strings.TrimSpace(out) == "" {
		return "✓ 命令已执行", true
	}
	return out, true
}

// splitCommand 拆出命令名与参数（"/compact 指引" → "compact", "指引"）。
func splitCommand(line string) (string, string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", ""
	}
	name := strings.TrimPrefix(fields[0], "/")
	args := ""
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	}
	return name, args
}

// compactWebConversation 压缩指定 Web 会话的历史并回写。
// 会话历史不足或未启用压缩器时给出明确提示（不再「无反应」）。
func (s *Server) compactWebConversation(convID, customPrompt string) string {
	if s.agent == nil || s.agent.Compactor == nil {
		return "✗ 会话压缩未启用（启动时使用了 --no-compact）"
	}
	if convID == "" {
		return "✗ 无活动会话，无法压缩"
	}
	c := s.web.store.get(convID)
	if c == nil {
		return "✗ 会话不存在"
	}
	if len(c.Messages) <= 1 {
		return "✗ 消息不足，无需压缩"
	}
	// convMessage → agent.Message
	msgs := make([]agent.Message, 0, len(c.Messages))
	for _, m := range c.Messages {
		msgs = append(msgs, agent.Message{Role: m.Role, Content: m.Content})
	}
	est := agent.EstimateMessages(msgs)

	// 自定义摘要指引：临时包裹 Summarize
	comp := s.agent.Compactor
	origSummarize := comp.Summarize
	if strings.TrimSpace(customPrompt) != "" {
		cp := strings.TrimSpace(customPrompt)
		comp.Summarize = func(ms []agent.Message) (string, error) {
			base, err := origSummarize(ms)
			if err != nil {
				return "", err
			}
			return base + "\n\n额外要求：" + cp, nil
		}
		defer func() { comp.Summarize = origSummarize }()
	}

	newMsgs, err := comp.CompactForce(msgs, comp.KeepRecentTokens)
	if err != nil {
		return "✗ 压缩失败: " + err.Error()
	}
	cutOccurred := false
	for _, m := range newMsgs {
		if m.Kind == agent.KindSummary {
			cutOccurred = true
			break
		}
	}
	if !cutOccurred {
		return fmt.Sprintf("当前估算: %d tokens（%d 条消息）\n  无需压缩（消息结构不允许切分）", est, len(msgs))
	}
	// 回写会话历史
	out := make([]convMessage, 0, len(newMsgs))
	for _, m := range newMsgs {
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "user" // 摘要等内部消息对前端按 user 呈现
		}
		out = append(out, convMessage{Role: role, Content: m.Content})
	}
	s.web.store.setMessages(convID, out)

	newEst := agent.EstimateMessages(newMsgs)
	reduced := est - newEst
	pct := 0.0
	if est > 0 {
		pct = float64(reduced) / float64(est) * 100
	}
	return fmt.Sprintf("当前估算: %d tokens（%d 条消息）\n✓ 已压缩: %d → %d tokens（节省 %d tokens, %.1f%%）",
		est, len(msgs), est, newEst, reduced, pct)
}

// chatOnce 普通对话：按会话维护历史，直接调 LLM（无工具循环）。
// 支持流式：底层 LLM 若实现 ChatStream，则逐分片通过 WS 广播。
func (s *Server) chatOnce(convID, msg string) (string, error) {
	client := s.agent.LLM
	if client == nil {
		return "", errString("LLM 未初始化")
	}
	set := s.web.store.getSettings()
	sys := set.System
	if strings.TrimSpace(sys) == "" {
		sys = "你是开阳（Mizar）的对话助手。直接、简洁地回答用户问题，用中文。"
	}
	// 组装历史
	msgs := []agent.Message{{Role: agent.RoleSystem, Content: sys}}
	if convID != "" {
		if c := s.web.store.get(convID); c != nil {
			for _, m := range c.Messages {
				msgs = append(msgs, agent.Message{Role: m.Role, Content: m.Content})
			}
		}
	}
	msgs = append(msgs, agent.Message{Role: agent.RoleUser, Content: msg})
	// 历史截断：仅保留 system + 最近 N 条（N = 设置 max_history，默认 50）。
	// 仅普通对话模式生效；Agent 模式走完整循环，不受此限制。
	maxHist := set.MaxHistory
	if maxHist <= 0 {
		maxHist = 50
	}
	if len(msgs) > maxHist+1 {
		msgs = append(msgs[:1], msgs[len(msgs)-maxHist:]...)
	}

	var reply string
	var err error
	// 优先走 AIChatStream：wrapReply=false，返回纯文本（不包 agent 控制 JSON），
	// 且支持 system/参数覆盖；逐分片通过 WS 广播。
	if sl, ok := client.(interface {
		AIChatStream(llm.AIChatRequest, func(agent.StreamDelta)) (string, error)
	}); ok {
		req := llm.AIChatRequest{System: sys, Thinking: set.Thinking,
			Temperature: set.Temperature, MaxTokens: set.MaxTokens}
		if set.TopP != nil {
			// AIChatRequest 无 TopP 字段，走全局 Params（applySettings 已设）
			_ = set.TopP
		}
		for _, m := range msgs[1:] { // 跳过我们组装的 system，system 用 req.System 传
			req.Messages = append(req.Messages, llm.AIChatMsg{Role: m.Role, Content: m.Content})
		}
		reply, err = sl.AIChatStream(req, func(d agent.StreamDelta) {
			if d.Thinking != "" {
				s.broadcastStream(0, d.Thinking, "thinking")
			}
			if d.Content != "" {
				s.broadcastStream(0, d.Content, "reply")
			}
		})
	} else {
		reply, err = client.Chat(msgs)
	}
	if err != nil {
		return "", err
	}
	// 记录到会话
	if convID != "" {
		s.web.store.appendMsg(convID, convMessage{Role: "user", Content: msg})
		s.web.store.appendMsg(convID, convMessage{Role: "assistant", Content: reply})
		s.web.store.setMode(convID, "chat")
	}
	return reply, nil
}

// handleWebRun POST /web/api/run {conversation_id,message} → Agent 模式（完整工具循环）
func (s *Server) handleWebRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		errJSON(w, http.StatusBadRequest, "message required")
		return
	}
	// 斜杠命令拦截：/compact、/list_plugins 等由命令注册表处理，不进入 Agent 循环。
	if out, ok := s.dispatchWebCommand(body.Message, body.ConversationID); ok {
		s.logf("web command: %s", truncateStr(body.Message, 80))
		writeJSON(w, http.StatusOK, map[string]any{"reply": out, "command": true})
		return
	}
	s.logf("web agent.run: %s", truncateStr(body.Message, 80))

	// Agent 模式复用会话历史：把该会话的 user/assistant 消息注入 a.Initial，
	// 使切换模式时上下文连续（修复"切 Agent 模式后列表无变化"的割裂感）。
	if body.ConversationID != "" {
		if c := s.web.store.get(body.ConversationID); c != nil {
			var initial []agent.Message
			for _, m := range c.Messages {
				initial = append(initial, agent.Message{Role: m.Role, Content: m.Content})
			}
			s.agent.Initial = initial
		}
	}

	// 流式输出：把每步 LLM 的 reply 文本增量 + 思考过程通过 WS 广播给前端。
	// 用 StreamTextExtractor 提取可显示文本（剥掉控制 JSON / 工具调用 JSON）。
	var extr agent.StreamTextExtractor
	var curStep = -1
	s.agent.OnLLMStream = func(step int, d agent.StreamDelta) {
		if step != curStep {
			// 步骤切换：新的一步开始，重置提取器（工具调用后模型会重新输出）
			extr.Reset()
			curStep = step
		}
		if d.Thinking != "" {
			s.broadcastStream(step, d.Thinking, "thinking")
		}
		if d.Content == "" {
			return
		}
		if inc := extr.Feed(d.Content); inc != "" {
			s.broadcastStream(step, inc, "reply")
		}
	}
	defer func() { s.agent.OnLLMStream = nil }()

	reply, err := s.agent.Run(body.Message)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ConversationID != "" {
		s.web.store.appendMsg(body.ConversationID, convMessage{Role: "user", Content: body.Message})
		s.web.store.appendMsg(body.ConversationID, convMessage{Role: "assistant", Content: reply})
		s.web.store.setMode(body.ConversationID, "agent")
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

// handleWebAbort POST /web/api/abort
func (s *Server) handleWebAbort(w http.ResponseWriter, r *http.Request) {
	s.agent.Abort()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWebSteer POST /web/api/steer  { "content": "纠正消息" }
func (s *Server) handleWebSteer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		errJSON(w, http.StatusBadRequest, "content required")
		return
	}
	s.agent.Steer(body.Content)
	s.logf("web agent.steer: %s", truncateStr(body.Content, 80))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSettings GET/POST /web/api/settings → 模型 + 对话参数
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	st := s.web.store
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": st.getSettings(),
			"model":    s.agent.Model(),
		})
	case http.MethodPost:
		var v webSettings
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid json")
			return
		}
		// 空模型 = 保持当前
		if strings.TrimSpace(v.Model) == "" {
			v.Model = s.agent.Model()
		}
		st.saveSettings(v)
		// 应用到 LLM 客户端
		s.applySettings(v)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": v, "model": s.agent.Model()})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// applySettings 把设置应用到 LLM 客户端（模型 + 参数 + 思考等级）。
// 空字段跳过（不覆盖启动时来自 config.json 的默认值）。
func (s *Server) applySettings(v webSettings) {
	type modelSwitcher interface{ SetModel(string) }
	type paramSetter interface{ SetParams(*llm.ChatParams) }
	type thinkingSetter interface{ SetThinking(string) }
	// 模型切换
	if v.Model != "" {
		if ms, ok := s.agent.LLM.(modelSwitcher); ok {
			ms.SetModel(v.Model)
		}
	}
	// 参数设置（全 nil 则跳过，保持默认）
	if v.Temperature != nil || v.TopP != nil || v.MaxTokens > 0 {
		if ps, ok := s.agent.LLM.(paramSetter); ok {
			ps.SetParams(&llm.ChatParams{
				Temperature: v.Temperature,
				TopP:        v.TopP,
				MaxTokens:   v.MaxTokens,
			})
		}
	}
	// 思考等级（空串跳过，不覆盖 config.json 的 thinking_level）
	if v.Thinking != "" {
		if ts, ok := s.agent.LLM.(thinkingSetter); ok {
			ts.SetThinking(v.Thinking)
		}
	}
}

// handleModelsList GET /web/api/models → 从供应商拉取可用模型清单
func (s *Server) handleModelsList(w http.ResponseWriter, r *http.Request) {
	baseURL, apiKey := s.agent.LLMInfo()
	models, err := config.ListModels(baseURL, apiKey)
	if err != nil {
		// 拉取失败不阻断：返回当前模型 + 错误提示
		writeJSON(w, http.StatusOK, map[string]any{
			"models":  []string{s.agent.Model()},
			"current": s.agent.Model(),
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"current": s.agent.Model(),
	})
}

// ApplyStoredSettings 启动时应用持久化的 Web UI 设置（模型/参数/思考等级）。
func (s *Server) ApplyStoredSettings() {
	if s.web == nil {
		return
	}
	s.applySettings(s.web.store.getSettings())
}

// errString 简单 error 包装。
type errString string

func (e errString) Error() string { return string(e) }
