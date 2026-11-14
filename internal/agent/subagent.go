// Package agent 实现子代理委派（对齐 hermes delegate_tool / subagent_lifecycle）。
//
// 设计要点（v0.8.7 重写，修掉此前「僵尸骨架」）：
//   - 子 agent = 独立的 Agent 实例，跑原生 Run() 主循环——自带工具解析/执行/
//     纠错/压缩/步数预算，不再另写第二套解析循环（两套逻辑迟早漂移）。
//   - LLM 与插件注册表共享（进程级服务）；工具清单里过滤掉委派类工具，防无限递归。
//   - 运行态（Initial / 回调 / 取消槽）天然隔离：每个子 agent 一个 New() 实例。
//   - delegate_task 异步返回句柄：Plugins.Call 有 30s 硬超时，同步等子任务必被砍。
//   - 结果回灌：复用 Jobs.DrainDone() 已挂进主循环(loop.go:655)的完成通知通道，
//     零主循环改动即可让结果自动进主对话。
//   - tasks[] 一次 fan-out 多个并发跑；并发上限 semaphore + 超时 context + 真 Cancel。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mizar/internal/plugins"
)

// ============================================================================
// 状态与句柄
// ============================================================================

type SubagentState string

const (
	SubagentPending   SubagentState = "PENDING"
	SubagentRunning   SubagentState = "RUNNING"
	SubagentSucceeded SubagentState = "SUCCEEDED"
	SubagentFailed    SubagentState = "FAILED"
	SubagentCancelled SubagentState = "CANCELLED"
)

// SubagentLaunchRequest 单个子任务。tasks[] 里每一项都是它。
type SubagentLaunchRequest struct {
	Goal         string   `json:"goal"`
	Context      string   `json:"context,omitempty"`
	Role         string   `json:"role,omitempty"`
	Model        string   `json:"model,omitempty"`
	BlockedTools []string `json:"blocked_tools,omitempty"`
	WorkingDir   string   `json:"working_dir,omitempty"`
	TimeoutSecs  int      `json:"timeout_seconds,omitempty"`
	MaxSteps     int      `json:"max_steps,omitempty"`
}

