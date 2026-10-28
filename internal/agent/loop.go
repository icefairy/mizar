// Package agent 实现 Agent 主循环：规划 → 工具调用 → 结果反馈 → 修复。
package agent

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mizar/internal/context"
	"mizar/internal/diag"
	"mizar/internal/engine"
	"mizar/internal/jobs"
	"mizar/internal/lifecycle"
	"mizar/internal/plugins"
)

// CacheStats 系统提示词缓存命中率统计。
// 由 Agent 维护，启动/重启时打印摘要。
type CacheStats struct {
	mu     sync.Mutex
	hits   int // SystemPrompt() 命中缓存的调用次数
	misses int // 缓存失效后重建的次数
	total  int // hits + misses
}

// RecordHit 记录一次缓存命中。
func (c *CacheStats) RecordHit() {
	c.mu.Lock()
	c.hits++
	c.total++
	c.mu.Unlock()
}

// RecordMiss 记录一次缓存未命中（首次构建或 ReloadTools 后）。
func (c *CacheStats) RecordMiss() {
	c.mu.Lock()
	c.misses++
	c.total++
	c.mu.Unlock()
}

// Summary 返回人类可读摘要。
func (c *CacheStats) Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total == 0 {
		return "缓存统计: 未使用"
	}
	hitRate := float64(c.hits) / float64(c.total) * 100
	return fmt.Sprintf("缓存统计: %d 次查询，命中 %d/%d（%.1f%%），失效 %d 次",
		c.total, c.hits, c.total, hitRate, c.misses)
}

func (a *Agent) CacheStats() *CacheStats {
	return a.cacheStats
}

// LLM 抽象：任何 OpenAI 兼容客户端或测试 mock 都可实现。
type LLM interface {
	// Chat 发送消息列表，返回模型回复文本。
	Chat(messages []Message) (string, error)
}

// ToolCallLLM 可选接口：支持原生工具调用的 LLM。
// 若 LLM 实现此接口，Agent 循环会使用原生 tools 参数而非文本 JSON。
type ToolCallLLM interface {
	// ChatWithTools 发送消息 + 工具定义，返回模型回复文本。
	// 若模型返回原生 tool_calls，实现方应将其转为 agent 循环可解析的 JSON 文本。
	ChatWithTools(messages []Message, tools []plugins.Tool) (string, error)
}

// LLMWithContext 可选接口：支持原生工具调用且请求可被上下文取消的 LLM。
// 所有支持 ChatWithTools 的 LLM 都应尽量实现此接口，以支持 ESC 取消。
// 实现方应把 ctx 绑定到 HTTP 请求，使在途请求能立即中断。
type LLMWithContext interface {
	// ChatWithToolsCtx 发送消息 + 工具定义，返回模型回复文本；
	// 响应 ctx.Done() 以支持 Abort() 中断在途请求。
	ChatWithToolsCtx(ctx stdctx.Context, messages []Message, tools []plugins.Tool) (string, error)
}

// Message 对话消息（定义见 message.go：含 Kind 字段用于压缩切点）。
// Role 常量见 message.go：RoleSystem/RoleUser/RoleAssistant。

// ToolResult 工具执行结果（喂回 LLM）。
type ToolResult struct {
	ToolName string `json:"tool_name"`
	Args     string `json:"args"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

// maxStepsDefault 默认最大循环步数。写插件/多文件任务常需 30+ 步（bash 探测→读文件→写码→测试→修复），
// 默认 60 步留出余量；仍可用 /config max_steps 调整（1-1000）。
const maxStepsDefault = 60

// maxStepsWarnRemain 步数预算预警阈值：剩余步数 ≤ 此值时向模型注入收敛提醒（只提醒一次）。
const maxStepsWarnRemain = 3

// Agent 是主循环。
type Agent struct {
	LLM          LLM
	Plugins      *plugins.Manager
	System       string            // 用户自定义个性化指令（注入 context 层）
	Soul         string            // SOUL.md 内容（identity 层，跨会话稳定）
	SkillsPrompt string            // 技能索引/正文（volatile 层，插件热加载时通过 ReloadTools 失效缓存）
	PluginDir    string            // 插件目录（如 ~/.mizar/extensions），用于系统提示引导模型自行创建插件
	WorkDir      string            // 当前工作目录，注入系统提示供模型锚定搜索范围
	Initial      []Message         // 会话恢复时的历史消息（置于 task 之前）
	MaxSteps     int               // 最大循环步数（默认 maxStepsDefault=60）
	VerboseLog   func(string)      // 可选日志回调
	Compactor    *Compactor        // 会话压缩器（nil = 不压缩）
	Hooks        *Hooks            // 挂载点（nil = 无钩子）
	Tuner        *WeakModelTuner   // 弱模型宽容策略（nil = 不启用）
	Guard        *RepeatGuard      // 循环卫生守卫：重复调用渐进提醒，超阈值终止（nil = 不启用；复刻 dsh repeat-tool-reminder）
	Jobs         *jobs.Registry    // 后台任务注册表（nil = 无后台任务；bash run_in_background + job_* 工具）
	PlanMode     *PlanMode         // 计划模式控制器（nil = 不启用；复刻 dsh plan-mode）
	GoalService  *GoalService      // 会话目标服务（nil = 不启用；复刻 dsh goal）
	GoalLoop     *GoalLoop         // Goal 自动 judge 循环（nil = 不启用；复刻 hermes Ralph loop）
	SubAgents    *SubagentManager  // 子代理委派管理器（nil = 不启用；复刻 hermes delegate_tool）
	BgReview     *BackgroundReview // 后台自学习 review（nil = 不启用；复刻 hermes background_review）
	StatsData    *StatsDataRef     // 会话统计（nil = 不启用）

	// CurrentTask 最近/正在执行的任务（Run 开始时记录）。
	// 为何需要：a.Initial 只在 Run 结束后才追加本次对话，因此运行中执行
	// /handoff 时无从得知「当前在做什么」——而交接文档最该写的恰恰是这个。
	// 历史与最终回复可直接读 a.Initial（调用方在 Run 结束后已追加）。
	currentTask string
	runStateMu  sync.Mutex

	// AuxLLM 辅助模型（judge / background review 用；nil = 使用主 LLM）
	AuxLLM AuxiliaryLLM

	// AskUser 可选：模型通过 ask_user_question 工具向人类提问时的回调。
	// 参数为 questions JSON，返回 answers JSON；TUI 模式弹出输入等待用户，无界面环境返回降级提示。
	AskUser func(questionsJSON string) (string, error)

	// OnToolExchange 可选：每次工具真实执行后回调（供调用方持久化工具交换历史）。
	// tool/args 为调用参数；reason 为模型在调用该工具前说的一句说明文字（可为空）；
	// out/err 为执行结果（err 非空表示工具执行失败，out 可能为部分输出）。
	// 在工具执行线程同步调用，必须快速返回（勿阻塞/勿做重 IO；落盘请在回调内自行异步或任务结束时批量做）。
	// 典型用途：TUI/CLI 把工具调用+结果持久化到会话文件，会话恢复后模型能看到之前试过什么。
	OnToolExchange func(tool, args, reason, out string, err error)

	// OnCompact 可选：会话压缩成功后回调（供调用方持久化压缩记录）。
	// summary 为生成的摘要文本；droppedCount 是被摘要掉的消息条数（按会话消息计数，
	// 不含 system 前缀与时间提示）；tokensBefore 为压缩前估算。
	//
	// 调用方应把它作为一条追加记录落盘（不改写历史），下次 Load 时重建为
	// 「summary + 保留范围」—— 对齐 pi 的 CompactionEntry，避免跨进程丢失压缩成果。
	OnCompact func(summary string, droppedCount, tokensBefore int)

	// 工具调用解析策略
	callParser func(text string) (*callRequest, error)

	// systemPromptCache 缓存 SystemPrompt() 结果。
	// 缓存友好关键：system + 工具列表必须是字节级稳定前缀，
	// 任何一次调用都返回完全相同的文本，否则整个前缀缓存全部失效。
	systemPromptCache string

	// cacheStats 系统提示词缓存命中率统计
	cacheStats *CacheStats

	// 自愈（上下文溢出应急裁剪）状态，随每次 Run 重置。
	// emergencyAttempts 已尝试的递进档数；lastEmergencyPct 最后一档的裁剪比例。
	// 供调用方（TUI/CLI）在 Run 结束后落盘降级记录。
	emergencyAttempts int
	lastEmergencyPct  int
	// overflowCount 本次 Run 内累计的上下文溢出次数，用于推进三级降级策略。
	overflowCount int

	// OnEmergencyTrim 可选：应急裁剪发生时的回调（供调用方持久化降级记录）。
	// pct 为裁剪比例，keptMsgs 为裁剪后保留的消息数。
	// 持久化后，用户退出再进入会直接以裁剪后的上下文开始，不用重新撞 400。
	OnEmergencyTrim func(pct, keptMsgs int)

	// 快速插入（steer/abort，见 steer.go）
	steerMu  sync.Mutex // 保护 steer 槽位
	steer    *steerMsg  // 待插入消息（nil = 无）
	steerSeq uint64     // 序号，最新覆盖用
	aborted  atomic.Bool

	// runMu 保护当前 Run 的取消上下文（Abort() 时取消，中断在途 LLM 请求）
	runMu     sync.Mutex
	runCtx    stdctx.Context
	runCancel stdctx.CancelFunc

	// runActive 标记是否有 Run 正在执行。
	// 用途：Web/WS 端收到 steer（纠正消息）时判断它能否被当前循环消费——
	// 若为 false，说明循环已结束，该消息会被静默丢弃，调用方应改作新任务补发。
	runActive atomic.Bool

	// OnLLMStream 可选：LLM 回复流式增量回调（step = 当前循环步，从 0 起）。
	// 在 Run 的 LLM 请求 goroutine 内同步调用，必须快速返回（勿阻塞/勿做重 IO）。
	// 每个分片同时携带 Thinkings（思考过程）与 Content（内容，含控制 JSON）；
	// 步骤切换时由调用方观察 step 变化自行重置状态。
	// 为 nil 时不走流式路径（保持原一次性调用，用于纯非流式场景）。
	OnLLMStream func(step int, delta StreamDelta)

	// Commands 斜杠命令注册表（内置 + 插件 command_*）
	Commands *CommandRegistry
}

type callRequest struct {
	Action string `json:"action"` // "tool" | "reply"
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"` // 模型在调用工具前说的一句说明文字

	// Calls 本步要执行的工具调用列表（按出现顺序）。
	// 模型一轮可以合法地并行发多个工具调用（OpenAI 原生 tool_calls 本就是数组），
	// 由 LLM 层把它们全部展开到此字段；为空时回退到单数字段 Tool/Args。
	Calls []callRequest `json:"calls,omitempty"`

	// CallID 原生 tool_call id（来自 OpenAI tool_calls[].id）。
	// 回填结果时用作 tool_call_id；为空则分配一个合成 ID。
	CallID string `json:"callId,omitempty"`
}

