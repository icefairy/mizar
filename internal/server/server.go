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
	// frontend 前端资源来源（P0 外部化：显式目录 > ~/.mizar/webui/ > embed）。
	frontend frontendSource
	// runMu 串行化 Agent.Run：Agent 实例内的 Initial / OnLLMStream / steer 槽位
	// 都是共享可变状态，多个入口（/v1/chat/completions、/rpc、/web/api/run）
	// 并发调用会互相覆盖。用 TryLock 而非 Lock——忙时立即返回 409，
	// 避免请求无限排队；steer/abort 不走此锁，等待期间仍可实时纠正。
	runMu sync.Mutex

	// runLocks 按会话的独占锁（多会话并发）。同一会话串行，不同会话互不阻塞。
	// runMu 仍保留：非 Web 入口（convID 为空）走它，与改造前行为一致。
	runLocksMu sync.Mutex
	runLocks   map[string]*sync.Mutex

	// maxConcurrentRuns 全局并发上限（跨会话），0 = 不限。
	// 对齐 Hermes gateway 的 max_concurrent_sessions：防止一次开几十个会话
	// 把上游 LLM 与内存打爆。
	maxConcurrentRuns int
	runCountMu        sync.Mutex
	runCount          int

	// busyRun 标记「已抢到 runMu、但 Run() 可能还没置 runActive」的窗口期。
	// runActive 只覆盖 Run() 体内，acquireRun 成功到 Run 真正开始之间有一小段，
	// 此时 agent.Running() 仍为 false。用户在这个窗口发纠正消息会被误判成
	// 「没有活跃循环」而提示排队（实际这一轮已经开始）。判断活跃统一用 runLive()。
	busyRun atomic.Bool

	// busyConvs 同上，但按会话记录（多会话并发下 busyRun 这个全局布尔会让
	// 「A 刚起步、B 空闲」被误判成 B 也在跑）。Web 入口按会话查这个集合。
	busyMu    sync.Mutex
	busyConvs map[string]bool

	// runConv 当前正在执行 Agent.Run 的 Web 会话 id（空 = 非 Web 会话发起的运行）。
	// WS 事件会带上该 id，前端据此把流式增量只渲染到「发起该运行的那个会话窗口」，
	// 修复多会话切换时事件串台（新会话的消息出现在老会话窗口里）。
	// 注意：多会话并发时它只反映「最后一次置位」，真正的归属判断用 runningConvs。
	runConv atomic.Value // string

	// runningConvs 正在运行的会话 id 集合（多会话并发）。与 runConv 并存：
	// runConv 保留给单运行入口做快速归属判断，集合用于状态查询与并发上报。
	runningMu    sync.Mutex
	runningConvs map[string]bool

	// ask 待回答的 ask_user_question 问题（Web 问答通道，见 ask.go）。
	ask *askBroker

	// pool 按会话提供独立的 Agent 实例，支持多会话真并发（见 agentpool.go）。
	// 为空时退回 s.agent 单实例（老行为），保证非 Web 入口不受影响。
	pool *agentPool

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
	if a := s.agentFor(s.RunConv()); a != nil {
		return a.Running()
	}
	return s.agent != nil && s.agent.Running()
}

// runLiveFor 该会话是否正在运行（多会话并发下按会话判断）。
// convID 为空时退回全局判断（非 Web 入口）。
//
// 注意：必须把 busyRun 算进来。acquireRunFor 成功到 agent.Run() 真正置
// runActive 之间有一个窗口（首个 LLM 请求还没返回时最典型，长思考模型可达数十秒），
// 这段里 a.Running() 仍是 false，但这一轮早已开始、steer 完全能生效。
// 只看 Running() 会让打断模式下的纠正被误报成「排队等本轮结束」——
// v0.8.2 修过全局 runLive() 的这个坑，按会话判断的这里当时漏了。
func (s *Server) runLiveFor(convID string) bool {
	if convID == "" {
		return s.runLive()
	}
	if s.isBusyConv(convID) {
		return true
	}
	if s.pool != nil {
		if a := s.pool.peek(convID); a != nil {
			return a.Running()
		}
		return false
	}
	return s.runLive()
}

