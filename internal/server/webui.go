package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// frontendSource 前端资源的运行时来源（/web/api/frontend 探测端点用）。
type frontendSource struct {
	fs   fs.FS
	kind string // "embed" | "dir"
	dir  string // kind="dir" 时的目录，embed 时为空
}

// resolveFrontend 解析前端资源来源（P0 外部化）：
// 显式 FrontendDir > ~/.mizar/webui/ > 内嵌 embed（fallback）。
// 外部目录必须存在且含 index.html 才被采纳——否则回退 embed，行为与旧版一致。
func (s *Server) resolveFrontend() frontendSource {
	var candidates []string
	if d := strings.TrimSpace(s.cfg.Web.FrontendDir); d != "" {
		candidates = append(candidates, d)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".mizar", "webui"))
	}
	for _, d := range candidates {
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil || len(entries) == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
			// 不是合法前端目录（缺 index.html）→ 跳过，不拿错目录去服务。
			continue
		}
		s.logf("webui: 使用外部前端目录 %s（改动文件即时生效，无需重编译）", abs)
		return frontendSource{fs: os.DirFS(abs), kind: "dir", dir: abs}
	}
	sub, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		s.logf("webui embed 失败: %v", err)
		return frontendSource{}
	}
	return frontendSource{fs: sub, kind: "embed"}
}

// handleWebFrontend GET /web/api/frontend → 前端资源来源探测。
// 自定义前端时独立恢复 URL 用的判断点：source=dir 时改动落盘即生效。
func (s *Server) handleWebFrontend(w http.ResponseWriter, r *http.Request) {
	src := s.frontend
	writeJSON(w, http.StatusOK, map[string]any{
		"source":  src.kind,
		"dir":     src.dir,
		"version": s.cfg.Version,
	})
}

