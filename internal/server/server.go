package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
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

	// busyRun 标记「已抢到 runMu、但 Run() 可能还没置 runActive」的窗口期。
	// runActive 只覆盖 Run() 体内，acquireRun 成功到 Run 真正开始之间有一小段，
	// 此时 agent.Running() 仍为 false。用户在这个窗口发纠正消息会被误判成
	// 「没有活跃循环」而提示排队（实际这一轮已经开始）。判断活跃统一用 runLive()。
	busyRun atomic.Bool

	// runConv 当前正在执行 Agent.Run 的 Web 会话 id（空 = 非 Web 会话发起的运行）。
	// WS 事件会带上该 id，前端据此把流式增量只渲染到「发起该运行的那个会话窗口」，
	// 修复多会话切换时事件串台（新会话的消息出现在老会话窗口里）。
	runConv atomic.Value // string

	// ask 待回答的 ask_user_question 问题（Web 问答通道，见 ask.go）。
	ask *askBroker

	// evt 会话事件回放日志（切后台/断线期间的事件补齐，见 evtlog.go）。
	evt *evtLog

	// draft 运行中答复草稿（页面被刷新/杀掉后不丢已生成内容，见 drafts.go）。
	draft *draftStore

	// stats Web UI 状态栏用量统计（tokens/缓存/速度），对齐 TUI 状态栏。
	stats runStats
	// usageMu 保护 lastUsage/lastModel（去重入账）。
	usageMu   sync.Mutex
	lastUsage *agent.Usage
	lastModel string
	// runStartAt 最近一次 Run 开始时间（零值=未开始），用于计算本次请求耗时。
	runStartAt time.Time
}

// setRunConv 记录/清除当前运行归属的会话 id（空串表示无归属）。
func (s *Server) setRunConv(id string) { s.runConv.Store(id) }

// RunConv 返回当前运行归属的 Web 会话 id（无归属返回空串）。
func (s *Server) RunConv() string {
	if v, ok := s.runConv.Load().(string); ok {
		return v
	}
	return ""
}

// handleWebRun 的一次运行是否“在跑”：runActive 只管 Run() 体内，
// acquireRun 成功到 Run() 执行 runActive.Store(true) 之间有一个窗口，
// 此时 agent.Running() 还是 false，但这一轮确实已经开始（消息已受理、
// 会话归属已记录）。用户在这个窗口里发纠正消息，若只看 Running() 会误判
// 成「没有活跃循环」→ 前端提示「已排队，本轮结束后发送」，与事实相反。
// busyRun 标记这个窗口，判断活跃一律用 runLive()。
func (s *Server) runLive() bool {
	if s.busyRun.Load() {
		return true
	}
	return s.agent != nil && s.agent.Running()
}

func (s *Server) acquireRun() bool {
	if !s.runMu.TryLock() {
		return false
	}
	// 抢到锁即视为一轮已开始（Run 可能还没跑到置 runActive 的地方）
	s.busyRun.Store(true)
	return true
}

// releaseRun 释放 Agent 独占。
func (s *Server) releaseRun() {
	s.busyRun.Store(false)
	s.runMu.Unlock()
}

// New 创建 Server。
func New(cfg *Config, a *agent.Agent, st *session.Store) *Server {
	s := &Server{
		cfg:     cfg,
		agent:   a,
		session: st,
		mux:     http.NewServeMux(),
		hub:     newHub(),
		web:     newWebUI(webStorePath()),
		ask:     newAskBroker(),
		evt:     newEvtLog(),
		draft:   newDraftStore(),
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
// 事件携带当前运行的会话 id（conversation_id），前端按当前打开的会话过滤，
// 避免切换会话期间旧运行的增量被渲染到新/别的会话窗口里。
func (s *Server) broadcastStream(step int, delta, kind string) {
	// 正文增量同时写草稿：页面被系统杀掉/用户刷新时，重开还能看到已生成的进度。
	if kind == "reply" {
		s.draft.append(s.RunConv(), delta)
	}
	s.emit(map[string]any{
		"type": "event", "event": "stream", "step": step, "delta": delta, "kind": kind,
		"conversation_id": s.RunConv(),
	})
}

// ============================================================================
// 运行统计（Web UI 状态栏，对齐 TUI 状态栏展示内容）
// ============================================================================

// runStats 会话级用量统计（进程级累计，模型切换时重置模型维度速度）。
type runStats struct {
	mu sync.Mutex

	CumPrompt     int
	CumCompletion int
	CumTotal      int
	CumCached     int

	// 模型维度平滑速度（累计输出 tokens / 累计耗时秒）
	ModelCumCompletion int
	ModelCumDuration   float64
	ModelTokPerSec     float64

	// 最后一次请求耗时
	LastDurationSec float64
}

// addUsage 累加一次用量。
func (r *runStats) addUsage(u *agent.Usage, durSec float64) {
	if u == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.CumPrompt += u.PromptTokens
	r.CumCompletion += u.CompletionTokens
	r.CumTotal += u.TotalTokens
	r.CumCached += u.CachedTokens
	r.LastDurationSec = durSec
	r.ModelCumCompletion += u.CompletionTokens
	r.ModelCumDuration += durSec
	if r.ModelCumDuration > 0 {
		r.ModelTokPerSec = float64(r.ModelCumCompletion) / r.ModelCumDuration
	}
}

// resetModel 切换模型时重置模型维度速度统计。
func (r *runStats) resetModel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ModelCumCompletion = 0
	r.ModelCumDuration = 0
	r.ModelTokPerSec = 0
}

// snapshot 返回统计快照。
func (r *runStats) snapshot() runStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runStats{
		CumPrompt:       r.CumPrompt,
		CumCompletion:   r.CumCompletion,
		CumTotal:        r.CumTotal,
		CumCached:       r.CumCached,
		ModelTokPerSec:  r.ModelTokPerSec,
		LastDurationSec: r.LastDurationSec,
	}
}

// recordLLMUsage 从 LLM 客户端读取最后一次用量并入账（若支持 UsageTracker）。
// afterModelChange 检测模型变化并重置速度统计。
func (s *Server) recordLLMUsage() {
	if s.agent == nil || s.agent.LLM == nil {
		return
	}
	ut, ok := s.agent.LLM.(interface{ LastUsage() *agent.Usage })
	if !ok {
		return
	}
	u := ut.LastUsage()
	if u == nil {
		return
	}
	// 去重：同一 usage 对象重复读取（多入口调用）不重复入账。
	s.usageMu.Lock()
	same := s.lastUsage == u || (s.lastUsage != nil && *s.lastUsage == *u)
	if same {
		s.usageMu.Unlock()
		return
	}
	cp := *u
	s.lastUsage = &cp
	s.usageMu.Unlock()

	if m := s.agent.Model(); m != s.lastModel {
		if s.lastModel != "" {
			s.stats.resetModel()
		}
		s.lastModel = m
	}
	dur := 0.0
	if !s.runStartAt.IsZero() {
		dur = time.Since(s.runStartAt).Seconds()
		s.runStartAt = time.Time{}
	}
	s.stats.addUsage(u, dur)
}