// toolCallSeq 合成 tool_call id 的序号（进程内单调递增，保证唯一）。
var toolCallSeq atomic.Uint64

// nextToolCallID 生成一个合成的 tool_call id。
// 仅用于文本协议/弱模型场景（模型没给原生 id），格式对齐 OpenAI 风格。
func nextToolCallID() string {
	return fmt.Sprintf("call_%d", toolCallSeq.Add(1))
}

// SetCurrentTask 记录正在执行的任务（Run 开始时调用）。线程安全。
func (a *Agent) SetCurrentTask(task string) {
	a.runStateMu.Lock()
	defer a.runStateMu.Unlock()
	a.currentTask = task
}

// CurrentTask 返回正在执行/最近执行的任务（无记录时为空串）。线程安全。
func (a *Agent) CurrentTask() string {
	a.runStateMu.Lock()
	defer a.runStateMu.Unlock()
	return a.currentTask
}

// New 创建 Agent。
func New(llm LLM, pm *plugins.Manager) *Agent {
	a := &Agent{
		LLM:        llm,
		Plugins:    pm,
		MaxSteps:   maxStepsDefault,
		callParser: parseCallJSON,
		Commands:   NewCommandRegistry(),
		cacheStats: &CacheStats{},
		Guard:      NewRepeatGuard(nil),
		PlanMode:   NewPlanMode(),
	}
	// 插件导出的 command_* 函数注册为斜杠命令
	for _, c := range pm.Commands() {
		cc := c
		a.Commands.Register(Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
	}
	return a
}

// Model 返回模型标识（供 OpenAI 兼容端点 /v1/models 使用）。
func (a *Agent) Model() string {
	if lm, ok := a.LLM.(interface{ ModelName() string }); ok {
		return lm.ModelName()
	}
	return "mizar-agent"
}

// LLMInfo 返回底层 LLM 的 baseURL 与 apiKey（Web UI 拉模型清单用）。
func (a *Agent) LLMInfo() (string, string) {
	if lm, ok := a.LLM.(interface{ Info() (string, string) }); ok {
		return lm.Info()
	}
	return "", ""
}

// stableBasePrompt 系统提示的稳定前缀（identity + 工作方法 + 搜索定位）。
// 跨会话不变，保证 prefix cache 命中。
const stableBasePrompt = `你是开阳(Mizar) Agent，一个自举的编码智能体。你通过调用工具帮助用户完成任务。

## 工作方法
遵循以下高效工作流，避免盲目尝试：
1. 先理解任务所需的信息类型，再选择工具。
2. 优先使用 grep/find 进行定位搜索（快、便宜），再使用 read 读取具体行（offset/limit 精确指定）。
3. 一次工具调用尽量完成，不要重复试探同一任务。
4. 命令执行前考虑是否真的需要 bash；环境感知类问题（hostname、ip、进程、包）可用 bash 一次搞定，stderr 已并入 stdout。
5. 读取文件时务必填写正确的 offset（行号）和 limit（行数），read 支持分段读，不要反复全量读。
6. 工具失败时根据错误信息修正参数重试，不要盲目换工具。
7. 修改代码前先读要改的文件，改完给出摘要。
8. 回复要简洁，展示文件路径要清晰。

## 搜索与文件定位
- 当前工作目录已明确告知，**所有文件操作默认相对于当前目录**。
- 搜索文件时优先用 grep/find 在当前目录递归，不要盲目向上层目录或全局搜索。
- 只有在当前目录找不到目标时，才扩大搜索范围并说明原因。
- 使用绝对路径时优先使用相对于当前目录的路径（如 ./src/main.go 而不是 /home/user/project/src/main.go）。
`

// replyFormatSection 回复格式要求（稳定部分，不随工具变化）。
//
// 协议分两种模式：
//   - 原生 tool_call 模式（默认）：LLM 层发送 tools 参数，模型结构化返回 tool_calls，
//     回答走 respond 工具。系统提示只讲语义，不教手写 JSON/XML——避免模型被多套
//     格式指令搞混后手写畸形 JSON（教训：双格式提示导致模型输出
//     {"action":"tool",...} 畸形文本直接吐给用户）。
//   - 文本协议兑底（LLM 不支持 function calling 时）：由 LLM 层 Chat() 路径处理，
//     此时由调用方注入文本格式指令（WeakModelTuner 升级提示里已含）。
const replyFormatSection = `
## 回复方式
- 执行操作：调用对应工具（工具列表见下）
- 回答用户/宣布任务完成：调用 respond 工具，text 参数填你要说的内容
- 一次只调一个工具；工具结果返回后再决定下一步

## 边做边说
调用工具前可以先用一句话说明你要做什么，用户会看到它。
但**这句话必须和工具调用在同一轮发出**——只有说明文字、没有工具调用的那一轮，
会被当成「任务已结束」。

## 说了就要做（重要）
**不要只“宣布”而不执行**。❌ 反例：只说“我重写为纯注册表清理方式”就结束本轮
——用户只能再发一句“继续”，任务实际停滞。所以：
- 要动手就先把当前这一步的工具调用发出去（可在同一轮先写一句说明）；
- respond 只用于：回答提问、或**任务真的已完成**时交付结果；
- 多步骤/长时间任务：持续调工具直到真正做完，不要逐步汇报、不要等用户催。
`

// SystemPrompt 构建三层系统提示：stable（identity+基础指令）→ context（SOUL+AGENTS+工作目录）→ volatile（工具+插件）。
// 结果被缓存——只有 volatile 部分在插件热加载后失效，stable+context 前缀保持字节级稳定。
func (a *Agent) SystemPrompt() string {
	if a.systemPromptCache != "" {
		a.cacheStats.RecordHit()
		return a.systemPromptCache
	}
	a.cacheStats.RecordMiss()
	var sb strings.Builder

	// === 1. Stable 层：跨会话不变的 identity + 基础指令 ===
	sb.WriteString(stableBasePrompt)

	// === 2. Context 层：SOUL.md + 用户自定义指令 + 上下文文件 + 工作目录 ===
	sb.WriteString(a.buildContextSection())

	// === 3. Volatile 层：工具列表 + 插件信息（工具变化时通过 ReloadTools 失效缓存）===
	sb.WriteString(replyFormatSection)
	sb.WriteString(a.buildVolatileSection())

	a.systemPromptCache = sb.String()
	return a.systemPromptCache
}

// buildContextSection 构建 context 层：SOUL.md → 用户自定义指令 → AGENTS.md → 工作目录 → 计划模式。
// 这部分在会话内稳定，与 stable 层共同构成缓存前缀。
func (a *Agent) buildContextSection() string {
	var sb strings.Builder
	// SOUL.md（identity，最高优先级）
	if a.Soul != "" {
		sb.WriteString("\n## 身份声明（SOUL.md）\n")
		sb.WriteString(a.Soul)
		sb.WriteString("\n")
	}
	// 用户自定义指令（来自 main.go 的 a.System）
	if a.System != "" {
		sb.WriteString(a.System)
		sb.WriteString("\n")
	}
	// 上下文文件（AGENTS.md / .cursorrules / USER.md）
	if ctx := context.LoadContextFiles(a.WorkDir); ctx != "" {
		sb.WriteString(ctx)
	}
	// 工作目录
	if a.WorkDir != "" {
		sb.WriteString(fmt.Sprintf("\n## 当前工作目录\n%s\n", a.WorkDir))
	}
	// 计划模式引导（活跃时注入，退出时为空字符串）
	if section := a.PlanMode.SystemSection(); section != "" {
		sb.WriteString(section)
	}
	return sb.String()
}

// buildVolatileSection 构建 volatile 层：工具列表 + 技能索引 + 插件目录信息。
// 工具列表/技能变化时通过 ReloadTools 使缓存失效。
func (a *Agent) buildVolatileSection() string {
	var sb strings.Builder
	tools := a.Plugins.Tools()
	if len(tools) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}
	// 技能索引/正文（volatile：技能增删时通过 ReloadTools 失效缓存）
	if a.SkillsPrompt != "" {
		sb.WriteString(a.SkillsPrompt)
	}
	// 插件目录信息（引导模型自己创建/扩展插件）
	if a.PluginDir != "" {
		pluginNames := a.Plugins.PluginNames()
		sb.WriteString(fmt.Sprintf("\n## 插件\n插件目录: %s\n", a.PluginDir))
		if len(pluginNames) > 0 {
			sb.WriteString("已加载插件：\n")
			for _, p := range pluginNames {
				sb.WriteString(fmt.Sprintf("- %s\n", p))
			}
		}
		sb.WriteString("\n如需扩展能力，在插件目录新建 .ts 文件，导出 tool_* 函数（工具）或 command_* 函数（斜杠命令）。" +
			"用 write/edit 写入插件目录内的 .ts/.js 文件时会自动热重载并立即生效；若未自动生效可调用 reload_plugins 工具手动重载。" +
			"注意：/reload 是用户侧斜杠命令，不要用 bash 执行它（会报 command not found）。" +
			"支持 host_listen 宿主函数启动 HTTP 服务器。\n")
		// 宿主函数清单：插件 TS/JS 内可直接调用这些 API（信息来自 internal/engine 权威文档）
		sb.WriteString("\n插件中可用的宿主函数（插件代码内可直接调用，无需 import）：\n")
		sb.WriteString(engine.HostDocBriefs())
		// 最小可运行示例：让模型照着写而不用去逆向源码/二进制
		sb.WriteString("\n最小插件示例（存入插件目录后调用 reload_plugins 或自动重载后生效）：\n")
		sb.WriteString(
			"```ts\n" +
				"// hello.ts — 导出 tool_* 即注册为工具，参数是 JSON 字符串\n" +
				"// 重要：tool_ 函数上方写 JSDoc 注释（做什么/何时用/参数含义），\n" +
				"// 注释会成为系统提示里的工具描述——不写注释，后续模型不知道何时/如何用这个工具\n" +
				"/** 用 hello 工具向某人问好。何时用：需要生成问候语时。参数: {name: string} 人名 */\n" +
				"export function tool_hello(args: string): string {\n" +
				"  const p = JSON.parse(args);\n" +
				"  return \"你好, \" + (p.name || \"朋友\");\n" +
				"}\n" +
				"// 导出 command_* 即注册为斜杠命令，args 为命令后的参数文本\n" +
				"export function command_greet(args: string): string {\n" +
				"  return \"你好, \" + args;\n" +
				"}\n" +
				"```\n")
	}
	return sb.String()
}