// SubagentHandle 一个子代理的运行句柄。register 写进 subs map 后 get/list 才查得到。
type SubagentHandle struct {
	ID         string        `json:"id"`
	Goal       string        `json:"goal"`
	State      SubagentState `json:"state"`
	CreatedAt  time.Time     `json:"created_at"`
	Depth      int           `json:"depth"`
	Role       string        `json:"role,omitempty"` // 行为预设（内置四角色或自定义类型名）
	Model      string        `json:"model,omitempty"` // 实际使用的模型（req.Model 生效后回显）
	WorkingDir string        `json:"working_dir,omitempty"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	StepsUsed  int           `json:"steps_used,omitempty"`
	Batch      string        `json:"batch,omitempty"` // 同一次 fan-out 的批次 id

	// Progress 最近 N 条运行步骤（get_subagent / /subagents 可见；运行中持续更新，
	// 终态后保留最后一批供事后查看）。快照字段，由 snapshot() 从 progress 复制。
	Progress []string `json:"progress,omitempty"`

	cancel context.CancelFunc `json:"-"` // 运行态，不序列化
	child  *Agent             `json:"-"` // 运行中的子 Agent（供 Steer 注入纠正）

	progress []string // 运行中进度（child.OnStep 写入，sm.mu 保护）；快照时截取到 Progress
}

// maxSubagentProgress 进度缓冲上限：只留最近 N 条，防长任务把句柄撑爆。
const maxSubagentProgress = 20

// snapshot 返回可安全序列化的副本（去掉 cancel/child 运行态）。
// 调用方必须已持有 sm.mu（读锁即可）：progress 由子 goroutine 在写锁下追加。
func (h *SubagentHandle) snapshot() SubagentHandle {
	return SubagentHandle{
		ID: h.ID, Goal: h.Goal, State: h.State, CreatedAt: h.CreatedAt,
		Depth: h.Depth, Role: h.Role, Model: h.Model, WorkingDir: h.WorkingDir,
		Result: h.Result, Error: h.Error,
		StepsUsed: h.StepsUsed, Batch: h.Batch,
		Progress: append([]string(nil), h.progress...),
	}
}

// modelDeriver 可选接口：LLM 能派生一个只换模型的新实例（子任务指定 model 时用）。
// 放在 agent 包用接口断言，避免 import llm 造成循环依赖。llm.OpenAI 实现它。
type modelDeriver interface {
	WithModel(model string) LLM
}

// ============================================================================
// SubagentManager
// ============================================================================

const (
	defaultMaxConcurrent = 10 // 对齐 hermes delegate_task 上限
	defaultMaxDepth      = 3
	defaultMaxSteps      = 50
	defaultTimeoutSecs   = 300
)

type SubagentManager struct {
	mu          sync.RWMutex
	subs        map[string]*SubagentHandle
	nextID      atomic.Int64
	maxDepth    int
	maxSteps    int
	parentLLM   LLM                   // 只要求 LLM（Run 内部自己按接口能力选 tools/ctx）
	parentSys   string                // 主 system（子 agent 个性化指令层）
	pm          *plugins.Manager      // 共享插件注册表（只读复制工具清单）
	jobNotify   func(note string)     // 结果回灌：完成通知塞进主循环可见通道
	onProgress  func(id, line string) // 进度推送（GAP-S3 收尾）：每步进度行回调，nil = 关闭
	persister   func(SubagentHandle)  // 终态落盘（GAP-S4）：快照交调用方持久化，nil = 不落盘
	permission  *PermissionGate       // 权限门禁（GAP-S2）：与主代理共用同一实例，nil = 无门禁
	customTypes map[string]AgentType  // 用户自定义子代理类型（GAP-S1 第二层），nil = 无
	sem         chan struct{}         // 并发上限
}

// SetOnProgress 注册进度推送回调（GAP-S3 收尾：推送式可见性）。nil = 关闭。
// 与 JobNotify（完成回灌）互补：OnStep 每步/每次工具调用触发，本回调把
// (句柄id, 进度行) 转给订阅方（TUI 状态栏 / WebUI WS 广播）。
// 回调在子 agent goroutine 里同步调用且**不带锁**——订阅方必须非阻塞
// （自己分发/节流），否则会拖慢子循环。
func (sm *SubagentManager) SetOnProgress(f func(id, line string)) {
	sm.mu.Lock()
	sm.onProgress = f
	sm.mu.Unlock()
}

// SetPersister 注册终态落盘回调（GAP-S4）。句柄到达 SUCCEEDED/FAILED/CANCELLED
// 时以快照调用一次（锁外）。回调可能做文件 IO，必须自己兜错误、不能 panic。
// nil = 不落盘（默认，行为与旧版一致）。
func (sm *SubagentManager) SetPersister(f func(SubagentHandle)) {
	sm.mu.Lock()
	sm.persister = f
	sm.mu.Unlock()
}

// persistTerminal 终态落盘：取快照（锁内）→ 回调（锁外）。
func (sm *SubagentManager) persistTerminal(id string) {
	sm.mu.RLock()
	h, ok := sm.subs[id]
	var snap SubagentHandle
	if ok {
		snap = h.snapshot()
	}
	f := sm.persister
	sm.mu.RUnlock()
	if !ok || f == nil {
		return
	}
	if snap.State == SubagentSucceeded || snap.State == SubagentFailed || snap.State == SubagentCancelled {
		f(snap)
	}
}

// SubagentConfig 构造参数。JobNotify 由 main.go 接入 Jobs 完成通知。
type SubagentConfig struct {
	ParentLLM LLM
	ParentSys string
	Plugins   *plugins.Manager
	// Permission 权限门禁（GAP-S2）：主代理与子代理共用同一实例（进程级策略，
	// 模式运行时可变）。nil = 子代理无门禁（与主代理一致）。子代理里的确认
	// 走 gate 的 ask 回调，与主代理共用 askMu 串行化（TUI 单槽不覆盖弹窗）。
	Permission *PermissionGate
	// JobNotify 子代理完成时调用，note 作为「【后台任务完成】」注入主循环。
	// nil 时结果仍可通过 get_subagent 查询，但不自动回灌。
	JobNotify func(note string)
}

func NewSubagentManager(cfg SubagentConfig) *SubagentManager {
	return &SubagentManager{
		subs:       make(map[string]*SubagentHandle),
		maxDepth:   defaultMaxDepth,
		maxSteps:   defaultMaxSteps,
		parentLLM:  cfg.ParentLLM,
		parentSys:  cfg.ParentSys,
		pm:         cfg.Plugins,
		jobNotify:  cfg.JobNotify,
		permission: cfg.Permission,
		sem:        make(chan struct{}, defaultMaxConcurrent),
	}
}

// register 把句柄真正写进 subs —— 旧实现最致命的缺失：Launch 造了句柄
// 却从不登记，导致 get/list/cancel 全部 "not found"，派出去就是孤儿。
func (sm *SubagentManager) register(h *SubagentHandle) {
	sm.mu.Lock()
	sm.subs[h.ID] = h
	sm.mu.Unlock()
}

// setState 在锁内安全更新句柄状态（子 goroutine 与主 goroutine 都会写）。
// 到达终态时触发落盘回调（GAP-S4）。
func (sm *SubagentManager) setState(id string, st SubagentState, result, errMsg string) {
	sm.mu.Lock()
	if h, ok := sm.subs[id]; ok {
		h.State = st
		if result != "" {
			h.Result = result
		}
		if errMsg != "" {
			h.Error = errMsg
		}
	}
	sm.mu.Unlock()
	if st == SubagentSucceeded || st == SubagentFailed || st == SubagentCancelled {
		sm.persistTerminal(id)
	}
}

// Launch 派单个子任务，异步返回句柄（深度 1，主对话直派）。
func (sm *SubagentManager) Launch(req SubagentLaunchRequest) (*SubagentHandle, error) {
	return sm.LaunchAtDepth(req, 1)
}

// LaunchAtDepth 派单个子任务并指定嵌套深度（子 agent 再委派时用）。
func (sm *SubagentManager) LaunchAtDepth(req SubagentLaunchRequest, depth int) (*SubagentHandle, error) {
	if depth <= 0 {
		depth = 1
	}
	if depth > sm.maxDepth {
		return nil, fmt.Errorf("subagent: max depth %d exceeded (got %d)", sm.maxDepth, depth)
	}
	hs := sm.launchBatch([]SubagentLaunchRequest{req}, depth)
	return hs[0], nil
}

// LaunchBatch 一次 fan-out 多个子任务并发生跑，返回同一批次的句柄列表。
func (sm *SubagentManager) LaunchBatch(reqs []SubagentLaunchRequest, depth int) ([]*SubagentHandle, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("subagent: tasks required")
	}
	if len(reqs) > defaultMaxConcurrent {
		return nil, fmt.Errorf("subagent: too many tasks (%d > %d)", len(reqs), defaultMaxConcurrent)
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > sm.maxDepth {
		return nil, fmt.Errorf("subagent: max depth %d exceeded (got %d)", sm.maxDepth, depth)
	}
	return sm.launchBatch(reqs, depth), nil
}

func (sm *SubagentManager) launchBatch(reqs []SubagentLaunchRequest, depth int) []*SubagentHandle {
	batchID := fmt.Sprintf("batch-%d", sm.nextID.Add(1))
	handles := make([]*SubagentHandle, 0, len(reqs))
	for i, req := range reqs {
		badGoal := req.Goal == ""
		if badGoal {
			req.Goal = fmt.Sprintf("(task %d: 缺少 goal)", i+1)
		}
		// role 决定子代理的行为预设，注入到它的任务指令里（此前只赋默认值从不使用，
		// 是个形同虚设的字段）。未知值按 leaf 处理，不静默接受任意字符串；
		// 用户自定义类型（GAP-S1 第二层）名同样合法，命中后按类型白名单/模型/系统层执行。
		if req.Role == "" {
			req.Role = "leaf"
		}
		switch req.Role {
		case "leaf", "explore", "implement", "review":
		default:
			if _, ok := sm.customType(req.Role); !ok {
				req.Role = "leaf"
			}
		}
		if req.MaxSteps <= 0 {
			req.MaxSteps = sm.maxSteps
		}
		if req.TimeoutSecs <= 0 {
			req.TimeoutSecs = defaultTimeoutSecs
		}
		h := &SubagentHandle{
			ID:         fmt.Sprintf("sub-%d-%d", sm.nextID.Add(1), i+1),
			Goal:       req.Goal,
			State:      SubagentPending,
			CreatedAt:  time.Now(),
			Depth:      depth,
			Role:       req.Role,
			Model:      req.Model, // 未指定时为空，runSubagent 里回显实际值
			WorkingDir: req.WorkingDir,
			Batch:      batchID,
		}
		sm.register(h)
		handles = append(handles, h)
		if badGoal {
			sm.setState(h.ID, SubagentFailed, "", "subagent: goal required")
			continue
		}
		go sm.runSubagent(h, req)
	}
	return handles
}

// runSubagent 跑一个子 agent：独立 Agent 实例 → 原生 Run() 主循环。
func (sm *SubagentManager) runSubagent(h *SubagentHandle, req SubagentLaunchRequest) {
	// 并发上限：拿不到槽就排队（不丢弃任务）。
	//
	// 排队超时与执行超时必须分开：早前两段共用 req.TimeoutSecs，排队 250s 才拿到槽
	// 的任务执行只剩 50s，实际是被饿死的——这不是任务本身跑不动，是排队吃掉了预算。
	// 排队窗口独立给一个等待上限（取 max(30s, TimeoutSecs)），拿到槽后重新计时。
	waitLimit := time.Duration(req.TimeoutSecs) * time.Second
	if waitLimit < 30*time.Second {
		waitLimit = 30 * time.Second
	}
	select {
	case sm.sem <- struct{}{}:
		defer func() { <-sm.sem }()
	case <-time.After(waitLimit):
		sm.setState(h.ID, SubagentFailed, "", "subagent: 等待并发槽超时（执行预算未消耗）")
		sm.notify(h, "")
		return
	}

	sm.setState(h.ID, SubagentRunning, "", "")
	// 执行预算从拿到槽后才开始计。
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutSecs)*time.Second)
	sm.mu.Lock()
	h.cancel = cancel
	sm.mu.Unlock()
	defer cancel()

	if sm.parentLLM == nil || sm.pm == nil {
		sm.setState(h.ID, SubagentFailed, "", "subagent: 未配置 LLM 或插件注册表")
		sm.notify(h, "")
		return
	}

	// 子 agent 工具集：共享注册表副本，过滤掉委派类工具（防无限递归）
	// 和 req.BlockedTools。Tools() 返回新 slice，改动不影响主注册表。
	// 三层收窄（互斥）：
	//   1. 自定义类型带 tools 白名单（GAP-S1 第二层）→ 只保留白名单内的共享工具；
	//   2. 只读类内置角色（explore/review）→ 写类工具直接摘除，「不许改任何文件」
	//      从 roleHint 的提示词约定变成工具层强制——模型不守约也调不到 edit/write；
	//   3. 都不命中 → 全量共享工具。
	// bash 在 2/3 层仍保留：git log/grep 等只读检索依赖它，进程级写闸门归权限门禁做。
	at, isCustom := sm.customType(req.Role)
	blockedByRole := roleWriteBlocked(req.Role)
	var typeWhitelist map[string]bool
	if isCustom && len(at.Tools) > 0 {
		typeWhitelist = make(map[string]bool, len(at.Tools))
		for _, name := range at.Tools {
			typeWhitelist[name] = true
		}
	}
	childPM := plugins.NewManager("", nil)
	for _, t := range sm.pm.Tools() {
		if isDelegationTool(t.Name) || containsStr(req.BlockedTools, t.Name) {
			continue
		}
		if typeWhitelist != nil {
			if !typeWhitelist[t.Name] {
				continue
			}
		} else if containsStr(blockedByRole, t.Name) {
			continue
		}
		childPM.RegisterBuiltin(t)
	}
	// 有限嵌套：子 agent 可以再派下一代，但深度 +1 且受 maxDepth 闸门拦住。
	// 只有未达上限、且调用方没显式屏蔽 delegate_task 时才开放，避免递归失控。
	if h.Depth < sm.maxDepth && !containsStr(req.BlockedTools, "delegate_task") {
		nextDepth := h.Depth + 1
		childPM.RegisterBuiltin(plugins.Tool{
			Name:        "delegate_task",
			Description: fmt.Sprintf("（第 %d 层子代理）再派下一层子任务并行处理，深度上限 %d。", nextDepth, sm.maxDepth),
			Run: func(args string) (string, error) {
				var batch struct {
					Tasks []SubagentLaunchRequest `json:"tasks"`
				}
				var single SubagentLaunchRequest
				var hs []*SubagentHandle
				var err error
				if json.Unmarshal([]byte(args), &batch) == nil && len(batch.Tasks) > 0 {
					hs, err = sm.LaunchBatch(batch.Tasks, nextDepth)
				} else if json.Unmarshal([]byte(args), &single) == nil && single.Goal != "" {
					var h *SubagentHandle
					h, err = sm.LaunchAtDepth(single, nextDepth)
					hs = []*SubagentHandle{h}
				} else {
					return `{"error": "goal required (or tasks[])"}`, fmt.Errorf("delegate_task: goal required")
				}
				if err != nil {
					return fmt.Sprintf(`{"error": %q}`, err.Error()), err
				}
				return marshalHandles(sm, hs), nil
			},
		})
	}

	// 子任务可指定模型：parentLLM 若实现 modelDeriver 就派生一个只换 Model 的
	// 新实例；否则忽略 req.Model 用主模型（并在句柄标注，避免"以为换了其实没换"）。
	// 自定义类型可带类型默认 model（GAP-S1 第二层）：req.Model 未指定时生效，
	// req.Model 显式指定仍优先——per-task 请求比类型默认更具体。
	subLLM := sm.parentLLM
	effectiveModel := req.Model
	if effectiveModel == "" && isCustom {
		effectiveModel = at.Model
	}
	if effectiveModel != "" && effectiveModel != req.Model {
		// 类型默认模型写回句柄，get_subagent 可见实际生效值
		sm.mu.Lock()
		h.Model = effectiveModel
		sm.mu.Unlock()
	}
	if effectiveModel != "" {
		if d, ok := sm.parentLLM.(modelDeriver); ok {
			subLLM = d.WithModel(effectiveModel)
		} else {
			// 不支持派生：用主模型，但在句柄标注，避免"以为换了其实没换"。
			sm.mu.Lock()
			h.Error = "指定的 model 被忽略：当前 LLM 不支持按任务切换模型"
			sm.mu.Unlock()
		}
	}

	// 独立 Agent：LLM/插件共享，运行态天然隔离。
	child := New(subLLM, childPM)
	child.System = sm.parentSys
	child.MaxSteps = req.MaxSteps
	child.WorkDir = req.WorkingDir
	// 权限门禁继承（GAP-S2）：与主代理共用同一 gate 实例——模式运行时可变、
	// 子代理里的确认与主代理共用 askMu 串行化（TUI 单槽不覆盖弹窗）。
	child.Permission = sm.permission
	// 进度上报（GAP-S3）：子循环每步/每次工具调用回调一行摘要，写进句柄缓冲
	//（get_subagent 的 progress 字段与 /subagents 的「进度」行读取）。
	// 写锁保护；只留最近 maxSubagentProgress 条。推送回调锁外调用：
	// 只推 RUNNING 态的进度（终态走 JobNotify 完成回灌，不再推线），订阅方非阻塞。
	child.OnStep = func(step string) {
		sm.mu.Lock()
		h.progress = append(h.progress, step)
		if len(h.progress) > maxSubagentProgress {
			h.progress = h.progress[len(h.progress)-maxSubagentProgress:]
		}
		f := sm.onProgress
		running := h.State == SubagentRunning
		sm.mu.Unlock()
		if f != nil && running {
			f(h.ID, step)
		}
	}
	// 把 child 存进句柄，供 Steer(id, msg) 在任务运行中注入纠正消息。
	sm.mu.Lock()
	h.child = child
	sm.mu.Unlock()
	defer func() {
		sm.mu.Lock()
		h.child = nil
		sm.mu.Unlock()
	}()
	// 按 role 给子代理定行为边界（此前 role 从不生效）。
	// 自定义类型（GAP-S1 第二层）：正文即该类型的 system 附加层，原样注入；
	// 带白名单时加一句说明，避免模型反复尝试清单外工具。
	roleHint := ""
	if isCustom {
		roleHint = "\n你的角色类型：**" + at.Name + "**。" + at.System
		if len(at.Tools) > 0 {
			roleHint += "\n你的可用工具已按类型白名单收窄，清单外的工具不可调用，请直接在其能力范围内完成任务。"
		}
	} else {
		switch req.Role {
		case "explore":
			roleHint = "\n你的角色：**探索者**。只读调研，弄清事实与位置即可；写类工具（edit/write）已对你关闭，结论里写清关键文件:行号与证据。"
		case "implement":
			roleHint = "\n你的角色：**实现者**。按 goal 直接动手改代码/写文件，完成后简述改了哪些文件与验证方式。"
		case "review":
			roleHint = "\n你的角色：**审查者**。只读审查，指出问题（含文件:行号、严重度、理由与修法）；写类工具（edit/write）已对你关闭，不要在结论里附修改后的完整文件。"
		default: // leaf
			roleHint = "\n你的角色：**叶子执行者**。独立完成这一件子任务并交付结果。"
		}
	}
	task := fmt.Sprintf("%s\n\n【子任务背景/上下文】\n%s\n\n你是被委派的子代理，只完成上面这一件子任务，做完用 respond 工具直接交付结果，不要再委派。%s",
		req.Goal, req.Context, roleHint)

	type runRes struct {
		reply string
		err   error
	}
	done := make(chan runRes, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- runRes{"", fmt.Errorf("subagent panicked: %v", r)}
			}
		}()
		reply, err := child.Run(task)
		done <- runRes{reply, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			sm.setState(h.ID, SubagentFailed, res.reply, res.err.Error())
		} else {
			sm.setState(h.ID, SubagentSucceeded, res.reply, "")
		}
		sm.notify(h, res.reply)
	case <-ctx.Done():
		child.Abort() // 尽量中断在途 LLM；Run 结束后 goroutine 自然退出
		sm.setState(h.ID, SubagentCancelled, "", "subagent: 超时/被取消 ("+ctx.Err().Error()+")")
		sm.notify(h, "")
	}
}

// notify 结果回灌：把完成摘要塞进主循环可见的通知通道（Jobs.DrainDone 已挂 loop.go:655）。
func (sm *SubagentManager) notify(h *SubagentHandle, reply string) {
	if sm.jobNotify == nil {
		return
	}
	// snapshot 现在读 progress（OnStep 在子 goroutine 写），必须带锁。
	sm.mu.RLock()
	s := h.snapshot()
	sm.mu.RUnlock()
	line := fmt.Sprintf("子代理 %s [%s] %s → %s", s.ID, s.State, truncate(s.Goal, 60),
		truncate(firstNonEmpty(reply, s.Result, s.Error), 300))
	sm.jobNotify(line)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func isDelegationTool(name string) bool {
	switch name {
	case "delegate_task", "get_subagent", "list_subagents", "cancel_subagent":
		return true
	}
	return false
}

// roleWriteBlocked 返回只读类角色必须从工具清单摘除的写类工具名。
// explore/review 承诺「只读」，若清单里留着 edit/write，约束就只是提示词级的，
// 模型不守约照样能改文件（GAP-S1 第一层：提示词 → 工具层强制）。
// 其余角色（leaf/implement）返回 nil 不摘除。skill_manage 等其他写入口可按需追加。
func roleWriteBlocked(role string) []string {
	switch role {
	case "explore", "review":
		return []string{"edit", "write"}
	}
	return nil
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// unwrapArgsJSON 宽容解析模型传入的工具参数：剥掉 {"args":"<json 字符串>"}
// / {"args":{...}} 这类包装壳，返回可直接 json.Unmarshal 到目标结构的扁平
// JSON 对象文本；剥不出包装就原样返回。
//
// 为什么需要：Description 里写的 Args 形状只是示例，实测模型会按自己的习惯
// 多包一层（会话日志里出现过 get_subagent 收到 {"args":"{\"id\":\"sub-2-1\"}"}），
// 顶层解析不到 id 就整轮报错，而模型往往原样重试、越错越死循环。
// 与 builtins.ask.go 的 parseQuestions 同思路（最多剥 3 层）。
// 只认 args/arguments/params 三个壳键——delegate_task 等四个工具的合法参数
// 里没有这些键名，不会误伤。
func unwrapArgsJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return raw
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return raw
	}
	for depth := 0; depth < 3; depth++ {
		m, ok := v.(map[string]any)
		if !ok {
			break
		}
		var next any
		found := false
		for _, key := range []string{"args", "arguments", "params"} {
			if x, ok := m[key]; ok {
				next, found = x, true
				break
			}
		}
		if !found {
			break
		}
		if s, ok := next.(string); ok {
			var inner any
			if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &inner); err != nil {
				break
			}
			next = inner
		}
		v = next
	}
	if _, ok := v.(map[string]any); ok {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return raw
}

// ============================================================================
// 查询 / 取消 / 汇总
// ============================================================================

func (sm *SubagentManager) Get(id string) (*SubagentHandle, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	h, ok := sm.subs[id]
	if !ok {
		return nil, false
	}
	s := h.snapshot()
	return &s, true
}

func (sm *SubagentManager) List() []*SubagentHandle {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]*SubagentHandle, 0, len(sm.subs))
	for _, h := range sm.subs {
		s := h.snapshot()
		out = append(out, &s)
	}
	return out
}

// Cancel 真取消：调 context.CancelFunc 中断在途 LLM，并标记状态。
func (sm *SubagentManager) Cancel(id string) error {
	sm.mu.Lock()
	h, ok := sm.subs[id]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("subagent: %q not found", id)
	}
	if h.State == SubagentSucceeded || h.State == SubagentFailed || h.State == SubagentCancelled {
		sm.mu.Unlock()
		return nil // 已终结，幂等
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.State = SubagentCancelled
	h.Error = "subagent: cancelled by user"
	sm.mu.Unlock()
	sm.persistTerminal(id)
	return nil
}

// Steer 向运行中的子代理注入一条纠正消息（对齐 Hermes steer）。
// child.Steer 是线程安全、非阻塞的：消息会在子循环下一步 LLM 调用前被消费。
// 只在 RUNNING 状态有效；已终结/未启动时返回明确错误，不静默丢弃。
func (sm *SubagentManager) Steer(id, content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("subagent: steer content required")
	}
	sm.mu.RLock()
	h, ok := sm.subs[id]
	var child *Agent
	var st SubagentState
	if ok {
		child = h.child
		st = h.State
	}
	sm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("subagent: %q not found", id)
	}
	if child == nil || st != SubagentRunning {
		return fmt.Errorf("subagent %q 不在运行中（state=%s），无法 steer", id, st)
	}
	child.Steer(content)
	return nil
}

// RenderSubagentsForHuman 渲染子代理清单给人看（/subagents 内部命令用）：
// 按创建时间排序，含状态图标、批次、模型、步数与结果/错误摘要。
// 与 RenderResults（给模型的紧凑版）区分。
func (sm *SubagentManager) RenderSubagentsForHuman() string {
	// 收集快照而非活句柄：progress 由子 goroutine 在写锁下追加，
	// 活句柄在 RUnlock 后读取会 data race（race detector 实测命中）。
	sm.mu.RLock()
	subs := make([]SubagentHandle, 0, len(sm.subs))
	for _, h := range sm.subs {
		subs = append(subs, h.snapshot())
	}
	sm.mu.RUnlock()
	if len(subs) == 0 {
		return "（还没有派发过子任务。用 delegate_task 派发后这里会显示状态）"
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].CreatedAt.Before(subs[j].CreatedAt) })
	icon := map[SubagentState]string{
		SubagentPending:   "⏳",
		SubagentRunning:   "🏃",
		SubagentSucceeded: "✅",
		SubagentFailed:    "❌",
		SubagentCancelled: "⏹",
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "子任务 %d 个：\n", len(subs))
	for i := range subs {
		h := &subs[i]
		state := string(h.State)
		if h.State == SubagentRunning {
			state = "运行中"
		} else if h.State == SubagentPending {
			state = "排队中"
		} else if h.State == SubagentSucceeded {
			state = "已完成"
		} else if h.State == SubagentFailed {
			state = "失败"
		} else if h.State == SubagentCancelled {
			state = "已取消"
		}
		fmt.Fprintf(&sb, " %s %s  %s", icon[h.State], h.ID, state)
		if h.Batch != "" {
			fmt.Fprintf(&sb, "  [%s]", h.Batch)
		}
		if h.Role != "" {
			fmt.Fprintf(&sb, "  角色:%s", h.Role)
		}
		if h.Model != "" {
			fmt.Fprintf(&sb, "  模型:%s", h.Model)
		}
		if h.StepsUsed > 0 {
			fmt.Fprintf(&sb, "  %d步", h.StepsUsed)
		}
		fmt.Fprintf(&sb, "\n     任务：%s\n", truncate(h.Goal, 80))
		// 最近一步（当前在干什么）；终态后 Progress 保留最后一批供事后查看
		if len(h.Progress) > 0 && h.State != SubagentPending {
			fmt.Fprintf(&sb, "     进度：%s\n", truncate(h.Progress[len(h.Progress)-1], 100))
		}
		if h.Result != "" {
			fmt.Fprintf(&sb, "     结果：%s\n", truncate(firstNonEmpty(h.Result, h.Error), 160))
		}
		if h.Error != "" && h.Result == "" {
			fmt.Fprintf(&sb, "     错误：%s\n", truncate(h.Error, 160))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// RenderResults 汇总所有句柄（含结果与错误），供 list_subagents / 结果总览用。
func (sm *SubagentManager) RenderResults() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.subs) == 0 {
		return "（无子代理记录）"
	}
	var sb strings.Builder
	for _, h := range sm.subs {
		fmt.Fprintf(&sb, "  [%s] %s → %s", h.ID, h.State, truncate(h.Goal, 60))
		if h.Result != "" {
			fmt.Fprintf(&sb, "\n       结果：%s", truncate(h.Result, 200))
		}
		if h.Error != "" {
			fmt.Fprintf(&sb, "\n       错误：%s", truncate(h.Error, 200))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// extractAssistantText 保留：历史测试 xml_multicall_test.go 引用它。
// 从模型一轮输出里取出「可当结果用」的文字。
func extractAssistantText(reply string) string {
	var parsed struct {
		Action  string `json:"action"`
		Text    string `json:"text,omitempty"`
		Reason  string `json:"reason,omitempty"`
		Tool    string `json:"tool,omitempty"`
		Command string `json:"command,omitempty"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(reply)), &parsed); err == nil {
		if parsed.Action == "reply" && parsed.Text != "" {
			return parsed.Text
		}
		return parsed.Reason
	}
	if IsToolCallText(reply) {
		return ""
	}
	return strings.TrimSpace(reply)
}

