package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"mizar/internal/agent"
)

//go:embed webui
var webuiFS embed.FS

// webSession 一次 Web 登录会话。
type webSession struct {
	username string
	expires  time.Time
}

// webUI Web UI 运行时状态：会话表 + 普通对话历史。
type webUI struct {
	mu       sync.Mutex
	sessions map[string]webSession    // token -> session
	chat     []agent.Message          // 普通对话模式的历史（system + user/assistant）
	sessTTL  time.Duration
}

func newWebUI() *webUI {
	return &webUI{sessions: map[string]webSession{}, sessTTL: 24 * time.Hour}
}

// cookieName 会话 cookie 名。
const webCookieName = "mizar_web_session"

// newToken 生成随机会话 token。
func newToken() string {
	b := make([]byte, 32)
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
	// 对话 / Agent 运行 / 中止
	s.mux.HandleFunc("/web/api/chat", s.webAuth(s.handleWebChat))
	s.mux.HandleFunc("/web/api/run", s.webAuth(s.handleWebRun))
	s.mux.HandleFunc("/web/api/abort", s.webAuth(s.handleWebAbort))
	s.mux.HandleFunc("/web/api/clear", s.webAuth(s.handleWebClear))
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

// handleWebMe GET /web/api/me → 当前登录用户 + 模型
func (s *Server) handleWebMe(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie(webCookieName)
	user := ""
	if c != nil {
		user, _ = s.web.validSession(c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user,
		"model":    s.agent.Model(),
	})
}

// handleWebChat POST /web/api/chat {message} → 普通对话（不调用工具）
func (s *Server) handleWebChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		errJSON(w, http.StatusBadRequest, "message required")
		return
	}
	reply, err := s.chatOnce(body.Message)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

// chatOnce 普通对话：维护独立历史，直接调 LLM（无工具循环）。
func (s *Server) chatOnce(msg string) (string, error) {
	u := s.web
	u.mu.Lock()
	if len(u.chat) == 0 {
		u.chat = append(u.chat, agent.Message{Role: agent.RoleSystem,
			Content: "你是开阳（Mizar）的普通对话助手。直接、简洁地回答用户问题，用中文。不调用任何工具。"})
	}
	u.chat = append(u.chat, agent.Message{Role: agent.RoleUser, Content: msg})
	hist := make([]agent.Message, len(u.chat))
	copy(hist, u.chat)
	u.mu.Unlock()

	llm := s.agent.LLM
	if llm == nil {
		return "", errString("LLM 未初始化")
	}
	reply, err := llm.Chat(hist)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	u.chat = append(u.chat, agent.Message{Role: agent.RoleAssistant, Content: reply})
	// 简单截断：历史过长时保留最近 40 条（对话模式无需复杂压缩）
	if len(u.chat) > 40 {
		u.chat = append(u.chat[:1], u.chat[len(u.chat)-39:]...)
	}
	u.mu.Unlock()
	return reply, nil
}

// handleWebRun POST /web/api/run {message} → Agent 模式（完整工具循环）
func (s *Server) handleWebRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		errJSON(w, http.StatusBadRequest, "message required")
		return
	}
	s.logf("web agent.run: %s", truncateStr(body.Message, 80))
	reply, err := s.agent.Run(body.Message)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

// handleWebAbort POST /web/api/abort
func (s *Server) handleWebAbort(w http.ResponseWriter, r *http.Request) {
	s.agent.Abort()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWebClear POST /web/api/clear → 清空普通对话历史
func (s *Server) handleWebClear(w http.ResponseWriter, r *http.Request) {
	if s.web != nil {
		s.web.mu.Lock()
		s.web.chat = nil
		s.web.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// errString 简单 error 包装。
type errString string

func (e errString) Error() string { return string(e) }