// ReloadTools 插件热加载后调用：使 SystemPrompt 缓存失效。
// 注意：缓存友好约束下，热加载会破坏前缀缓存一次——这是显式 trade-off，
// 调用方应仅在"确实新增工具"时使用，而非每次轮询都调用。
func (a *Agent) ReloadTools() {
	a.systemPromptCache = ""
}

// Run 执行任务。返回最终回复。
func (a *Agent) Run(task string) (string, error) {
	// MaxSteps 语义：>0 有限步数；-1 表示无限（用户通过 /config max_steps -1 设置）；
	// 0 表示未设置 → 落到默认 maxStepsDefault。
	// 注意不能用 <=0，否则会把 -1（无限）误当成“未设置”而改写回默认值。
	unlimited := a.MaxSteps == -1
	if a.MaxSteps == 0 {
		a.MaxSteps = maxStepsDefault
	}
	if a.Hooks == nil {
		a.Hooks = NewHooks()
	}
	a.resetAbort()
	// 标记 Run 活跃：steer/abort 调用方可据此判断消息能否被当前循环消费。
	a.runActive.Store(true)
	defer a.runActive.Store(false)
	// 创建 Run 级取消上下文：Abort()（ESC 取消）时立即中断在途 LLM 请求，
	// 任务 goroutine 不再阻塞到流自然结束（否则 TUI 的 canceling 状态无法解除，
	// 新消息只能排队等待）。Run 结束自动清理。
	diag.Logf("agent", "Run 开始: task=%q initial=%d maxSteps=%d", truncate(task, 80), len(a.Initial), a.MaxSteps)
	// 记录当前任务（开始时写，使运行中执行 /handoff 也能看到「在做什么」）。
	// 历史与最终回复从 a.Initial 读（调用方在 Run 结束后追加），无需在此重复记录。
	a.SetCurrentTask(task)
	abortCtx, abortCancel := stdctx.WithCancel(stdctx.Background())
	a.setRunCancel(abortCtx, abortCancel)
	defer a.setRunCancel(nil, nil)
	if a.Tuner != nil {
		a.Tuner.Reset()
	}
	if a.Guard != nil {
		a.Guard.Reset()
	}
	// 重置自愈状态：每次 Run 重新计数递进档位。
	a.emergencyAttempts = 0
	a.lastEmergencyPct = 0
	a.overflowCount = 0
	q := lifecycle.NewQuery("cli")
	runCtx := &HookContext{
		RunID:     string(q.QueryID()),
		Task:      task,
		AgentName: "mizar",
	}
	a.Hooks.fireRunStart(runCtx, a.logf)
	msgs := []Message{
		{Role: RoleSystem, Content: a.SystemPrompt()},
	}
	// 时间感知：历史消息可能隔了几天（用户随时回来继续）。
	// ① 每轮都告知当前实时时间，避免模型用旧上下文推断“现在”；
	// ② 距上一条消息超过阈值时插一条时间提示，显式打破“上下文连贯”的错觉。
	// 两者都作为增量追加在 system 之后 / 历史之前，不污染 Initial（调用方后续仍会追加消息），
	// 也保留 system 前缀缓存（只动其后的内容）。
	if tc := TimeContext(nowTimestamp()); tc != "" {
		msgs = append(msgs, Message{Role: RoleSystem, Content: tc})
	}
	if notice := HistoryTimeNotice(a.Initial, TimeNow()); notice != "" {
		msgs = append(msgs, Message{Role: RoleUser, Kind: KindSummary, Content: "【时间提示】" + notice})
	}
	msgs = append(msgs, a.Initial...)
	// histLen 记录「会话消息」在 msgs 中的结束位置（前缀 + Initial 之后），
	// 供压缩回调把切点换算成会话消息条数。
	histEnd := len(msgs)
	msgs = append(msgs, Message{Role: RoleUser, Content: task})
	// 标记人类直接消息（用于 goal create/edit/pause/resume 权限校验）
	if a.GoalService != nil {
		a.GoalService.MarkHumanTurn()
	}

	step := 0
	stepWarned := false // 步数预算预警只提醒一次
	var qc lifecycle.QueryContext
	for unlimited || step < a.MaxSteps {
		q.BeginStep()
		qc = q.Context()
		stepCtx := &HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}
		// StepStart 挂载点（🟡 中等：可读，不建议改 Messages）
		a.Hooks.fireStepStart(stepCtx, a.logf)

		// 步数预算预警：有限步数下剩余不足时提醒模型收敛（只注入一次，避免刷屏）。无限模式（-1）不提醒。
		if !unlimited {
			if remain := a.MaxSteps - step; remain <= maxStepsWarnRemain && !stepWarned {
				stepWarned = true
				msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf(
					"【系统提醒】剩余步骤仅 %d 步（当前上限 %d）。若任务已基本完成，请立即用 reply 输出最终回答；"+
						"若仍需操作，请合并为一次工具调用（如一次 bash 完成多项检查/修改）快速收尾", remain, a.MaxSteps)})
				a.log("%s", q.FormatLog("step_budget_warn", "remain=", fmt.Sprintf("%d", remain)))
			}
		}

		// 快速插入检查
		if sm := a.drainSteer(); sm != nil {
			a.log("%s", q.FormatLog("steer", "step=", fmt.Sprintf("%d", qc.Step), " content=", truncate(sm.content, 80)))
			msgs = append(msgs, Message{Role: RoleUser, Content: "【用户快速纠正】" + sm.content})
		}
		// 后台任务完成通知：上一步后台任务已结束但模型尚未感知时，注入通知（对齐 dsh job 完成通知注入）
		if a.Jobs != nil {
			for _, note := range a.Jobs.DrainDone() {
				a.log("%s", q.FormatLog("job_done_notice", "id=", note))
				msgs = append(msgs, Message{Role: RoleUser, Content: "【后台任务完成】" + note})
			}
		}
		if a.Aborted() {
			diag.Logf("agent", "abort 检查点命中（循环头）step=%d", qc.Step)
			q.Complete(errors.New("aborted by user"))
			a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: errors.New("aborted by user")}, a.logf)
			return "", ErrAborted
		}

		// 压缩检查
		if a.Compactor != nil {
			est := EstimateMessages(msgs)
			if a.Compactor.ShouldCompact(est) {
				a.log("%s", q.FormatLog("compact", "est=", fmt.Sprintf("%d", est), " step=", fmt.Sprintf("%d", qc.Step)))
				a.Hooks.fireCompactionBefore(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, EstTokens: est, Messages: msgs}, a.logf)
				// 先算切点（纯函数，与 Compact 内部一致），供落盘记录保留边界。
				cut := a.Compactor.FindCutPoint(msgs)
				var err error
				msgs, err = a.Compactor.Compact(msgs)
				if err != nil {
					a.log("%s", q.FormatLog("compact_fail", "err=", err.Error()))
				} else {
					a.log("%s", q.FormatLog("compact", "result=", fmt.Sprintf("%d msgs, %d tokens", len(msgs), EstimateMessages(msgs))))
					// 真正瘦身了才落盘压缩记录（cut<=0 时 Compact 原样返回，不算压缩）。
					if a.OnCompact != nil && cut > 0 && len(msgs) > 0 {
						// cut 是 msgs 下标；换算成「被摘要掉的会话消息条数」：
						// 只有落在会话消息区间（≤histEnd）的部分才计入文件位置。
						dropped := cut
						if dropped > histEnd {
							dropped = histEnd
						}
						if dropped > 0 {
							a.OnCompact(summaryOf(msgs), dropped, est)
						}
					}
				}
				a.Hooks.fireCompactionAfter(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Compacted: true, EstTokens: est, Summary: summaryOf(msgs), Messages: msgs}, a.logf)
			}
		}

		// LLMRequest 挂载点（🔴 严重）
		q.BeginOperation("llm")
		llmCtx := &HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}
		a.Hooks.fireLLMRequest(llmCtx, a.logf)
		msgs = llmCtx.Messages

		var reply string
		var llmErr error
		if a.OnLLMStream != nil {
			// 流式路径：回调每个分片（含思考/内容），UI 侧增量渲染
			// 注意：将 OnLLMStream 捕获到局部变量，避免 TOCTOU 竞态条件
			// （另一个 goroutine 可能在检查后、调用前将 OnLLMStream 设为 nil）
			onStream := a.OnLLMStream
			// 首次分片附带本步输入 token 估算（供 TUI 实时展示；复用下游 compaction 同口径 EstinateMessages）
			var promptEstSent bool
			emit := func(d StreamDelta) {
				if !promptEstSent {
					promptEstSent = true
					d.PromptEst = EstimateMessages(msgs)
				}
				onStream(qc.Step, d)
			}
			// 优先使用支持取消的流式接口（ESC 取消可中断在途 HTTP 请求）；
			// 不支持时回退旧接口（取消仍生效，但需等 LLM 自然返回）
			if toolLLM, ok := a.LLM.(CancellableStreamToolCallLLM); ok {
				reply, llmErr = toolLLM.ChatWithToolsStreamCtx(a.currentRunCtx(), msgs, a.Plugins.Tools(), emit)
			} else if llmWithContext, ok := a.LLM.(LLMWithContext); ok {
				// 支持上下文取消的一次性调用接口：非流式路径也可中断在途请求
				reply, llmErr = llmWithContext.ChatWithToolsCtx(a.currentRunCtx(), msgs, a.Plugins.Tools())
			} else if toolLLM, ok := a.LLM.(StreamToolCallLLM); ok {
				reply, llmErr = toolLLM.ChatWithToolsStream(msgs, a.Plugins.Tools(), emit)
			} else if sllm, ok := a.LLM.(StreamLLM); ok {
				reply, llmErr = sllm.ChatStream(msgs, emit)
			} else if toolLLM, ok := a.LLM.(ToolCallLLM); ok {
				// LLM 不支持流式且不支持上下文取消：回退一次性调用（ESC 仅设置标志位，需等 LLM 自然返回）
				reply, llmErr = toolLLM.ChatWithTools(msgs, a.Plugins.Tools())
			} else {
				reply, llmErr = a.LLM.Chat(msgs)
			}
		} else {
			// 非流式路径（原逻辑）
			// 优先检查支持上下文取消的接口
			if llmWithContext, ok := a.LLM.(LLMWithContext); ok {
				reply, llmErr = llmWithContext.ChatWithToolsCtx(a.currentRunCtx(), msgs, a.Plugins.Tools())
			} else if toolLLM, ok := a.LLM.(ToolCallLLM); ok {
				// 原生工具调用：发送 tools 参数，模型结构化返回
				reply, llmErr = toolLLM.ChatWithTools(msgs, a.Plugins.Tools())
			} else {
				reply, llmErr = a.LLM.Chat(msgs)
			}
		}
		q.EndOperation("llm")
		if llmErr != nil {
			diag.Logf("llm", "请求失败 step=%d aborted=%v err=%v", qc.Step, a.Aborted(), llmErr)
			// 用户已请求中止（ESC 取消中断了在途 LLM 请求）：立即退出，
			// 不走重试/压缩/弱模型宽容（否则 abort 后可能因重试继续空转）。
			if a.Aborted() {
				q.Complete(errors.New("aborted by user"))
				a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: errors.New("aborted by user")}, a.logf)
				return "", ErrAborted
			}
			// Context overflow：不走重试，直接强制压缩后继续。
			// ⚠ 关键：这里不再用 ShouldCompact（本地启发式估算）否决——服务端 400
			// 已明确报超限，而 EstimateMessages 对工具输出/JSON/base64 严重低估，
			// 曾导致「服务端说 500K、本地估算才 100K」→ 不压缩 → 任务直接失败。
			if isContextOverflow(llmErr) {
				a.log("%s", q.FormatLog("llm_overflow", "err=", truncate(llmErr.Error(), 100)))
				a.overflowCount++
				if a.Compactor != nil {
					est := EstimateMessages(msgs)
					// 三级降级，按「同一轮 Run 内的溢出次数」推进，避免第一步反复
					// continue 成功但实际仍超限、导致后面更狠的手段永远轮不到：
					//   第 1 次溢出 → 强制压缩（保留最近约一半）
					//   第 2 次溢出 → 按窗口 1/4 硬截断
					//   第 3 次起   → 递进自愈 10%/20%/40%/60%
					if a.overflowCount == 1 {
						a.log("%s", q.FormatLog("compact_force", "est=", fmt.Sprintf("%d", est)))
						msgs2, compErr := a.Compactor.CompactForce(msgs, a.Compactor.KeepRecentTokens/2)
						if compErr == nil && len(msgs2) < len(msgs) {
							msgs = msgs2
							a.log("%s", q.FormatLog("compact_force_result", "msgs=", fmt.Sprintf("%d", len(msgs))))
							continue
						}
						if compErr != nil {
							a.log("%s", q.FormatLog("compact_force_fail", "err=", compErr.Error()))
						}
					} else if a.overflowCount == 2 {
						if tr := hardTrimOnOverflow(msgs, a.Compactor.ContextWindow/4); len(tr) < len(msgs) {
							msgs = tr
							a.log("%s", q.FormatLog("compact_hardtrim", "msgs=", fmt.Sprintf("%d", len(msgs))))
							continue
						}
					}
				}
				// 自愈：压缩与硬截断都没解决问题（典型场景：context_window 配得比模型
				// 真实上限大，本地估算无从判断），逐步递进裁剪后重试。
				// 步长 10% → 20% → 40% → 60%，每试一档重发一次请求。
				steps := emergencyTrimSteps()
				if a.emergencyAttempts < len(steps) {
					pct := steps[a.emergencyAttempts]
					a.emergencyAttempts++
					trimmed := emergencyTrim(msgs, pct)
					if EmergencyChanged(msgs, trimmed) {
						msgs = trimmed
						a.lastEmergencyPct = pct
						a.log("%s", q.FormatLog("emergency_trim",
							"pct=", fmt.Sprintf("%d", pct),
							"msgs=", fmt.Sprintf("%d", len(msgs)),
							"est=", fmt.Sprintf("%d", EstimateMessages(msgs))))
						// 告知用户：已进入降级模式（不能静默“失忆”）。
						a.notifyEmergency(pct, len(msgs))
						continue // 裁剪后重试
					}
				}
				// 无压缩器：当作普通错误处理
			}
			// 不可重试错误：直接失败，不消耗重试预算
			if isNonRetryableError(llmErr) {
				if a.Tuner != nil {
					a.Tuner.LLMSucceeded() // 重置计数器（不算失败）
				}
				q.Complete(llmErr)
				a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: llmErr}, a.logf)
				return "", fmt.Errorf("llm chat: %w", llmErr)
			}
			if a.Tuner != nil {
				if retry, retryCnt := a.Tuner.LLMFailed(); retry {
					time.Sleep(a.Tuner.RetryDelay(retryCnt))
					a.log("%s", q.FormatLog("llm_retry", "cnt=", fmt.Sprintf("%d", retryCnt)))
					continue
				}
			}
			q.Complete(llmErr)
			a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: llmErr}, a.logf)
			return "", fmt.Errorf("llm chat: %w", llmErr)
		}
		if a.Tuner != nil {
			a.Tuner.LLMSucceeded()
		}
		a.Hooks.fireLLMResponse(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: reply}, a.logf)
		a.log("%s", q.FormatLog("model_reply", "step=", fmt.Sprintf("%d", qc.Step), " len=", fmt.Sprintf("%d", len(reply))))

		req, err := a.callParser(reply)
		if err != nil {
			if a.Tuner != nil {
				if escalated, msg := a.Tuner.ParseFailed(qc.Step); escalated {
					msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
					msgs = append(msgs, Message{Role: RoleUser, Content: msg})
					a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
					step++
					continue
				}
			}
			a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: fmt.Errorf("parse: %w", err)}, a.logf)
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
			msgs = append(msgs, Message{Role: RoleUser, Content: "解析你的回复失败：" + err.Error() + "。请严格按格式输出 JSON。"})
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			step++
			continue
		}
		if a.Tuner != nil {
			a.Tuner.ParseSucceeded()
		}

		// 中止检查点：ESC 中止发生在 LLM 请求进行中时，请求返回后立即退出，
		// 不再执行随后的工具调用（否则用户看到的「已取消」之后还会多出一串工具步骤）。
		if a.Aborted() {
			diag.Logf("agent", "abort 检查点命中（解析后）step=%d", qc.Step)
			q.Complete(errors.New("aborted by user"))
			a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Err: errors.New("aborted by user")}, a.logf)
			return "", ErrAborted
		}

		if req.Action == "reply" {
			q.Complete(nil)
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: req.Text, Messages: msgs}, a.logf)
			a.Hooks.fireRunEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: req.Text}, a.logf)
			return req.Text, nil
		}

		// 工具调用（本步可能是模型一轮发起的**多个**调用，按出现顺序依次执行）
		calls := req.Calls
		if len(calls) == 0 {
			// 单数形态（旧协议 / XML / bare）：归一为一个调用
			calls = []callRequest{{Action: "tool", Tool: req.Tool, Args: req.Args, CallID: req.CallID}}
		}
		var terminated bool
		for i, c := range calls {
			c.Reason = req.Reason // reason（模型同一轮的开场说明）归给所有调用
			fin, isTerminate, err := a.runOneToolCall(&c, i, len(calls), reply, msgs, q, qc, task)
			if err != nil {
				return "", err
			}
			msgs = fin.Msgs
			if fin.Control == controlReturn {
				return fin.Reply, nil
			}
			if fin.Control == controlNextStep {
				// 该调用已自行收尾（参数校验/guard 提醒）；剩余调用不再执行
				break
			}
			if isTerminate {
				// 终止型工具已给出最终回答：剩余调用必须跳过，否则会被执行（错误的副作用）
				terminated = true
				break
			}
		}
		if terminated {
			// 终止型工具已给出最终回答，跳过后续 judge/review
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			return lastToolOutput(msgs), nil
		}
		if len(calls) == 0 {
			// 理论不可达（calls 至少有一个）；保险起见推进步数避免死循环
			step++
			continue
		}

		// GoalLoop judge：每轮后判断目标是否完成，自动续跑（🟡 ImpactModerate）
		if a.GoalLoop != nil {
			_, continuationPrompt, judgeErr := a.GoalLoop.EvaluateAfterTurn(reply)
			if judgeErr != nil {
				a.log("%s", q.FormatLog("goal_judge_error", "err=", judgeErr.Error()))
			} else if continuationPrompt != "" {
				// 判定「有 continuation prompt 即续跑」：非空 prompt 与 shouldContinue
				// 语义绑定，用单一条件可避免实现方漏置 shouldContinue 时提示被静默丢弃。
				msgs = append(msgs, Message{Role: RoleUser, Content: continuationPrompt})
				a.log("%s", q.FormatLog("goal_continue", "step=", fmt.Sprintf("%d", qc.Step)))
				step++
				a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
				continue
			}
		}

		// BackgroundReview：每轮后异步触发 skill/memory 沉淀（🟢 ImpactSafe）
		if a.BgReview != nil {
			a.BgReview.Advance(reply, msgs)
		}

		a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
		step++
	}
	stepExceededErr := fmt.Errorf("%w（已用满 %d 步）。可调大上限: 输入 /config max_steps 60 立即生效并持久化，或在 ~/.mizar/config.json 设置 \"max_steps\" 字段", ErrMaxSteps, a.MaxSteps)
	q.Complete(stepExceededErr)
	a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Task: task, Err: stepExceededErr}, a.logf)
	return "", stepExceededErr
}