// ============================================================================
// 工具工厂
// ============================================================================

// DelegateTool：支持单任务 {goal,...} 与批量 {"tasks":[{goal,...},...]}。
// 自定义类型（GAP-S1 第二层）注册后追加进描述，模型可直接按类型名派发。
func DelegateTool(sm *SubagentManager) plugins.Tool {
	desc := "分派子代理并行处理子任务。复杂任务可一次派多个：{\"tasks\":[{\"goal\":\"子任务1\",\"context\":\"背景\"},{\"goal\":\"子任务2\"}]}；" +
		"单个用 {\"goal\":\"...\",\"context\":\"...\",\"max_steps\":30}。异步返回句柄 id，用 get_subagent 查结果，list_subagents 看全部，运行中可用 steer_subagent 纠偏。" +
		"role 四选一：leaf(默认，独立执行)、implement(动手实现)、explore(只读调研)、review(只读审查)；explore/review 的写类工具(edit/write)被强制摘除，只出结论。" +
		"有先后依赖的子任务先记 todo（action=add, blocked_by），完成回灌会自动解锁后继；tasks[] 里只放互相独立的。上下文与主对话隔离。"
	if types := sm.AgentTypes(); len(types) > 0 {
		var sb strings.Builder
		sb.WriteString("\n可用自定义子代理类型（role 直接传类型名，工具集/模型/行为按类型定义执行）：")
		for _, at := range types {
			fmt.Fprintf(&sb, "\n- %s: %s", at.Name, at.Description)
		}
		desc += sb.String()
	}
	return plugins.Tool{
		Name:        "delegate_task",
		Description: desc,
		Run: func(args string) (string, error) {
			args = unwrapArgsJSON(args)
			var batch struct {
				Tasks []SubagentLaunchRequest `json:"tasks"`
			}
			var single SubagentLaunchRequest
			trimmed := strings.TrimSpace(args)
			if strings.HasPrefix(trimmed, "{") && json.Unmarshal([]byte(args), &batch) == nil && len(batch.Tasks) > 0 {
				handles, err := sm.LaunchBatch(batch.Tasks, 1)
				if err != nil {
					return fmt.Sprintf(`{"error": %q}`, err.Error()), err
				}
				return marshalHandles(sm, handles), nil
			}
			if err := json.Unmarshal([]byte(args), &single); err != nil || single.Goal == "" {
				return `{"error": "goal required (or tasks[])"}`, fmt.Errorf("delegate_task: {goal} or {tasks:[]} required")
			}
			h, err := sm.Launch(single)
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error()), err
			}
			return marshalHandles(sm, []*SubagentHandle{h}), nil
		},
	}
}