// agentFor 取该会话专属的 Agent 实例（没有则克隆）。
// 非 Web 入口（convID 空）返回主实例，行为与改造前一致。
func (s *Server) agentFor(convID string) *agent.Agent {
	if s.pool != nil {
		a := s.pool.forConv(convID)
		// 每次取实例都刷新工作目录：根目录可能刚在设置里改过；目录名固定为 convID。
		// WorkDir 同时用于系统提示与工具 cwd（CallInDir），二者必须一致。
		if a != nil && convID != "" {
			if dir, err := s.convWorkspaceDir(convID); err == nil {
				a.WorkDir = dir
			}
		}
		return a
	}
	return s.agent
}

// acquireRun 尝试独占「该会话」的 Agent。
//
// 多会话并发语义（对齐 Hermes gateway 的 max_concurrent_sessions）：
// 不同会话各自用各自的 Agent 实例，互不阻塞；同一会话仍然串行
// （一个会话里同时跑两个循环必然互相覆盖上下文）。
//
// 全局并发上限由 maxConcurrentRuns 兜底（0 = 不限），防止一次开几十个会话
// 把上游 LLM 和内存打爆。
func (s *Server) acquireRun() bool { return s.acquireRunFor(s.RunConv()) }

func (s *Server) acquireRunFor(convID string) bool {
	s.runLocksMu.Lock()
	if s.runLocks == nil {
		s.runLocks = map[string]*sync.Mutex{}
	}
	lk := s.runLocks[convID]
	if lk == nil {
		lk = &sync.Mutex{}
		s.runLocks[convID] = lk
	}
	s.runLocksMu.Unlock()
	if !lk.TryLock() {
		// 同一会话已有一轮在跑
		return false
	}
	// 全局并发上限（跨会话）
	if cap := s.maxConcurrentRuns; cap > 0 {
		s.runCountMu.Lock()
		if s.runCount >= cap {
			s.runCountMu.Unlock()
			lk.Unlock()
			return false
		}
		s.runCount++
		s.runCountMu.Unlock()
	}
	// 抢到锁即视为一轮已开始（Run 可能还没跑到置 runActive 的地方）
	s.busyRun.Store(true)
	s.setBusyConv(convID, true)
	return true
}

// setBusyConv 记录/清除某会话的「已开始但 Run 还没置 runActive」窗口期。
func (s *Server) setBusyConv(convID string, on bool) {
	if convID == "" {
		return
	}
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	if s.busyConvs == nil {
		s.busyConvs = map[string]bool{}
	}
	if on {
		s.busyConvs[convID] = true
	} else {
		delete(s.busyConvs, convID)
	}
}

// isBusyConv 该会话是否处于「已开始、但 Run 还没置 runActive」的窗口期。
func (s *Server) isBusyConv(convID string) bool {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	return s.busyConvs[convID]
}

// releaseRun 释放该会话的独占。
func (s *Server) releaseRun() { s.releaseRunFor(s.RunConv()) }

func (s *Server) releaseRunFor(convID string) {
	s.runLocksMu.Lock()
	lk := s.runLocks[convID]
	s.runLocksMu.Unlock()
	if s.maxConcurrentRuns > 0 {
		s.runCountMu.Lock()
		if s.runCount > 0 {
			s.runCount--
		}
		s.runCountMu.Unlock()
	}
	s.busyRun.Store(false)
	s.setBusyConv(convID, false)
	if lk != nil {
		lk.Unlock()
	}
}

// firstRunningConv 返回任一在跑的会话 id（多会话并发下「running_conv」单值语义
// 已不够用，保留该字段兼容旧前端；新前端读 running_convs 数组）。
func (s *Server) firstRunningConv() string {
	s.runningMu.Lock()
	n := len(s.runningConvs)
	s.runningMu.Unlock()
	// 集合为空时退回 runConv：非 Web 入口与老调用路径只设 runConv 不设集合。
	if n == 0 {
		return s.RunConv()
	}
	if id := s.RunConv(); id != "" && s.isConvRunning(id) {
		return id
	}
	for _, id := range s.runningConvsSnapshot() {
		return id
	}
	return s.RunConv()
}