// ReleaseFrontend 把内嵌前端（webui/）释放到目标目录，返回写出的文件数。
// 供 `mizar -webui-out <dir>` 使用：用户拿到 index.html/app.js/style.css 后
// 就地修改，-serve 启动时 FrontendDir/`~/.mizar/webui/` 命中即外部目录生效，
// 不必为改一行前端重编译二进制。目标目录已存在的同名文件会被覆盖（幂等）。
func ReleaseFrontend(dir string) (int, error) {
	sub, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(p)
		dest := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
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
	// 静态资源（登录页本身不需要鉴权；数据接口才需要）。
	// 前端来源在 New() 已解析：显式 FrontendDir > ~/.mizar/webui/ > 内嵌 embed。
	if s.frontend.fs == nil {
		s.logf("webui 前端资源不可用（embed 与外部目录均失败）")
		return
	}
	fileServer := http.FileServer(http.FS(s.frontend.fs))
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
	s.mux.HandleFunc("/web/api/version", s.handleWebVersion)
	s.mux.HandleFunc("/web/api/me", s.webAuth(s.handleWebMe))
	// 会话 CRUD
	s.mux.HandleFunc("/web/api/conversations", s.webAuth(s.handleConversations))
	s.mux.HandleFunc("/web/api/conversation", s.webAuth(s.handleConversation))
	// 对话 / Agent 运行 / 中止 / 纠正
	s.mux.HandleFunc("/web/api/chat", s.webAuth(s.handleWebChat))
	s.mux.HandleFunc("/web/api/run", s.webAuth(s.handleWebRun))
	s.mux.HandleFunc("/web/api/abort", s.webAuth(s.handleWebAbort))
	s.mux.HandleFunc("/web/api/steer", s.webAuth(s.handleWebSteer))
	// 事件回放：断线/切后台期间缺失的流式事件（前端带 after=seq 补齐）
	s.mux.HandleFunc("/web/api/events", s.webAuth(s.handleWebEvents))
	// 运行中草稿：页面刷新/被杀后重开时先显示已有进度
	s.mux.HandleFunc("/web/api/draft", s.webAuth(s.handleWebDraft))
	// ask_user_question 问答通道：作答 + 恢复未答问题（刷新页面后重弹）
	s.mux.HandleFunc("/web/api/answer", s.webAuth(s.handleWebAnswer))
	s.mux.HandleFunc("/web/api/ask", s.webAuth(s.handleWebAskPending))
	// 设置（模型 + 对话参数）
	s.mux.HandleFunc("/web/api/settings", s.webAuth(s.handleSettings))
	// 工作空间（上传文件按会话落盘，Agent 文件工具以该目录为 cwd）
	s.mux.HandleFunc("/web/api/workspace/root", s.webAuth(s.handleWorkspaceRoot))
	s.mux.HandleFunc("/web/api/workspace/files", s.webAuth(s.handleWorkspaceFiles))
	s.mux.HandleFunc("/web/api/workspace/upload", s.webAuth(s.handleWorkspaceUpload))
	s.mux.HandleFunc("/web/api/workspace/download", s.webAuth(s.handleWorkspaceDownload))
	s.mux.HandleFunc("/web/api/workspace/file", s.webAuth(s.handleWorkspaceDelete))
	s.mux.HandleFunc("/web/api/models", s.webAuth(s.handleModelsList))
	// 供应商收藏夹（多 provider 管理）
	s.mux.HandleFunc("/web/api/providers", s.webAuth(s.handleWebProviders))
	// 状态栏统计（对齐 TUI 状态栏：tokens/上下文/缓存/速度）
	s.mux.HandleFunc("/web/api/status", s.webAuth(s.handleWebStatus))
	// 前端资源来源探测（embed / 外部目录）
	s.mux.HandleFunc("/web/api/frontend", s.webAuth(s.handleWebFrontend))
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

// handleWebVersion GET /web/api/version → 版本号（登录页展示用，无需鉴权）。
func (s *Server) handleWebVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": s.cfg.Version})
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
		"version":  s.cfg.Version,
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
		// 联动清理该会话的工作空间目录（只删 <root>/<convID> 单层，删除前校验边界）。
		s.removeConvWorkspace(id)
		// 释放该会话的 Agent 实例（2026-11-15 审计修复：agentPool.drop 此前
		// 零调用 → byConv 只增不减，每个实例都持有 Initial 历史，长期跑内存只涨不落）。
		if s.pool != nil {
			s.pool.drop(id)
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
		// 用户手动改名：之后不再自动覆盖
		st.markTitleManual(id, true)
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
		if body.ConversationID != "" {
			s.web.store.appendMsg(body.ConversationID, convMessage{Role: "user", Content: body.Message})
			s.web.store.appendMsg(body.ConversationID, convMessage{Role: "assistant", Content: out})
			s.web.store.setMode(body.ConversationID, "chat")
		}
		writeJSON(w, http.StatusOK, map[string]any{"reply": out, "command": true, "command_display": true})
		return
	}
	// 普通对话也记录归属会话，使流式事件带上 conversation_id（前端按会话过滤）
	s.setRunConv(body.ConversationID)
	defer s.setRunConv("")
	// 纳入「正在运行的会话」集合：普通对话不开 Agent 循环，但同样有流式输出，
	// 前端的会话切换/断线补齐判断依赖 runningConvs。不登记的话，A 会话跑普通对话、
	// 用户切到 B 再切回 A 时会被判成「没在跑」，既不重放缓存也不补齐事件。
	if body.ConversationID != "" {
		s.markRunning(body.ConversationID, true)
		defer s.markRunning(body.ConversationID, false)
	}
	// 同会话串行 + 置 busyRun：普通对话也要占这把按会话的锁，否则
	// runLiveFor() 读不到「正在跑」，导致打断模式下发来的纠正被判成
	// running=false → 前端显示排队（实际这一轮明明还在跑）。
	// 同时防止同一会话并发两次 /web/api/chat 把历史写乱。
	if !s.acquireRunFor(body.ConversationID) {
		errJSON(w, http.StatusConflict, "该会话正在处理上一条消息，请稍候或发送纠正消息")
		return
	}
	defer s.releaseRunFor(body.ConversationID)
	if body.ConversationID != "" {
		s.evt.reset(body.ConversationID)
	}
	s.runStartAt = time.Now()
	reply, err := s.chatOnce(body.ConversationID, body.Message)
	s.recordLLMUsage()
	if body.ConversationID != "" {
		s.draft.finish(body.ConversationID)
		s.emit(map[string]any{
			"type": "event", "event": "run_done", "conversation_id": body.ConversationID,
			"ok": err == nil,
		})
	}
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
				s.broadcastStreamFor(convID, 0, d.Thinking, "thinking")
			}
			if d.Content != "" {
				s.broadcastStreamFor(convID, 0, d.Content, "reply")
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
		s.maybeAutoTitle(convID)
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
		writeJSON(w, http.StatusOK, map[string]any{"reply": out, "command": true, "command_display": true})
		return
	}
	s.logf("web agent.run: %s", truncateStr(body.Message, 80))

	// 同一会话串行、不同会话并发：每个会话用自己的 Agent 实例与独占锁。
	// 忙时返回 409（同一会话重复发才会命中），steer/abort 仍可实时生效。
	if !s.acquireRunFor(body.ConversationID) {
		errJSON(w, http.StatusConflict, "该会话正在处理上一条消息，请稍候或发送纠正消息")
		return
	}
	// 该会话专属的 Agent 实例（首次使用自动克隆，共享 LLM/插件/Jobs 等进程级服务）
	ag := s.agentFor(body.ConversationID)
	if ag == nil {
		s.releaseRunFor(body.ConversationID)
		errJSON(w, http.StatusInternalServerError, "agent 未初始化")
		return
	}
	// 记录本次运行的归属会话：WS 事件带 conversation_id，前端据此过滤避免串台。
	s.setRunConv(body.ConversationID)
	// 新一轮开始：清空该会话的事件回放缓冲（上一轮的增量已并入落盘历史）。
	// seq 不清零，保持单调，避免前端游标回退导致重复重放。
	if body.ConversationID != "" {
		s.evt.reset(body.ConversationID)
	}
	convID := body.ConversationID
	defer func() { s.setRunConv(""); s.releaseRunFor(convID) }()

	// Agent 模式复用会话历史：把该会话的 user/assistant 消息注入 a.Initial，
	// 使切换模式时上下文连续（修复"切 Agent 模式后列表无变化"的割裂感）。
	// 只注入正文消息：Kind=tool 的工具过程是展示用的，塞进 LLM 上下文会破坏
	// OpenAI 协议（tool 消息必须紧跟带 tool_calls 的 assistant 消息）。
	if body.ConversationID != "" {
		if c := s.web.store.get(body.ConversationID); c != nil {
			var initial []agent.Message
			for _, m := range c.Messages {
				if !m.isText() {
					continue
				}
				initial = append(initial, agent.Message{Role: m.Role, Content: m.Content})
			}
			ag.Initial = initial
		}
	}

	// 工具调用过程落盘：模型说的一句说明（reason）+ 调用参数 + 真实输出。
	// 页面刷新后 /web/api/conversation 能读回这些过程，不再只剩一问一答。
	// 注意：回调在工具执行线程同步调用，webStore 内部只 append + 落盘，量小可接受。
	ag.OnToolExchange = func(tool, args, reason, out string, execErr error) {
		s.web.store.appendToolMsg(convID, tool, args, reason, out, execErr)
	}
	defer func() { ag.OnToolExchange = nil }()

	// 流式输出：把每步 LLM 的 reply 文本增量 + 思考过程通过 WS 广播给前端。
	// 用 StreamTextExtractor 提取可显示文本（剥掉控制 JSON / 工具调用 JSON）。
	// 每个会话的 Agent 实例各有自己的 OnLLMStream，闭包独占 extr/curStep，
	// 并发跑多个会话时互不干扰（共享一个实例才会串台）。
	var extr agent.StreamTextExtractor
	var curStep = -1
	ag.OnLLMStream = func(step int, d agent.StreamDelta) {
		if step != curStep {
			// 步骤切换：新的一步开始，重置提取器（工具调用后模型会重新输出）
			extr.Reset()
			curStep = step
		}
		// 显式传本会话 id：不能用 s.RunConv()（多会话并发时会被别的会话覆盖）。
		if d.Thinking != "" {
			s.broadcastStreamFor(convID, step, d.Thinking, "thinking")
		}
		if d.Content == "" {
			return
		}
		if inc := extr.Feed(d.Content); inc != "" {
			s.broadcastStreamFor(convID, step, inc, "reply")
		}
	}
	defer func() { ag.OnLLMStream = nil }()

	// 会话级运行归属：多会话并发时分不清「谁在跑」，用集合维护。
	s.markRunning(body.ConversationID, true)
	defer s.markRunning(body.ConversationID, false)

	s.runStartAt = time.Now()
	// 用户消息先落盘：这样本轮运行期间新增的 steer/排队消息能按正确顺序
	// 追加在它之后（此前是 Run 结束后才写 user+assistant，导致 steer 消息
	// 排在用户原始提问之前，历史顺序错乱）。
	if body.ConversationID != "" {
		s.web.store.appendMsg(body.ConversationID, convMessage{Role: "user", Content: body.Message})
		s.web.store.setMode(body.ConversationID, "agent")
	}
	reply, err := ag.Run(body.Message)
	s.recordLLMUsage()
	// 本轮结束：草稿已并入正式答复（落盘历史），清掉临时文件
	if body.ConversationID != "" {
		s.draft.finish(body.ConversationID)
		// 广播「本轮结束」：页面在后台被系统杀掉/刷新时，发起请求的那个标签页
		// 已经收不到 HTTP 响应，只能靠这条事件知道「跑完了，该刷新历史了」。
		s.emit(map[string]any{
			"type": "event", "event": "run_done", "conversation_id": body.ConversationID,
			"aborted": errors.Is(err, agent.ErrAborted), "ok": err == nil,
		})
	}
	if err != nil {
		// 用户主动停止（/stop、中止按钮）不是错误：返回 200 + aborted 标记，
		// 前端据此显示「已停止」而不是一条刺眼的报错。
		if errors.Is(err, agent.ErrAborted) {
			writeJSON(w, http.StatusOK, map[string]any{"reply": "", "aborted": true})
			return
		}
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ConversationID != "" {
		s.web.store.appendMsg(body.ConversationID, convMessage{Role: "assistant", Content: reply})
		s.maybeAutoTitle(body.ConversationID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
}

// handleWebAbort POST /web/api/abort
// 中止当前运行，并丢弃尚未被消费的 steer——否则停止后残留的纠正消息会被
// 下一次 Run 当作待注入消息消费（表现为「停止后新任务莫名其妙换了方向」）。
func (s *Server) handleWebAbort(w http.ResponseWriter, r *http.Request) {
	// 按会话中止：多会话并发时不能把别人的循环一起停掉
	var body struct {
		ConversationID string `json:"conversation_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ag := s.agentFor(body.ConversationID)
	if ag != nil {
		ag.Abort()
		ag.ClearSteer()
	} else {
		s.agent.Abort()
		s.agent.ClearSteer()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWebSteer POST /web/api/steer  { "content": "纠正消息", "conversation_id": "<可选>" }
// 响应 { ok, running }：running=false 表示当前没有活跃循环（或活跃循环属于别的会话），
// 该消息不会被消费，前端应改作新任务补发（避免用户刚发的纠正被静默丢弃/串到别的会话）。
func (s *Server) handleWebSteer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content        string `json:"content"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Content) == "" {
		errJSON(w, http.StatusBadRequest, "content required")
		return
	}
	// 归属判断：多会话并发下按会话查，不再用全局 runConv（并发时会互相覆盖）
	running := s.runLiveFor(body.ConversationID)
	if body.ConversationID == "" {
		// 非 Web 入口：沿用全局判断
		running = s.runLive()
	}
	if running {
		ag := s.agentFor(body.ConversationID)
		if ag == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": false})
			return
		}
		ag.Steer(body.Content)
		// 落盘到会话历史：否则刷新页面后这条纠正消息就不见了（只有内存里被循环消费）。
		if body.ConversationID != "" {
			s.web.store.appendMsg(body.ConversationID, convMessage{Role: "user", Content: body.Content})
		}
		s.logf("web agent.steer: %s", truncateStr(body.Content, 80))
	} else {
		s.logf("web agent.steer (no active run, deferred): %s", truncateStr(body.Content, 80))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": running})
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
		// 工作空间根目录：空=默认；非空必须是可创建/可写的绝对路径。
		root, rerr := validateWorkspaceRoot(v.WorkspaceRoot)
		if rerr != nil {
			errJSON(w, http.StatusBadRequest, rerr.Error())
			return
		}
		v.WorkspaceRoot = root
		st.saveSettings(v)
		// 应用到 LLM 客户端
		s.applySettings(v)
		// 应用缓存命中率提示开关（进程级）
		s.setCacheMissEnabled(v.ShowCacheMiss)
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

// setCacheMissEnabled 设置缓存命中率下跌提示开关（进程级）。
//
// 2026-11-15 审计修复：此前是空操作（`_ = v`），WebUI 又没有对应开关 →
// show_cache_miss 字段形同虚设。现在真正落到 agent 包的进程级开关上。
func (s *Server) setCacheMissEnabled(v bool) {
	agent.SetCacheMissNoticeEnabled(v)
}

// errString 简单 error 包装。
type errString string

func (e errString) Error() string { return string(e) }
