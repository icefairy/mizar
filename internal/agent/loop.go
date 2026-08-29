// Package agent 实现 Agent 主循环：规划 → 工具调用 → 结果反馈 → 修复。
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mizar/internal/engine"
	"mizar/internal/jobs"
	"mizar/internal/lifecycle"
	"mizar/internal/plugins"
)

// CacheStats 系统提示词缓存命中率统计。
// 由 Agent 维护，启动/重启时打印摘要。
type CacheStats struct {
	mu      sync.Mutex
	hits    int // SystemPrompt() 命中缓存的调用次数
	misses  int // 缓存失效后重建的次数
	total   int // hits + misses
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
	LLM        LLM
	Plugins    *plugins.Manager
	System     string
	PluginDir  string          // 插件目录（如 ~/.mizar/extensions），用于系统提示引导模型自行创建插件
	Initial    []Message       // 会话恢复时的历史消息（置于 task 之前）
	MaxSteps   int             // 最大循环步数（默认 maxStepsDefault=60）
	VerboseLog func(string)    // 可选日志回调
	Compactor  *Compactor      // 会话压缩器（nil = 不压缩）
	Hooks      *Hooks          // 挂载点（nil = 无钩子）
	Tuner      *WeakModelTuner // 弱模型宽容策略（nil = 不启用）
	Guard      *RepeatGuard    // 循环卫生守卫：重复调用渐进提醒，超阈值终止（nil = 不启用；复刻 dsh repeat-tool-reminder）
	Jobs       *jobs.Registry  // 后台任务注册表（nil = 无后台任务；bash run_in_background + job_* 工具）
	PlanMode   *PlanMode       // 计划模式控制器（nil = 不启用；复刻 dsh plan-mode）
	GoalService *GoalService   // 会话目标服务（nil = 不启用；复刻 dsh goal）
	GoalLoop   *GoalLoop       // Goal 自动 judge 循环（nil = 不启用；复刻 hermes Ralph loop）
	SubAgents  *SubagentManager // 子代理委派管理器（nil = 不启用；复刻 hermes delegate_tool）
	BgReview   *BackgroundReview // 后台自学习 review（nil = 不启用；复刻 hermes background_review）
	StatsData  *StatsDataRef // 会话统计（nil = 不启用）

	// AuxLLM 辅助模型（judge / background review 用；nil = 使用主 LLM）
	AuxLLM AuxiliaryLLM

	// AskUser 可选：模型通过 ask_user_question 工具向人类提问时的回调。
	// 参数为 questions JSON，返回 answers JSON；TUI 模式弹出输入等待用户，无界面环境返回降级提示。
	AskUser func(questionsJSON string) (string, error)

	// 工具调用解析策略
	callParser func(text string) (*callRequest, error)

	// systemPromptCache 缓存 SystemPrompt() 结果。
	// 缓存友好关键：system + 工具列表必须是字节级稳定前缀，
	// 任何一次调用都返回完全相同的文本，否则整个前缀缓存全部失效。
	systemPromptCache string

	// cacheStats 系统提示词缓存命中率统计
	cacheStats *CacheStats

	// 快速插入（steer/abort，见 steer.go）
	steerMu  sync.Mutex // 保护 steer 槽位
	steer    *steerMsg  // 待插入消息（nil = 无）
	steerSeq uint64     // 序号，最新覆盖用
	aborted  atomic.Bool

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

// SystemPrompt 构建系统提示（含工具列表）。结果被缓存——工具列表在运行期
// 不可变（插件热加载通过 ReloadTools 显式失效），保证前缀字节级稳定。
	const defaultSystemPrompt = `你是开阳(Mizar) Agent，一个自举的编码智能体。你通过调用工具帮助用户完成任务。

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

## 会话上下文
- 当前工作目录由用户所在目录决定，不确定时用 pwd 确认。
- 项目可能有 AGENTS.md 或 .mizar 上下文文件，相关时先读取。
- 技能（SKILL.md）通过 index 模式注入系统提示，可用 skill_manage 管理。
`

// HostPlugins 定位插件目录（如 ~/.mizar/extensions）。
// 由系统提示词使用，让模型知道在哪里创建/查找插件。可留空以省略该段。

// SystemPrompt 构建系统提示（含工具列表）。结果被缓存——工具列表在运行期
// 不可变（插件热加载通过 ReloadTools 显式失效），保证前缀字节级稳定。
func (a *Agent) SystemPrompt() string {
	if a.systemPromptCache != "" {
		a.cacheStats.RecordHit()
		return a.systemPromptCache
	}
	a.cacheStats.RecordMiss()
	tools := a.Plugins.Tools()
	var sb strings.Builder
	sys := a.System
	if sys == "" {
		sys = defaultSystemPrompt
	}
	sb.WriteString(sys)
	sb.WriteString("\n")
	// 计划模式引导（活跃时注入，退出时为空字符串，不影响缓存键稳定）
	if section := a.PlanMode.SystemSection(); section != "" {
		sb.WriteString(section)
	}
	if len(tools) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}
	sb.WriteString(`
## 回复格式
你的每轮回复必须是以下两种 JSON 之一，不要输出其他文字：
- 调用工具：{"action":"tool","tool":"工具名","args":"参数JSON字符串"}
- 回答用户或任务完成：{"action":"reply","text":"回答内容"}

示例：
用户: 你好
你: {"action":"reply","text":"你好！有什么可以帮你的？"}

用户: 列出当前目录
你: {"action":"tool","tool":"ls","args":"{}"}
`)

	// 插件目录信息（引导模型自己创建/扩展插件）
	if a.PluginDir != "" {
		pluginNames := a.Plugins.PluginNames()
		sb.WriteString(fmt.Sprintf("\n## 插件\n插件目录: %s\n", a.PluginDir))
		if len(pluginNames) > 0 {
			sb.WriteString("已加载插件：\n")
			for _, p := range pluginNames {
				// 统计每个插件导出了多少个工具
				sb.WriteString(fmt.Sprintf("- %s\n", p))
			}
		}
		sb.WriteString("\n如需扩展能力，在插件目录新建 .ts 文件，导出 tool_* 函数（工具）或 command_* 函数（斜杠命令），" +
			"然后调用 /reload 加载；也可直接写文件后用 host 函数验证。支持 host_listen 宿主函数启动 HTTP 服务器。\n")
		// 宿主函数清单：插件 TS/JS 内可直接调用这些 API（信息来自 internal/engine 权威文档）
		sb.WriteString("\n插件中可用的宿主函数（插件代码内可直接调用，无需 import）：\n")
		sb.WriteString(engine.HostDocBriefs())
		// 最小可运行示例：让模型照着写而不用去逆向源码/二进制
		sb.WriteString("\n最小插件示例（存入插件目录后 /reload 生效）：\n")
		sb.WriteString(
			"```ts\n" +
				"// hello.ts — 导出 tool_* 即注册为工具，参数是 JSON 字符串\n" +
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
	a.systemPromptCache = sb.String()
	return a.systemPromptCache
}

// ReloadTools 插件热加载后调用：使 SystemPrompt 缓存失效。
// 注意：缓存友好约束下，热加载会破坏前缀缓存一次——这是显式 trade-off，
// 调用方应仅在"确实新增工具"时使用，而非每次轮询都调用。
func (a *Agent) ReloadTools() {
	a.systemPromptCache = ""
}

// Run 执行任务。返回最终回复。
func (a *Agent) Run(task string) (string, error) {
	if a.MaxSteps <= 0 {
		a.MaxSteps = maxStepsDefault
	}
	if a.Hooks == nil {
		a.Hooks = NewHooks()
	}
	a.resetAbort()
	if a.Tuner != nil {
		a.Tuner.Reset()
	}
	if a.Guard != nil {
		a.Guard.Reset()
	}
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
	msgs = append(msgs, a.Initial...)
	msgs = append(msgs, Message{Role: RoleUser, Content: task})
	// 标记人类直接消息（用于 goal create/edit/pause/resume 权限校验）
	if a.GoalService != nil {
		a.GoalService.MarkHumanTurn()
	}

	step := 0
	stepWarned := false // 步数预算预警只提醒一次
	var qc lifecycle.QueryContext
	for step < a.MaxSteps {
		q.BeginStep()
		qc = q.Context()
		stepCtx := &HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}
		// StepStart 挂载点（🟡 中等：可读，不建议改 Messages）
		a.Hooks.fireStepStart(stepCtx, a.logf)

		// 步数预算预警：剩余步数不足时提醒模型收敛（只注入一次，避免刷屏）
		if remain := a.MaxSteps - step; remain <= maxStepsWarnRemain && !stepWarned {
			stepWarned = true
			msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf(
				"【系统提醒】剩余步骤仅 %d 步（当前上限 %d）。若任务已基本完成，请立即用 reply 输出最终回答；"+
					"若仍需操作，请合并为一次工具调用（如一次 bash 完成多项检查/修改）快速收尾", remain, a.MaxSteps)})
			a.log("%s", q.FormatLog("step_budget_warn", "remain=", fmt.Sprintf("%d", remain)))
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
				var err error
				msgs, err = a.Compactor.Compact(msgs)
				if err != nil {
					a.log("%s", q.FormatLog("compact_fail", "err=", err.Error()))
				} else {
					a.log("%s", q.FormatLog("compact", "result=", fmt.Sprintf("%d msgs, %d tokens", len(msgs), EstimateMessages(msgs))))
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
			emit := func(d StreamDelta) { onStream(qc.Step, d) }
			if toolLLM, ok := a.LLM.(StreamToolCallLLM); ok {
				reply, llmErr = toolLLM.ChatWithToolsStream(msgs, a.Plugins.Tools(), emit)
			} else if sllm, ok := a.LLM.(StreamLLM); ok {
				reply, llmErr = sllm.ChatStream(msgs, emit)
			} else if toolLLM, ok := a.LLM.(ToolCallLLM); ok {
				// LLM 不支持流式但支持原生工具：回退一次性调用
				reply, llmErr = toolLLM.ChatWithTools(msgs, a.Plugins.Tools())
			} else {
				reply, llmErr = a.LLM.Chat(msgs)
			}
		} else {
			// 非流式路径（原逻辑）
			if toolLLM, ok := a.LLM.(ToolCallLLM); ok {
				// 原生工具调用：发送 tools 参数，模型结构化返回
				reply, llmErr = toolLLM.ChatWithTools(msgs, a.Plugins.Tools())
			} else {
				reply, llmErr = a.LLM.Chat(msgs)
			}
		}
		q.EndOperation("llm")
		if llmErr != nil {
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

		if req.Action == "reply" {
			q.Complete(nil)
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: req.Text, Messages: msgs}, a.logf)
			a.Hooks.fireRunEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Reply: req.Text}, a.logf)
			return req.Text, nil
		}

		// 工具调用
		q.BeginOperation("tool:" + req.Tool)
		a.Hooks.fireToolCall(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: req.Tool, Args: req.Args}, a.logf)
		// 空参数兑底：不执行工具，把错误发回模型让它修正（防止"args {command} required"空转）
		trimmedArgs := strings.TrimSpace(req.Args)
		if trimmedArgs == "" || trimmedArgs == "{}" {
			q.EndOperation("tool:" + req.Tool)
			msg := fmt.Sprintf("⚠️ 工具 %s 调用缺少参数（args 为空）。请重新调用，并在 args 中传入正确的 JSON 参数。", req.Tool)
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply, Kind: KindToolCall})
			msgs = append(msgs, Message{Role: RoleUser, Content: msg})
			a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
			step++
			continue
		}
		// 先执行工具（dsh 的 repeat-tool-reminder 也是 post-execute 观察语义）
		out, err := a.Plugins.Call(req.Tool, req.Args)
		q.EndOperation("tool:" + req.Tool)
		tr := ToolResult{ToolName: req.Tool, Args: req.Args, Output: out}
		if err != nil {
			tr.Error = err.Error()
			a.Hooks.fireError(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: req.Tool, Args: req.Args, Err: err}, a.logf)
		}
		b, _ := json.Marshal(tr)

		// Guard 观察：记录此次调用后触发渐进提醒或终止（dsh semantics：observe-and-enrich, never veto, kill on max threshold exceed）
		if a.Guard != nil {
			if reminder, terminate := a.Guard.Observe(req.Tool, req.Args); terminate {
				msgs = append(msgs, Message{Role: RoleAssistant, Content: reply, Kind: KindToolCall})
				msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b)})
				msgs = append(msgs, Message{Role: RoleUser, Content: reminder + "\n⚠️ 任务因重复工具调用已终止。"})
				a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
				return "", fmt.Errorf("%w: tool=%s 连续重复 %d 次，任务终止", errors.New("loop guard terminated"), req.Tool, a.Guard.MaxObserved())
			} else if reminder != "" {
				// 渐进提醒，注入为追加用户消息（紧接工具结果之后，模型可据此改参数或停手）
				msgs = append(msgs, Message{Role: RoleAssistant, Content: reply, Kind: KindToolCall})
				msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b)})
				msgs = append(msgs, Message{Role: RoleUser, Content: reminder})
				a.log("%s", q.FormatLog("guard_reminder", "step=", fmt.Sprintf("%d", qc.Step), "tool=", req.Tool, "cnt=", fmt.Sprintf("%d", a.Guard.Count())))
				a.Hooks.fireStepEnd(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Messages: msgs}, a.logf)
				step++
				continue
			}
		}
		msgs = append(msgs, Message{Role: RoleAssistant, Content: reply, Kind: KindToolCall})
		msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b), Kind: KindToolResult})
		a.Hooks.fireToolResult(&HookContext{RunID: string(qc.QueryID), Step: qc.Step, Task: task, Tool: req.Tool, Args: req.Args, Result: string(b), Err: err, Messages: msgs}, a.logf)

		// GoalLoop judge：每轮后判断目标是否完成，自动续跑（🟡 ImpactModerate）
		if a.GoalLoop != nil {
			shouldContinue, continuationPrompt, judgeErr := a.GoalLoop.EvaluateAfterTurn(reply)
			if judgeErr != nil {
				a.log("%s", q.FormatLog("goal_judge_error", "err=", judgeErr.Error()))
			} else if shouldContinue && continuationPrompt != "" {
				// 追加 continuation prompt，继续下一轮
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
func (a *Agent) logf(msg string) {
	a.log("%s", msg)
}

func (a *Agent) log(format string, args ...any) {
	if a.VerboseLog != nil {
		a.VerboseLog(fmt.Sprintf(format, args...))
	}
}

// parseCallJSON 从模型回复中提取 JSON 调用。容忍 ```json 围栏与前后缀文本。
func parseCallJSON(text string) (*callRequest, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) == 2 {
			s = strings.TrimSuffix(lines[1], "```")
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in reply")
	}
	var req callRequest
	if err := json.Unmarshal([]byte(s[start:end+1]), &req); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if req.Action == "" {
		return nil, fmt.Errorf("missing action field")
	}
	return &req, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