func marshalHandles(sm *SubagentManager, handles []*SubagentHandle) string {
	out := make([]SubagentHandle, 0, len(handles))
	for _, h := range handles {
		// 走带锁的 Get 读快照：子 goroutine 的 setState 可能正在改同一个句柄，
		// 直接 h.snapshot() 会 data race（race detector 实测命中）。
		if s, ok := sm.Get(h.ID); ok {
			out = append(out, *s)
		} else {
			sm.mu.RLock()
			out = append(out, h.snapshot())
			sm.mu.RUnlock()
		}
	}
	b, _ := json.Marshal(map[string]interface{}{"count": len(out), "handles": out})
	return string(b)
}

func GetSubagentTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "get_subagent",
		Description: "查询某个子代理的状态与结果。Args: {id}。返回含 state/result/error。",
		Run: func(args string) (string, error) {
			args = unwrapArgsJSON(args)
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.ID == "" {
				return `{"error": "id required"}`, fmt.Errorf("get_subagent: id required")
			}
			h, ok := sm.Get(p.ID)
			if !ok {
				return `{"error": "subagent not found"}`, fmt.Errorf("subagent %q not found", p.ID)
			}
			b, _ := json.Marshal(h)
			return string(b), nil
		},
	}
}

func ListSubagentsTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "list_subagents",
		Description: "列出全部分派过的子代理及其状态/结果（含并行批次）。",
		Run: func(args string) (string, error) {
			_ = args
			return sm.RenderResults(), nil
		},
	}
}