// callControl 单次工具调用执行后的控制流指示。
type callControl int

const (
	// controlContinue 正常：继续执行本批的下一个调用，或进入下一轮 LLM。
	controlContinue callControl = iota
	// controlReturn 本调用已给出最终回答（respond / 终止型工具），Run 应立即返回。
	controlReturn
	// controlNextStep 本调用已自行收尾（参数校验失败 / guard 提醒），
	// 剩余调用不再执行，直接进入下一轮。
	controlNextStep
)

// callDirective 单次工具调用的执行结果（供批量循环汇总）。
type callDirective struct {
	Msgs    []Message // 更新后的消息列表
	Reply   string    // Control==controlReturn 时的最终回答
	Control callControl
}

// runOneToolCall 执行单个工具调用，返回（更新后的本批消息、控制流、错误）。
//
// 从原 Run 内联块提取而来，供批量执行循环复用。所有分支语义（respond 直接回答、
// 空参数回填纠错、guard 观察与终止、终止型工具）逐条保持一致，只是把原来的
// `continue`（推进步数、进入下一轮）换成 controlNextStep 返回。
//
// idx/total 仅用于日志与回复文本（标明这是第几个调用，便于诊断多调用场景）。
func (a *Agent) runOneToolCall(c *callRequest, idx, total int, reply string, msgs []Message,
	q *lifecycle.QueryLifecycle, qc lifecycle.QueryContext, task string) (callDirective, bool, error) {

	// 为本次调用分配原生 tool_call id：后续回填的 tool 结果用它配对
	// （OpenAI 要求 role:"tool" 消息必须带对应的 tool_call_id）。
	callID := c.CallID
	if callID == "" {
		callID = nextToolCallID()
	}

	q.BeginOperation("tool:" + c.Tool)
	a.Hooks.fireToolCall(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: c.Tool, Args: c.Args, Reason: c.Reason}, a.logf)

	// 空工具名兜底（规避 `tool "" not found`）：模型在某些形态下丢了 tool 字段，
	// 但 args 里带了 bash 的 command 参数——此时猜为 bash 工具。
	if c.Tool == "" {
		if trimmed := strings.TrimSpace(c.Args); strings.Contains(trimmed, "\"command\":") {
			c.Tool = "bash"
		}
	}

	// respond 工具：直接回答出口。不执行不回填，text 参数作为最终回答返回。
	// 给 tool_choice=required 模式下的模型一条正规的"回答"路径，
	// 避免模型被迫硬调无关工具或手写畸形 JSON。
	if c.Tool == "respond" {
		q.EndOperation("tool:" + c.Tool)
		text := respondText(c.Args)
		if text == "" {
			msg := "⚠️ respond 工具调用缺少 text 参数。请重新调用 respond，并在 text 参数中填入你要说的内容。"
			// ⚠ 不得标 KindToolCall：该消息没有 ToolCalls 字段，也没有对应的工具结果。
			// encodeMessages 见到 KindToolCall 会从 Content 回退出一个 tool_call（合成 ID），
			// 而后续只有一条普通 user 纠正提示 → 发出「有 tool_calls 无 role:tool 结果」的
			// 非法序列，上游报 400「tool calls and tool results do not match」（2026-10-16 实测）。
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
			msgs = append(msgs, Message{Role: RoleUser, Content: msg})
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			return callDirective{Msgs: msgs, Control: controlNextStep}, false, nil
		}
		// 更新 forcedToolCalls 计数器：模型调了 respond 也算调工具（重置计数器）
		if tllm, ok := a.LLM.(interface{ updateForcedToolCalls(v bool) }); ok {
			tllm.updateForcedToolCalls(true)
		}
		q.Complete(nil)
		a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: text, Messages: msgs}, a.logf)
		a.Hooks.fireRunEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: text}, a.logf)
		return callDirective{Msgs: msgs, Reply: text, Control: controlReturn}, false, nil
	}

	// 空参数兑底：不执行工具，把错误发回模型让它修正（防止"args {command} required"空转）
	trimmedArgs := strings.TrimSpace(c.Args)
	if trimmedArgs == "" || trimmedArgs == "{}" {
		q.EndOperation("tool:" + c.Tool)
		msg := fmt.Sprintf("⚠️ 工具 %s 调用缺少参数（args 为空）。请重新调用，并在 args 中传入正确的 JSON 参数。", c.Tool)
		// ⚠ 同 respond 分支：无 ToolCalls、无工具结果，标 KindToolCall 会让编码层回退出
		// 孤儿 tool_calls → 上游 400（详见上方注释）。
		msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
		msgs = append(msgs, Message{Role: RoleUser, Content: msg})
		a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
		return callDirective{Msgs: msgs, Control: controlNextStep}, false, nil
	}

	// 先执行工具（dsh 的 repeat-tool-reminder 也是 post-execute 观察语义）
	out, err := a.Plugins.Call(c.Tool, c.Args)
	q.EndOperation("tool:" + c.Tool)
	// 工具交换回调：供调用方持久化（会话记录工具调用过程/结果）
	if a.OnToolExchange != nil {
		a.OnToolExchange(c.Tool, c.Args, c.Reason, out, err)
	}
	tr := ToolResult{ToolName: c.Tool, Args: c.Args, Output: out}
	if err != nil {
		tr.Error = err.Error()
		a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: c.Tool, Args: c.Args, Err: err}, a.logf)
	}
	b, _ := json.Marshal(tr)

	// Guard 观察：记录此次调用后触发渐进提醒或终止（dsh semantics：observe-and-enrich, never veto, kill on max threshold exceed）
	if a.Guard != nil {
		if reminder, terminate := a.Guard.Observe(c.Tool, c.Args); terminate {
			msgs = append(msgs, Message{
				Role: RoleAssistant, Content: reply, Kind: KindToolCall,
				ToolName: c.Tool, ToolArgs: c.Args,
				ToolCalls: []ToolCallRef{{ID: callID, Name: c.Tool, Args: c.Args}},
			})
			msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b), Kind: KindToolResult, ToolCallID: callID})
			msgs = append(msgs, Message{Role: RoleUser, Content: reminder + "\n⚠️ 任务因重复工具调用已终止。"})
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			return callDirective{}, false, fmt.Errorf("%w: tool=%s 连续重复 %d 次，任务终止", errors.New("loop guard terminated"), c.Tool, a.Guard.MaxObserved())
		} else if reminder != "" {
			// 渐进提醒，注入为追加用户消息（紧接工具结果之后，模型可据此改参数或停手）
			msgs = append(msgs, Message{
				Role: RoleAssistant, Content: reply, Kind: KindToolCall,
				ToolName: c.Tool, ToolArgs: c.Args,
				ToolCalls: []ToolCallRef{{ID: callID, Name: c.Tool, Args: c.Args}},
			})
			msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b), Kind: KindToolResult, ToolCallID: callID})
			msgs = append(msgs, Message{Role: RoleUser, Content: reminder})
			a.log("%s", q.FormatLog("guard_reminder", "step=", fmt.Sprintf("%d", qc.Step), "tool=", c.Tool, "cnt=", fmt.Sprintf("%d", a.Guard.Count())))
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			return callDirective{Msgs: msgs, Control: controlNextStep}, false, nil
		}
	}
	msgs = append(msgs, Message{
		Role: RoleAssistant, Content: reply, Kind: KindToolCall,
		ToolName: c.Tool, ToolArgs: c.Args,
		ToolCalls: []ToolCallRef{{ID: callID, Name: c.Tool, Args: c.Args}},
	})
	msgs = append(msgs, Message{
		Role: RoleUser, Content: "工具结果: " + string(b), Kind: KindToolResult,
		ToolCallID: callID,
	})
	a.Hooks.fireToolResult(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: c.Tool, Args: c.Args, Result: string(b), Err: err, Messages: msgs}, a.logf)

	// 终止型工具（对齐 pi 0.69.0 `terminate: true`）：执行成功且工具声明 Terminate 时，
	// 直接以工具输出作为最终回答，省掉一次 follow-up LLM 调用（延迟 + 成本）。
	// 执行失败时忽略声明：错误要回填给模型，给它纠正的机会。
	if err == nil && a.isTerminatingTool(c.Tool) {
		if final := strings.TrimSpace(out); final != "" {
			a.log("%s", q.FormatLog("tool_terminate", "step=", fmt.Sprintf("%d", qc.Step), "tool=", c.Tool))
			// 标记本轮终止：由调用方返回 lastToolOutput 作为最终回答
			return callDirective{Msgs: msgs}, true, nil
		}
	}
	return callDirective{Msgs: msgs, Control: controlContinue}, false, nil
}

