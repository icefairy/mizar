package agent

import (
	"fmt"
	"sync"
)

// ============================================================================
// Hook 挂载点系统
//
// 设计目标：
//  1. 全环节挂载：Agent 循环的每个关键环节都有挂载点（开始/结束/步骤/LLM/
//     工具/压缩/错误），插件可在任意环节注入逻辑。
//  2. 多挂载：每个挂载点是一个 []HookFunc 列表，多个插件可同时挂载同一
//     挂载点，按注册顺序依次执行。
//  3. 缓存影响分级：每个挂载点标注 CacheImpact——🔴 严重（修改会破坏
//     prefix cache）/ 🟡 中 / 🟢 安全。文档必须向插件作者明示。
// ============================================================================

// CacheImpact 挂载点对 prefix cache 的影响分级。
type CacheImpact int

const (
	// ImpactSafe 安全：不影响发给 LLM 的消息序列，缓存前缀稳定。
	ImpactSafe CacheImpact = iota
	// ImpactModerate 中等：在工具结果/回复之后触发，可能影响后续消息，
	// 但不会改变已发送的历史前缀。
	ImpactModerate
	// ImpactSevere 严重：在 LLM 请求前触发且能修改消息列表，
	// 任何修改都会使缓存前缀失效，导致本步及后续所有请求缓存 miss。
	ImpactSevere
)

// String 返回人类可读的影响等级。
func (c CacheImpact) String() string {
	switch c {
	case ImpactSafe:
		return "🟢 安全"
	case ImpactModerate:
		return "🟡 中等"
	case ImpactSevere:
		return "🔴 严重"
	}
	return "?"
}

// CacheWarning 返回缓存影响警告文案（写进架构文档/插件模板）。
func (c CacheImpact) CacheWarning() string {
	switch c {
	case ImpactSafe:
		return "安全：不修改发给 LLM 的消息，缓存前缀稳定。"
	case ImpactModerate:
		return "中等：触发点在工具/回复之后，不会改变已发送历史，但新增消息会参与后续缓存。"
	case ImpactSevere:
		return "严重：在 LLM 请求前触发，修改 messages 会使整个 prefix cache 失效，本步及后续所有请求缓存 miss，成本显著上升。仅在绝对必要时使用。"
	}
	return ""
}

// HookContext 传给挂载函数的环境。
type HookContext struct {
	// 运行元信息
	RunID     string // 本次 Run 的唯一 ID
	Step      int    // 当前循环步（0 起）
	Task      string // 用户任务
	AgentName string

	// LLM 请求相关（LLMRequest 挂载点可修改）
	Messages []Message // 即将发给 LLM 的消息（修改会影响缓存！）

	// LLM 响应相关（LLMResponse 挂载点）
	Reply string // 模型回复文本

	// 工具相关（ToolCall / ToolResult 挂载点）
	Tool   string
	Args   string
	Result string
	Err    error // 工具执行错误（若有）

	// 压缩相关（CompactionBefore / CompactionAfter 挂载点）
	Compacted bool   // 本次步骤是否发生了压缩
	EstTokens int    // 压缩前估算 token
	Summary   string // 生成的摘要（CompactionAfter 可查看）

	// 会话相关
	SessionID string
}

// HookFunc 挂载函数。返回 error 时会被收集（记日志），但默认不中断主循环；
// 挂载点可通过 HookContext 中的指针字段影响流程（见各挂载点说明）。
type HookFunc func(ctx *HookContext) error

