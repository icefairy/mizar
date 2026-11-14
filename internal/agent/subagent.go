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
	ID        string        `json:"id"`
	Goal      string        `json:"goal"`
	State     SubagentState `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
	Depth     int           `json:"depth"`
	Model     string        `json:"model,omitempty"`     // 实际使用的模型（req.Model 生效后回显）
	WorkingDir string       `json:"working_dir,omitempty"`
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	StepsUsed int           `json:"steps_used,omitempty"`
	Batch     string        `json:"batch,omitempty"` // 同一次 fan-out 的批次 id

	cancel context.CancelFunc `json:"-"` // 运行态，不序列化
	child  *Agent             `json:"-"` // 运行中的子 Agent（供 Steer 注入纠正）
}

// snapshot 返回可安全序列化的副本（去掉 cancel/child 运行态）。
func (h *SubagentHandle) snapshot() SubagentHandle {
	return SubagentHandle{
		ID: h.ID, Goal: h.Goal, State: h.State, CreatedAt: h.CreatedAt,
		Depth: h.Depth, Model: h.Model, WorkingDir: h.WorkingDir,
		Result: h.Result, Error: h.Error,
		StepsUsed: h.StepsUsed, Batch: h.Batch,
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
	mu         sync.RWMutex
	subs       map[string]*SubagentHandle
	nextID     atomic.Int64
	maxDepth   int
	maxSteps   int
	parentLLM  LLM               // 只要求 LLM（Run 内部自己按接口能力选 tools/ctx）
	parentSys  string            // 主 system（子 agent 个性化指令层）
	pm         *plugins.Manager // 共享插件注册表（只读复制工具清单）
	jobNotify  func(note string) // 结果回灌：完成通知塞进主循环可见通道
	sem        chan struct{}     // 并发上限
}

// SubagentConfig 构造参数。JobNotify 由 main.go 接入 Jobs 完成通知。
type SubagentConfig struct {
	ParentLLM LLM
	ParentSys string
	Plugins   *plugins.Manager
	// JobNotify 子代理完成时调用，note 作为「【后台任务完成】」注入主循环。
	// nil 时结果仍可通过 get_subagent 查询，但不自动回灌。
	JobNotify func(note string)
}

func NewSubagentManager(cfg SubagentConfig) *SubagentManager {
	return &SubagentManager{
		subs:      make(map[string]*SubagentHandle),
		maxDepth:  defaultMaxDepth,
		maxSteps:  defaultMaxSteps,
		parentLLM: cfg.ParentLLM,
		parentSys: cfg.ParentSys,
		pm:        cfg.Plugins,
		jobNotify: cfg.JobNotify,
		sem:       make(chan struct{}, defaultMaxConcurrent),
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
func (sm *SubagentManager) setState(id string, st SubagentState, result, errMsg string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if h, ok := sm.subs[id]; ok {
		h.State = st
		if result != "" {
			h.Result = result
		}
		if errMsg != "" {
			h.Error = errMsg
		}
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
		if req.Role == "" {
			req.Role = "leaf"
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
			Model:      req.Model,       // 未指定时为空，runSubagent 里回显实际值
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
	// 并发上限：拿不到槽就限时排队（不丢弃任务）。
	select {
	case sm.sem <- struct{}{}:
		defer func() { <-sm.sem }()
	case <-time.After(time.Duration(req.TimeoutSecs) * time.Second):
		sm.setState(h.ID, SubagentFailed, "", "subagent: 等待并发槽超时")
		sm.notify(h, "")
		return
	}

	sm.setState(h.ID, SubagentRunning, "", "")
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
	childPM := plugins.NewManager("", nil)
	for _, t := range sm.pm.Tools() {
		if isDelegationTool(t.Name) || containsStr(req.BlockedTools, t.Name) {
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
	subLLM := sm.parentLLM
	if req.Model != "" {
		if d, ok := sm.parentLLM.(modelDeriver); ok {
			subLLM = d.WithModel(req.Model)
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
	// 把 child 存进句柄，供 Steer(id, msg) 在任务运行中注入纠正消息。
	sm.mu.Lock()
	h.child = child
	sm.mu.Unlock()
	defer func() {
		sm.mu.Lock()
		h.child = nil
		sm.mu.Unlock()
	}()
	task := fmt.Sprintf("%s\n\n【子任务背景/上下文】\n%s\n\n你是被委派的子代理，只完成上面这一件子任务，做完用 respond 工具直接交付结果，不要再委派。",
		req.Goal, req.Context)

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
	s := h.snapshot()
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

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
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
	defer sm.mu.Unlock()
	h, ok := sm.subs[id]
	if !ok {
		return fmt.Errorf("subagent: %q not found", id)
	}
	if h.State == SubagentSucceeded || h.State == SubagentFailed || h.State == SubagentCancelled {
		return nil // 已终结，幂等
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.State = SubagentCancelled
	h.Error = "subagent: cancelled by user"
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

// delegate_task：支持单任务 {goal,...} 与批量 {"tasks":[{goal,...},...]}。
func DelegateTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name: "delegate_task",
		Description: "分派子代理并行处理子任务。复杂任务可一次派多个：{\"tasks\":[{\"goal\":\"子任务1\",\"context\":\"背景\"},{\"goal\":\"子任务2\"}]}；" +
			"单个用 {\"goal\":\"...\",\"context\":\"...\",\"max_steps\":30}。异步返回句柄 id，用 get_subagent 查结果，list_subagents 看全部。" +
			"子代理有完整工具能力(read/bash/edit/grep)，上下文与主对话隔离。",
		Run: func(args string) (string, error) {
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
			out = append(out, h.snapshot())
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