// lastToolOutput 取消息流末尾的最后一个工具结果内容（终止型工具已写入）。
func lastToolOutput(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind != KindToolResult {
			continue
		}
		content := msgs[i].Content
		const prefix = "工具结果: "
		if !strings.HasPrefix(content, prefix) {
			continue
		}
		var tr ToolResult
		if err := json.Unmarshal([]byte(content[len(prefix):]), &tr); err != nil {
			continue
		}
		return strings.TrimSpace(tr.Output)
	}
	return ""
}

// summaryOf 提取消息流中的摘要（供 CompactionAfter 查看），无则返回空。
func summaryOf(msgs []Message) string {
	for _, m := range msgs {
		if m.Kind == KindSummary {
			return m.Content
		}
	}
	return ""
}

// logf 适配 fire 挂载点的日志回调签名。
// notifyEmergency 应急裁剪的用户可见提示（日志 + 回调）。
//
// 不能静默裁剪：模型对旧对话“失忆”必须让用户知道，否则会以为 agent 出了 bug。
func (a *Agent) notifyEmergency(pct, keptMsgs int) {
	msg := fmt.Sprintf("⚠ 上下文超限：已临时裁剪 %d%% 历史消息（保留 %d 条），原始记录仍完整保存在会话文件中", pct, keptMsgs)
	a.logf(msg)
	if a.OnEmergencyTrim != nil {
		a.OnEmergencyTrim(pct, keptMsgs)
	}
}