// Hooks 所有挂载点。每个字段是列表——多个插件可同时挂载同一挂载点，
// 按注册顺序执行。
type Hooks struct {
	mu sync.Mutex

	// RunStart: Run 开始，task 已就绪（🟢 安全——此时尚未构建 LLM 消息）
	RunStart []HookFunc
	// RunEnd: Run 结束，reply 已生成（🟢 安全）
	RunEnd []HookFunc

	// StepStart: 每步循环开始（🟡 中等——可读 msgs，但修改 msgs 会影响缓存）
	StepStart []HookFunc
	// StepEnd: 每步循环结束（🟡 中等）
	StepEnd []HookFunc

	// LLMRequest: LLM 调用前，messages 即将发送（🔴 严重——修改会破坏缓存）
	LLMRequest []HookFunc
	// LLMResponse: LLM 调用后，reply 已返回（🟢 安全）
	LLMResponse []HookFunc

	// ToolCall: 工具调用前（🟢 安全）
	ToolCall []HookFunc
	// ToolResult: 工具结果产生后（🟡 中等——结果会进入后续消息）
	ToolResult []HookFunc

	// CompactionBefore: 压缩执行前（🟢 安全）
	CompactionBefore []HookFunc
	// CompactionAfter: 压缩执行后（🟢 安全——摘要已固定，但修改 msgs 仍会破坏缓存）
	CompactionAfter []HookFunc

	// Error: 任何错误发生（解析失败/工具失败/LLM 失败）（🟢 安全）
	Error []HookFunc
}

// NewHooks 创建空挂载集。
func NewHooks() *Hooks {
	return &Hooks{}
}

// --- 注册方法（链式支持） ---

func (h *Hooks) OnRunStart(f HookFunc) *Hooks        { h.mu.Lock(); h.RunStart = append(h.RunStart, f); h.mu.Unlock(); return h }
func (h *Hooks) OnRunEnd(f HookFunc) *Hooks          { h.mu.Lock(); h.RunEnd = append(h.RunEnd, f); h.mu.Unlock(); return h }
func (h *Hooks) OnStepStart(f HookFunc) *Hooks       { h.mu.Lock(); h.StepStart = append(h.StepStart, f); h.mu.Unlock(); return h }
func (h *Hooks) OnStepEnd(f HookFunc) *Hooks         { h.mu.Lock(); h.StepEnd = append(h.StepEnd, f); h.mu.Unlock(); return h }
func (h *Hooks) OnLLMRequest(f HookFunc) *Hooks      { h.mu.Lock(); h.LLMRequest = append(h.LLMRequest, f); h.mu.Unlock(); return h }
func (h *Hooks) OnLLMResponse(f HookFunc) *Hooks     { h.mu.Lock(); h.LLMResponse = append(h.LLMResponse, f); h.mu.Unlock(); return h }
func (h *Hooks) OnToolCall(f HookFunc) *Hooks        { h.mu.Lock(); h.ToolCall = append(h.ToolCall, f); h.mu.Unlock(); return h }
func (h *Hooks) OnToolResult(f HookFunc) *Hooks      { h.mu.Lock(); h.ToolResult = append(h.ToolResult, f); h.mu.Unlock(); return h }
func (h *Hooks) OnCompactionBefore(f HookFunc) *Hooks { h.mu.Lock(); h.CompactionBefore = append(h.CompactionBefore, f); h.mu.Unlock(); return h }
func (h *Hooks) OnCompactionAfter(f HookFunc) *Hooks { h.mu.Lock(); h.CompactionAfter = append(h.CompactionAfter, f); h.mu.Unlock(); return h }
func (h *Hooks) OnError(f HookFunc) *Hooks           { h.mu.Lock(); h.Error = append(h.Error, f); h.mu.Unlock(); return h }

// --- 触发（内部） ---

// fire 触发一个挂载点，收集所有 error。
func (h *Hooks) fire(name string, hooks []HookFunc, ctx *HookContext, log func(string)) {
	if len(hooks) == 0 {
		return
	}
	for i, f := range hooks {
		if f == nil {
			continue
		}
		if err := f(ctx); err != nil {
			if log != nil {
				log(fmt.Sprintf("hook %s[%d] error: %v", name, i, err))
			}
		}
	}
}