// markRunning 维护「哪些会话正在跑」的集合（多会话并发时单个 runConv 不够用）。
func (s *Server) markRunning(convID string, on bool) {
	if convID == "" {
		return
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.runningConvs == nil {
		s.runningConvs = map[string]bool{}
	}
	if on {
		s.runningConvs[convID] = true
	} else {
		delete(s.runningConvs, convID)
	}
}

// runningConvsSnapshot 当前在跑的会话 id 列表（前端状态栏据此显示）。
func (s *Server) runningConvsSnapshot() []string {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	out := make([]string, 0, len(s.runningConvs))
	for id := range s.runningConvs {
		out = append(out, id)
	}
	return out
}

// isConvRunning 指定会话是否在跑。
func (s *Server) isConvRunning(convID string) bool {
	if convID == "" {
		return false
	}
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	return s.runningConvs[convID]
}

// busyRunActive 当前是否有任意会话在跑（用于 /status 的全局 running）。
func (s *Server) anyRunLive() bool {
	if s.busyRun.Load() {
		return true
	}
	if s.pool != nil {
		if len(s.pool.busyConvs()) > 0 {
			return true
		}
	}
	return s.agent != nil && s.agent.Running()
}

// New 创建 Server。
func New(cfg *Config, a *agent.Agent, st *session.Store) *Server {
	s := &Server{
		cfg:      cfg,
		agent:    a,
		session:  st,
		mux:      http.NewServeMux(),
		hub:      newHub(),
		web:      newWebUI(webStorePath()),
		ask:      newAskBroker(),
		evt:      newEvtLog(),
		draft:    newDraftStore(),
		runLocks: map[string]*sync.Mutex{},
	}
	s.wsUp = newWSUpgrader(cfg.Token)
	s.pool = newAgentPool(a)
	s.maxConcurrentRuns = cfg.MaxConcurrentRuns
	s.frontend = s.resolveFrontend()
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
	// 插件 RPC 的 REST 直达（/ext/<name>，Bearer 鉴权）
	s.mux.HandleFunc("/ext/", s.auth(s.handleExtRPC))
	// WebSocket
	s.mux.HandleFunc("/ws", s.auth(s.handleWS))
	// 管理：动态开关
	s.mux.HandleFunc("/admin/status", s.auth(s.handleStatus))
	s.mux.HandleFunc("/admin/switch", s.auth(s.handleSwitch))
	s.mux.HandleFunc("/admin/routes", s.auth(s.handleRoutes))
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

// broadcastStreamFor 广播流式增量（step + 可显示文本增量 + 是否思考过程）。
//
// ⚠ convID 必须由调用方显式传入，**不能用 s.RunConv()**。
// RunConv 是进程级单值，多会话并发时会被后开始的那一轮覆盖：A 会话正在输出、
// 用户切到 B 发问，则 A 后续的每个增量都会被贴上 B 的 id —— 表现为
// 「B 的响应全跑到 A 的对话框里，B 自己反而一直空白」。这是 2026-11-14 用户实测
// 报告的问题，根因就在于这里读全局态。
func (s *Server) broadcastStreamFor(convID string, step int, delta, kind string) {
	// 正文增量同时写草稿：页面被系统杀掉/用户刷新时，重开还能看到已生成的进度。
	if kind == "reply" {
		s.draft.append(convID, delta)
	}
	s.emit(map[string]any{
		"type": "event", "event": "stream", "step": step, "delta": delta, "kind": kind,
		"conversation_id": convID,
	})
}

// broadcastStream 兼容旧调用路径（非 Web 入口只有单运行，RunConv 可信）。
func (s *Server) broadcastStream(step int, delta, kind string) {
	s.broadcastStreamFor(s.RunConv(), step, delta, kind)
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