// CancelSubagentTool 供需要主动中止的场景（可选注册）。
func CancelSubagentTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "cancel_subagent",
		Description: "取消一个运行中的子代理。Args: {id}。",
		Run: func(args string) (string, error) {
			args = unwrapArgsJSON(args)
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.ID == "" {
				return `{"error": "id required"}`, fmt.Errorf("cancel_subagent: id required")
			}
			if err := sm.Cancel(p.ID); err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error()), err
			}
			return `{"ok": true}`, nil
		},
	}
}

// DelegationGuide 返回注入主 system prompt 的委派策略（P2 自动分解）。
//
// 设计立场：不做硬编码复杂度判定（数句子/关键词那种规则引擎误判率高、维护差），
// 而是把「何时拆、怎么拆、何时别拆」讲清楚，由模型自己判断。关键是必须带
// 抑制条件——否则简单任务也会被拆成一堆子代理，白烧 token、结果还更难汇总。
func DelegationGuide() string {
	return `
## 子任务委派策略（delegate_task）
只有在**确实能并行省时**时才拆，不要为了"看起来专业"而拆。

满足以下全部条件才委派：
1. 任务能切成 **2 个以上互相独立**的子目标（彼此不依赖对方的中间结果）；
2. 每个子目标本身有实质工作量（要读多个文件 / 跑多次命令 / 多次检索），不是一两步就能完；
3. 这些子目标**并行做比顺序做明显更快**。

典型该拆：同时调研 3 个互不相关的技术方案；批量改造 5 个互不依赖的模块。
**不该拆**（直接自己做）：
- 单文件修改、单个问题排查、一次搜索能答的问题；
- 有强先后依赖的步骤（B 必须等 A 的结果）；
- 你自己一两步就能完成的小事——拆出去反而多一轮汇总开销。

拆的时候：
- 用 {"tasks":[{goal,context},{goal,context}]} 一次派多个，让它们真并行；
- 子任务间若有先后依赖，先 todo add（blocked_by）落依赖，完成回灌会自动解锁后继；tasks[] 里只放互相独立的；
- 每个 goal 写清"交付什么"，context 补足子代理看不到的背景（它只有你的 messages，没有主对话）；
- 派完先用 list_subagents 看状态，用 get_subagent 取结果，**汇总后自己写最终答复**——不要把子代理的原始输出直接甩给用户；
- 发现某个子代理跑偏，用 steer_subagent 发纠正（不终止），确实没救了再 cancel_subagent。
`
}

// SteerSubagentTool 向运行中的子代理注入纠正消息（对齐 Hermes steer）。
// 与 Cancel 的区别：Cancel 终止任务，Steer 只调整方向、任务继续跑。
func SteerSubagentTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "steer_subagent",
		Description: "给运行中的子代理发一条纠正指令，它会在此后各步优先遵守。Args: {id, message}。任务不终止。",
		Run: func(args string) (string, error) {
			args = unwrapArgsJSON(args)
			var p struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.ID == "" || strings.TrimSpace(p.Message) == "" {
				return `{"error": "id and message required"}`, fmt.Errorf("steer_subagent: id and message required")
			}
			if err := sm.Steer(p.ID, p.Message); err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error()), err
			}
			return `{"ok": true}`, nil
		},
	}
}