func (a *Agent) logf(msg string) {
	a.log("%s", msg)
}

func (a *Agent) log(format string, args ...any) {
	if a.VerboseLog != nil {
		a.VerboseLog(fmt.Sprintf(format, args...))
	}
}

// parseCallJSON 从模型回复中提取 action 控制指令。
// 返回首个调用（保持原有单数 API，供旧调用方与测试用）；
// 需要拿到模型一轮发出的**全部**工具调用时请用 parseCallJSONs。
func parseCallJSON(text string) (*callRequest, error) {
	reqs, err := parseCallJSONs(text)
	if err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("空的控制指令")
	}
	return &reqs[0], nil
}

// parseCallJSONs 从模型回复中提取**全部**控制指令（保持出现顺序）。
//
// 支持格式：
//  1. 纯 JSON: {"action":"tool","tool":"bash","args":"{}"}
//  2. XML 包裹: <tool name="bash">{"command": "ls"}</tool>
//  3. 混合文本: 先解释...\n<tool name="ls">{"path": "."}</tool>
//  4. 并行多调用: 一轮发多个工具调用（原生 tool_calls 或文本里多个 JSON 对象）
//
// 多调用支持修复的问题：模型合法地并行发多个工具调用时，旧实现只取首个，
// 其余被静默丢弃——模型的写操作会凭空消失。现按出现顺序全部返回，由循环依次执行。
func parseCallJSONs(text string) ([]callRequest, error) {
	// 优先尝试 XML 格式解析（可能命中多个 <tool> 标签）
	if reqs := extractActionXML(text); len(reqs) > 0 {
		return reqs, nil
	}
	// JSON 格式：可能命中多个
	extracted := ExtractAllActionJSON(text)
	if len(extracted) == 0 {
		// 检测到「想调工具但 JSON 畸形」：缺 tool 字段 / args 内层引号未转义等。
		// 给出针对性纠错提示（比通用提示更能帮助弱模型快速自纠，避免反复失败）。
		if IsToolCallText(text) {
			return nil, fmt.Errorf("检测到工具调用但格式错误（args 值内部的引号需转义为 \\\"，或缺少 tool 字段）。\n正确格式：{\"action\":\"tool\",\"tool\":\"bash\",\"args\":\"{\\\"command\\\": \\\"ls\\\"}\"}\n注意：args 是一个 **JSON 字符串**（不是嵌套对象），内部引号必须转义。")
		}
		return nil, fmt.Errorf("无法识别的控制指令：未找到含 action 字段的合法 JSON 对象\n请输出 JSON：{\"action\":\"tool\",\"tool\":\"工具名\",\"args\":\"{...}\"}，或完成任务时：{\"action\":\"reply\",\"text\":\"最终回答\"}")
	}
	reqs := make([]callRequest, 0, len(extracted))
	for _, one := range extracted {
		req, err := parseOneCallJSON(one)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, *req)
	}
	return reqs, nil
}

// parseOneCallJSON 解析单个已提取的 JSON 对象（含自动修复与 bare 归一化）。
func parseOneCallJSON(extracted string) (*callRequest, error) {
	var reqJSON callRequest
	if err := json.Unmarshal([]byte(extracted), &reqJSON); err != nil {
		fixed := tryFixJSON(extracted)
		if fixed != extracted {
			if err2 := json.Unmarshal([]byte(fixed), &reqJSON); err2 == nil {
				return &reqJSON, nil
			}
		}
		return nil, fmt.Errorf("JSON 格式错误：%v\n原始输出片段：%s\n请确保 JSON 完整闭合；args 必须是转义后的 JSON 字符串", err, truncate(extracted, 80))
	}
	if reqJSON.Action == "" {
		// 原生 bare 工具调用（缺 action 字段）：{"tool":...,"args":...}
		// 含 tool 字段即视为工具调用（args 可为空字符串，循环内做空参数兜底），归一化为标准工具调用。
		if reqJSON.Tool != "" {
			reqJSON.Action = "tool"
			return &reqJSON, nil
		}
		return nil, fmt.Errorf("缺少 action 字段：提取到的 JSON 没有 action 字段\n请输出：{\"action\":\"tool\",\"tool\":\"工具名\",\"args\":\"{...}\"} 或 {\"action\":\"reply\",\"text\":\"回答\"}")
	}
	return &reqJSON, nil
}

// extractActionXML 从 XML 标签格式中提取控制指令（返回**全部**，保序）。
//
// 模型一轮可以合法地发多个 <tool> 标签（与原生 tool_calls 数组等价）。
// 旧实现用 FindStringSubmatch 只取首个，其余被**静默丢弃**——模型的操作凭空消失。
// 现改为 FindAllStringSubmatch 逐段解析，并把 arg_key/arg_value 与
// 直接 body 两种形态的分隔符从贪婪改为**非贪婪**（否则多个标签会互相吞并）。
//
// 返回 nil 表示未识别到 XML 控制指令（调用方回退 JSON 解析）。
func extractActionXML(text string) []callRequest {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil
	}
	// 移除开头的 { (模型有时会输出 )
	if strings.HasPrefix(s, "{") {
		s = s[1:]
	}

	// 模式1: <tool name="x">JSON</tool>（args 直接是 JSON 体）
	//
	// 正则用 `([\s\S]*?)</tool>` 而非 `([^<]+)</tool>`，且**闭合标签可缺失**：
	// 实测模型会把 XML 开标签与 JSON 混写，并出现两种畸形：
	//   ① args 内是 bash 命令，含 <# 与 <<'EOF' 等字符（`[^<]+` 会直接断掉）
	//   ② 只写开标签不写 </tool>（JSON 完整但未闭合，甚至尾部串出 </system>）
	// 因此这里改为**逐候选提取 + JSON 完备性校验**：从头找 `<tool name="x">`，
	// 用 json.Decoder 吃下第一个完整 JSON 对象，成功即成调用（无需 </tool>）。
	reOpen := regexp.MustCompile(`<tool\s+name="([^"]+)">`)
	if locs := reOpen.FindAllStringSubmatchIndex(s, -1); len(locs) > 0 {
		var reqs []callRequest
		for _, loc := range locs {
			name := s[loc[2]:loc[3]]
			body := s[loc[1]:] // 开标签之后的内容
			// 先试 JSON（多数情况 args 就是 JSON 体）；失败则交由模式2 处理 arg_key 形态
			dec := json.NewDecoder(strings.NewReader(body))
			var obj map[string]any
			if err := dec.Decode(&obj); err != nil {
				continue
			}
			args := strings.TrimSpace(body[:dec.InputOffset()])
			if args == "" {
				continue
			}
			reqs = append(reqs, callRequest{Action: "tool", Tool: name, Args: args})
		}
		if len(reqs) > 0 {
			return reqs
		}
	}

	// 模式2: <tool name="x" ...><arg_key>k</arg_key><arg_value>v</arg_value>…</tool>
	// 注意 `([\s\S]*?)` 必须非贪婪：贪婪会跨过第一个 </tool> 吞并后续标签。
	reMixed := regexp.MustCompile(`<tool\s+name="([^"]+)"[^>]*>([\s\S]*?)</tool>`)
	reArg := regexp.MustCompile(`<arg_key>([^<]+)</arg_key>\s*<arg_value>([\s\S]*?)</arg_value>`)
	if ms := reMixed.FindAllStringSubmatch(s, -1); len(ms) > 0 {
		reqs := make([]callRequest, 0, len(ms))
		for _, m := range ms {
			toolName, body := m[1], m[2]
			argsMap := map[string]string{}
			for _, am := range reArg.FindAllStringSubmatch(body, -1) {
				argsMap[am[1]] = am[2]
			}
			if len(argsMap) > 0 {
				argsJSON, err := json.Marshal(argsMap)
				if err != nil {
					continue // map[string]string 不会失败；防御性跳过
				}
				reqs = append(reqs, callRequest{Action: "tool", Tool: toolName, Args: string(argsJSON)})
			} else if b := strings.TrimSpace(body); b != "" {
				reqs = append(reqs, callRequest{Action: "tool", Tool: toolName, Args: b})
			}
		}
		if len(reqs) > 0 {
			return reqs
		}
	}

	// 模式2b: 带引号的 "tool 格式（模型有时输出 "tool 而不是 <tool）
	reMixed2 := regexp.MustCompile(`"tool\s+name="([^"]+)"[^>]*>([\s\S]*?)</tool>`)
	if ms := reMixed2.FindAllStringSubmatch(s, -1); len(ms) > 0 {
		reqs := make([]callRequest, 0, len(ms))
		for _, m := range ms {
			toolName, body := m[1], m[2]
			argsMap := map[string]string{}
			for _, am := range reArg.FindAllStringSubmatch(body, -1) {
				argsMap[am[1]] = am[2]
			}
			if len(argsMap) == 0 {
				reqs = append(reqs, callRequest{Action: "tool", Tool: toolName, Args: strings.TrimSpace(body)})
				continue
			}
			argsJSON, err := json.Marshal(argsMap)
			if err != nil {
				continue
			}
			reqs = append(reqs, callRequest{Action: "tool", Tool: toolName, Args: string(argsJSON)})
		}
		if len(reqs) > 0 {
			return reqs
		}
	}

	// 模式3: 标准 reply
	reReply := regexp.MustCompile(`<reply>([\s\S]+)</reply>`)
	if m := reReply.FindStringSubmatch(s); m != nil {
		return []callRequest{{Action: "reply", Text: strings.TrimSpace(m[1])}}
	}

	return nil
}