func (h *Hooks) fireRunStart(ctx *HookContext, log func(string))     { h.mu.Lock(); hs := append([]HookFunc(nil), h.RunStart...); h.mu.Unlock(); h.fire("RunStart", hs, ctx, log) }
func (h *Hooks) fireRunEnd(ctx *HookContext, log func(string))       { h.mu.Lock(); hs := append([]HookFunc(nil), h.RunEnd...); h.mu.Unlock(); h.fire("RunEnd", hs, ctx, log) }
func (h *Hooks) fireStepStart(ctx *HookContext, log func(string))    { h.mu.Lock(); hs := append([]HookFunc(nil), h.StepStart...); h.mu.Unlock(); h.fire("StepStart", hs, ctx, log) }
func (h *Hooks) fireStepEnd(ctx *HookContext, log func(string))      { h.mu.Lock(); hs := append([]HookFunc(nil), h.StepEnd...); h.mu.Unlock(); h.fire("StepEnd", hs, ctx, log) }
func (h *Hooks) fireLLMRequest(ctx *HookContext, log func(string))   { h.mu.Lock(); hs := append([]HookFunc(nil), h.LLMRequest...); h.mu.Unlock(); h.fire("LLMRequest", hs, ctx, log) }
func (h *Hooks) fireLLMResponse(ctx *HookContext, log func(string))  { h.mu.Lock(); hs := append([]HookFunc(nil), h.LLMResponse...); h.mu.Unlock(); h.fire("LLMResponse", hs, ctx, log) }
func (h *Hooks) fireToolCall(ctx *HookContext, log func(string))     { h.mu.Lock(); hs := append([]HookFunc(nil), h.ToolCall...); h.mu.Unlock(); h.fire("ToolCall", hs, ctx, log) }
func (h *Hooks) fireToolResult(ctx *HookContext, log func(string))   { h.mu.Lock(); hs := append([]HookFunc(nil), h.ToolResult...); h.mu.Unlock(); h.fire("ToolResult", hs, ctx, log) }
func (h *Hooks) fireCompactionBefore(ctx *HookContext, log func(string)) { h.mu.Lock(); hs := append([]HookFunc(nil), h.CompactionBefore...); h.mu.Unlock(); h.fire("CompactionBefore", hs, ctx, log) }
func (h *Hooks) fireCompactionAfter(ctx *HookContext, log func(string))  { h.mu.Lock(); hs := append([]HookFunc(nil), h.CompactionAfter...); h.mu.Unlock(); h.fire("CompactionAfter", hs, ctx, log) }
func (h *Hooks) fireError(ctx *HookContext, log func(string))        { h.mu.Lock(); hs := append([]HookFunc(nil), h.Error...); h.mu.Unlock(); h.fire("Error", hs, ctx, log) }

// HookSpec 挂载点规格（文档生成/插件模板用）。
type HookSpec struct {
	Name        string
	Timing      string
	CacheImpact CacheImpact
	CanMutate   string // 可修改的字段（若有）
	Purpose     string
}

// AllHookSpecs 返回所有挂载点的规格表（架构文档引用）。
func AllHookSpecs() []HookSpec {
	return []HookSpec{
		{"RunStart", "Run 开始、task 已就绪", ImpactSafe, "-", "任务开始钩子：记录开始时间、初始化状态、下发任务上下文"},
		{"RunEnd", "Run 结束、reply 已生成", ImpactSafe, "-", "任务结束钩子：统计耗时、上报结果、清理状态"},
		{"StepStart", "每步循环开始", ImpactModerate, "Messages(不建议)", "步骤钩子：进度上报、外部熔断检查"},
		{"StepEnd", "每步循环结束", ImpactModerate, "-", "步骤钩子：步骤计数、状态持久化"},
		{"LLMRequest", "LLM 调用前、messages 即将发送", ImpactSevere, "Messages", "请求钩子：注入动态上下文、改写消息（⚠️ 会破坏缓存）"},
		{"LLMResponse", "LLM 调用后、reply 已返回", ImpactSafe, "-", "响应钩子：日志、流式转发、响应后处理"},
		{"ToolCall", "工具调用前", ImpactSafe, "Tool/Args", "工具钩子：参数校验、权限检查、审计"},
		{"ToolResult", "工具结果产生后", ImpactModerate, "-", "工具钩子：结果过滤、错误上报"},
		{"CompactionBefore", "压缩执行前", ImpactSafe, "-", "压缩钩子：预检查、手动触发前拦截"},
		{"CompactionAfter", "压缩执行后", ImpactSafe, "Messages(不建议)", "压缩钩子：摘要后处理、更新外部记忆"},
		{"Error", "任何错误发生", ImpactSafe, "-", "错误钩子：错误告警、降级策略"},
	}
}