// tryFixJSON 尝试修复常见的 JSON 格式错误。
// 策略：
//  1. 补全缺失的闭合括号
//  2. 修复内部未转义的引号（如 args 值中的 \" 应为 \\\"）
//  3. 移除多余的尾部字符
//
// 返回修复后的文本，若所有尝试均失败则返回原样。
func tryFixJSON(s string) string {
	original := s
	// 策略 1：补全缺失的闭合括号
	if !strings.HasSuffix(s, "}") && strings.HasSuffix(s, "\"") {
		if fixed := s + "}"; isValidJSON(fixed) {
			return fixed
		}
	}
	// 策略 2：花括号不平衡
	for strings.Count(s, "{") > strings.Count(s, "}") {
		s += "}"
		if isValidJSON(s) {
			return s
		}
	}
	// 策略 3：修复内部未转义引号
	// 找到 args 值的模式："args":"{...}" 内部的未转义引号
	// 简单启发式：在 {"command": 后面的引号前加转义
	if idx := strings.Index(s, "\"args\":\""); idx >= 0 {
		// 找到 args 值的起始位置
		argsStart := idx + len("\"args\":\"")
		argsEnd := strings.LastIndex(s[argsStart:], "\"")
		if argsEnd > 0 {
			argsContent := s[argsStart : argsStart+argsEnd]
			// 尝试在 argsContent 内部的引号前加转义
			fixedArgs := fixInternalQuotes(argsContent)
			if fixedArgs != argsContent {
				fixed := s[:argsStart] + fixedArgs + s[argsStart+argsEnd:]
				if isValidJSON(fixed) {
					return fixed
				}
			}
		}
	}
	// 策略 4：移除尾部多余内容（如 XML 标签、代码围栏）
	if lastBrace := strings.LastIndex(s, "}"); lastBrace > 0 {
		fixed := s[:lastBrace+1]
		if isValidJSON(fixed) {
			return fixed
		}
	}
	return original
}

// fixInternalQuotes 尝试修复 JSON 字符串值内部的未转义引号。
// 简单启发式：将非转义的 \" 替换为 \\\"（但保留已经是 \\\" 的）
func fixInternalQuotes(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			// 已经有转义，原样输出
			result.WriteByte(s[i])
			i++
			if i < len(s) {
				result.WriteByte(s[i])
				i++
			}
		} else if s[i] == '"' {
			// 未转义的引号，添加转义
			result.WriteString("\\\"")
			i++
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

func isValidJSON(s string) bool {
	var dummy map[string]any
	err := json.Unmarshal([]byte(s), &dummy)
	return err == nil
}

// ExtractActionJSON 从任意文本中提取第一个包含 action 字段的 JSON 对象（agent 控制协议）。
// 处理三类模型输出：
//  1. 纯 JSON：{"action":"tool",...} → 直接提取
//  2. 围栏包裹：```json ... ``` → 剥壳后提取
//  3. 混合文本："先解释一段话…\n\n{"action":"tool",...}" → 扫描提取控制 JSON
//     （此前这种输出会被包装成 reply 文本，tool JSON 原样显示、工具不执行）
//
// 优先级：先找 action=tool（模型意图是调工具时优先执行），再找 action=reply，
// 最后找缺 action 的原生 bare 工具调用（{"tool":...,"args":...}，模型搞丢 action 时常见）。
// 返回提取到的原始 JSON 文本。文本内嵌的转义 tool JSON（reply.text 里的 \"action\"）
// 不会被误提取——非转义的 { 开头才会进入解码尝试，转义形态解码必然失败。
// ExtractActionJSON 从任意文本中提取第一个控制指令（返回协议 JSON 文本）。
//
// 同时支持 JSON 与 XML 两种形态：JSON 路径返回原 JSON 对象文本；
// XML 路径（<tool name="x">…</tool> 或 <reply>…</reply>）**归一化为等价协议 JSON**，
// 让调用方（wrapReplyJSON / 循环）能用同一套解析逻辑处理。
//
// 为何必须处理 XML：wrapReplyJSON 原先只尝试 JSON，模型输出 XML 时会被
// 包成 reply → 循环当成最终回答，任务提前结束且 XML 标签原样显示给用户。
func ExtractActionJSON(text string) (string, bool) {
	// XML 优先：extractActionXML 只识别真正的控制标签（伪标签如 <toolbox> 会落空）
	if reqs := extractActionXML(text); len(reqs) > 0 {
		if b, err := json.Marshal(reqs[0]); err == nil {
			return string(b), true
		}
	}
	objs := ExtractAllActionJSON(text)
	if len(objs) == 0 {
		return "", false
	}
	return objs[0], true
}

// ExtractAllActionJSON 从任意文本中提取【所有】控制协议 JSON 对象（保持出现顺序）。
//
// 模型一轮可以合法地并行发出多个工具调用（OpenAI 原生 tool_calls 就是数组），
// 旧实现只取首个，其余被**静默丢弃**——模型的写操作会凭空消失。
// 这里返回全部，由调用方决定执行策略；顺序敏感（模型可能先读后写）。
//
// 阻塞排除：若文本里存在 action=tool，则不再返回 action=reply 对象
// （反之亦然）——两种意图互斥，混着执行语义不明。原三态优先级保持不变。
func ExtractAllActionJSON(text string) []string {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil
	}
	// 剥 ```json 围栏
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) == 2 {
			s = strings.TrimSpace(strings.TrimSuffix(lines[1], "```"))
		}
	}
	// 三轮扫描：第一轮只要 action=tool，第二轮接受 action=reply，第三轮接受缺 action 的 bare 工具调用。
	// 某一轮只要有命中就结束——保持与原 ExtractActionJSON 完全一致的优先级。
	for _, want := range []string{"tool", "reply", "native"} {
		var found []string
		for i := 0; i < len(s); i++ {
			if s[i] != '{' {
				continue
			}
			dec := json.NewDecoder(strings.NewReader(s[i:]))
			var obj map[string]any
			if err := dec.Decode(&obj); err != nil {
				continue // 非 JSON 起始（如 {namespace} 占位符）：跳过
			}
			act, _ := obj["action"].(string)
			switch want {
			case "tool", "reply":
				if act == "" || (want == "tool" && act != "tool") {
					continue
				}
			case "native":
				// 缺 action 的原生 bare 工具调用：{"tool":...,"args":...}
				if act != "" {
					continue // 有 action 的交由标准分支
				}
				tool, _ := obj["tool"].(string)
				_, hasArgs := obj["args"]
				if tool == "" || !hasArgs {
					continue
				}
			}
			end := i + int(dec.InputOffset())
			// 解码器可能吃掉对象后的空白，截取到 '}' 为止更精确，但多余空白不影响 Unmarshal
			raw := strings.TrimSpace(s[i:min(end, len(s))])
			found = append(found, raw)
			// 从该对象之后继续扫描（避免嵌套对象被重复提取）
			i = end - 1
		}
		if len(found) > 0 {
			return found
		}
	}
	// 严格三态扫描全部失败后，宽容兜底：文本以 { 开头、无 action，且逐字符扫描能命中
	// 畸形 bare 工具调用（args 内可能含字面换行/未闭合，弱模型对多行 bash 命令常见）。
	// 重建为合法工具调用 JSON（args 重新转义成 JSON 字符串），使畸形 bare 调用真正执行而非显示。
	if strings.HasPrefix(s, "{") && jsonValueOf(s, "action") == "" {
		tool := jsonValueOf(s, "tool")
		args := jsonValueOf(s, "args")
		if tool != "" && args != "" {
			// 形状A：{"tool":...,"args":...}
			if rebuilt, err := json.Marshal(map[string]any{"action": "tool", "tool": tool, "args": args}); err == nil {
				return []string{string(rebuilt)}
			}
		}
		// 形状B：只剩 bash 参数对象 {"command":...,"timeout":...}（弱模型丢掉外层 tool/args），
		// 把 command 及可选 timeout 重建为 bash 的 args JSON 字符串，使其真正执行。
		if cmd := jsonValueOf(s, "command"); cmd != "" {
			if jsonValueOf(s, "text") == "" && jsonValueOf(s, "reply") == "" {
				params := map[string]any{"command": cmd}
				if t := jsonValueOfNum(s, "timeout"); t != "" {
					if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
						params["timeout"] = n
					}
				}
				argsJSON, _ := json.Marshal(params)
				if rebuilt, err := json.Marshal(map[string]any{"action": "tool", "tool": "bash", "args": string(argsJSON)}); err == nil {
					return []string{string(rebuilt)}
				}
			}
		}
	}
	return nil
}

// respondText 从 respond 工具调用参数中提取回答文本。
// 标准形态：{"text": "..."}；容忍模型直接把纯文本当 args 传（无 JSON 包裹）。
func respondText(args string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		return ""
	}
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(s), &p); err == nil && p.Text != "" {
		return p.Text
	}
	// args 非标准 JSON 或 text 为空：若是合法 JSON 对象则返回空（让模型重传）；
	// 否则把 args 本身当回答文本（模型常见误传：respond("你好")）。
	if strings.HasPrefix(s, "{") {
		return ""
	}
	return s
}

// ToolExchangeMessages 将一次工具调用+结果转为与循环内部格式一致的 Message 对，
// 供调用方持久化到会话（恢复会话后 LLM 可无缝理解之前试过什么）。
// toolCall 消息（role=assistant, Kind=tool_call）+ toolResult 消息（role=user, Kind=tool_result）。
// reason 为模型在调用工具前说的一句说明文字；非空时写入 tool_call 消息的 Content 中的 reason 字段，
// 使会话恢复后 UI/LLM 能读到“为什么执行这些命令”。
func ToolExchangeMessages(tool, args, reason, out string, err error) []Message {
	// 还原与模型输出同构的工具调用 JSON（callParser 只读 action/tool/args 字段）
	cc := map[string]any{"action": "tool", "tool": tool, "args": args}
	if reason != "" {
		cc["reason"] = reason
	}
	callJSON, _ := json.Marshal(cc)
	tr, _ := json.Marshal(ToolResult{ToolName: tool, Args: args, Output: out})
	if err != nil {
		tr, _ = json.Marshal(ToolResult{ToolName: tool, Args: args, Output: out, Error: err.Error()})
	}
	// 同时写原生字段（ToolCalls/ToolCallID）：会话恢复后按 OpenAI 协议回灌，
	// 模型才能看到“真的工具调用”，而不是一串 JSON 文本（见 llm.encodeMessages）。
	callID := nextToolCallID()
	return []Message{
		{
			Role: RoleAssistant, Kind: KindToolCall, ToolName: tool, ToolArgs: args,
			Content:   string(callJSON),
			ToolCalls: []ToolCallRef{{ID: callID, Name: tool, Args: args}},
		},
		{Role: RoleUser, Kind: KindToolResult, Content: "工具结果: " + string(tr), ToolCallID: callID},
	}
}

// isTerminatingTool 判断工具是否声明了终止语义（Terminate 字段）。
// 工具不存在时返回 false（由 Call 负责报错，这里不重复处理）。
func (a *Agent) isTerminatingTool(name string) bool {
	if a.Plugins == nil || name == "" {
		return false
	}
	t, ok := a.Plugins.Get(name)
	return ok && t.Terminate
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// isContextOverflow 检测 LLM 错误是否为上下文溢出。
// 兼容 OpenAI、DeepSeek、通用网关的常见 overflow 信号。
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// OpenAI: "maximum context length" / "context_length_exceeded"
	// DeepSeek: "maximum context length" / "prompt is too long"
	// 通用网关: "too long" / "overflow" / "超出" / "上下文"
	return strings.Contains(s, "maximum context") ||
		strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "context length") ||
		strings.Contains(s, "超出上下文") ||
		strings.Contains(s, "上下文溢出")
}

// emergencyTrimSteps 递进裁剪的步长（%，每次尝试一档）。
// 递进而非累加：第 4 次尝试直接砍 60%，覆盖「窗口设错得离谱」的场景。
func emergencyTrimSteps() []int {
	return []int{10, 20, 40, 60}
}

// hugeToolResultTokens 超大工具结果的阈值：超过即优先剔除。
// 工具输出常是几十 KB 的日志/JSON，一条顶几十条聊天，按条数裁剪既低效又伤对话连贯。
const hugeToolResultTokens = 4000

// emergencyTrim 上下文溢出时的应急裁剪（自愈）。
//
// 策略（按优先级）：
//  1. 剔除超大工具结果（> hugeToolResultTokens），它们最占 token 但价值最低
//  2. 从最老的消息开始丢弃，直到达到目标削减量
//
// pct 为目标削减比例（%）：10/20/40/60 递进尝试，应对「context_window 配置
// 超过模型真实上限」而本地估算无法可靠判断的场景。
//
// 保证：[system] 与最新一条消息永远保留；tool_call/tool_result 不会因裁剪而失去配对。
func emergencyTrim(msgs []Message, pct int) []Message {
	if len(msgs) <= 2 {
		return msgs
	}
	// 拆出 system 前缀（可能是多条 system：主提示词 + 时间上下文）。
	sysEnd := 0
	for sysEnd < len(msgs) && msgs[sysEnd].Role == RoleSystem {
		sysEnd++
	}
	body := msgs[sysEnd:]
	if len(body) <= 1 {
		return msgs
	}

	// 第一步：剔除超大工具结果（保留占位，维持 tool_call/tool_result 配对）。
	out := make([]Message, 0, len(body))
	droppedHuge := false
	for _, m := range body {
		if m.Kind == KindToolResult && EstimateTokens(m) > hugeToolResultTokens {
			placeholder := m
			placeholder.Content = "[工具结果过大，已因上下文超限被省略]"
			out = append(out, placeholder)
			droppedHuge = true
			continue
		}
		out = append(out, m)
	}
	if pct <= 0 {
		res := append([]Message{}, msgs[:sysEnd]...)
		return append(res, out...)
	}

	// 第二步：从最老开始丢弃整条消息，直到达到目标削减量。
	// 以「估算 token 总量」为基准，而非消息条数（长消息应多减）。
	total := 0
	for _, m := range out {
		total += EstimateTokens(m)
	}
	target := total * pct / 100
	dropped := 0
	i := 0
	// 保留最后一条（当前任务/最新消息）
	for i < len(out)-1 && dropped < target {
		dropped += EstimateTokens(out[i])
		i++
	}
	keep := out[i:]
	if len(keep) == 0 && len(out) > 0 {
		keep = out[len(out)-1:]
	}
	_ = droppedHuge // 占位剔除已生效，记录供调用方日志

	res := make([]Message, 0, sysEnd+len(keep))
	res = append(res, msgs[:sysEnd]...)
	res = append(res, keep...)
	return res
}

// EmergencyChanged 判断应急裁剪是否真的改变了消息（用于避免空转重试）。
func EmergencyChanged(before, after []Message) bool {
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		if before[i].Content != after[i].Content || before[i].Kind != after[i].Kind {
			return true
		}
	}
	return false
}

// hardTrimOnOverflow 上下文溢出的最后兜底：强制压缩失败时，按 token 预算从
// 头部保留消息（丢弃尾部最早之外的内容），保留 system + 最近若干条，保证请求瘦身。
// 保留结构：[system] + [最近消息...]，从尾部累计到 budget。至少保留 system + 最后一条。
func hardTrimOnOverflow(msgs []Message, budget int) []Message {
	if len(msgs) <= 1 {
		return msgs
	}
	var sys *Message
	body := msgs
	if msgs[0].Role == RoleSystem {
		s := msgs[0]
		sys = &s
		body = msgs[1:]
	}
	// 从尾部累计保留
	var keep []Message
	acc := 0
	for i := len(body) - 1; i >= 0; i-- {
		t := EstimateTokens(body[i])
		if acc+t > budget && len(keep) > 0 {
			break
		}
		keep = append([]Message{body[i]}, keep...)
		acc += t
	}
	if len(keep) == 0 && len(body) > 0 {
		keep = body[len(body)-1:]
	}
	var out []Message
	if sys != nil {
		out = append(out, *sys)
	}
	out = append(out, keep...)
	return out
}

// isNonRetryableError 判断 LLM 错误是否不可重试（确定性失败）。
// 不可重试的错误直接返回，不消耗 WeakModelTuner 的重试预算。
// 429 rate limit 带有 retry-after 提示的属于可重试（指数退避已处理）。
func isNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// 模型不存在 / 认证失败 / 配额耗尽 → 立即失败，重试无意义
	return strings.Contains(s, "model not found") ||
		strings.Contains(s, "invalid_model") ||
		strings.Contains(s, "authentication") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "forbidden") ||
		strings.Contains(s, "quota") ||
		strings.Contains(s, "insufficient_quota") ||
		strings.Contains(s, "billing")
}
