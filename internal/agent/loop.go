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

	"mizar/internal/plugins"
)

// LLM 抽象：任何 OpenAI 兼容客户端或测试 mock 都可实现。
type LLM interface {
	// Chat 发送消息列表，返回模型回复文本。
	Chat(messages []Message) (string, error)
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

// Agent 是主循环。
type Agent struct {
	LLM        LLM
	Plugins    *plugins.Manager
	System     string
	Initial    []Message // 会话恢复时的历史消息（置于 task 之前）
	MaxSteps   int       // 最大循环步数（默认 20）
	VerboseLog func(string) // 可选日志回调
	Compactor  *Compactor   // 会话压缩器（nil = 不压缩）
	Hooks      *Hooks       // 挂载点（nil = 无钩子）

	// 工具调用解析策略
	callParser func(text string) (*callRequest, error)

	// systemPromptCache 缓存 SystemPrompt() 结果。
	// 缓存友好关键：system + 工具列表必须是字节级稳定前缀，
	// 任何一次调用都返回完全相同的文本，否则整个前缀缓存全部失效。
	systemPromptCache string

	// 快速插入（steer/abort，见 steer.go）
	steerMu  sync.Mutex // 保护 steer 槽位
	steer    *steerMsg  // 待插入消息（nil = 无）
	steerSeq uint64     // 序号，最新覆盖用
	aborted  atomic.Bool

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
		MaxSteps:   20,
		callParser: parseCallJSON,
		Commands:   NewCommandRegistry(),
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
func (a *Agent) SystemPrompt() string {
	if a.systemPromptCache != "" {
		return a.systemPromptCache
	}
	tools := a.Plugins.Tools()
	var sb strings.Builder
	sb.WriteString(a.System)
	sb.WriteString("\n\n## 可用工具\n")
	if len(tools) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}
	sb.WriteString(`
## 调用格式
当需要调用工具时，回复如下 JSON（不要有其他内容）：
{"action":"tool","tool":"工具名","args":"参数字符串"}
任务完成时，回复：
{"action":"reply","text":"最终回答"}
`)
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
		a.MaxSteps = 20
	}
	if a.Hooks == nil {
		a.Hooks = NewHooks()
	}
	a.resetAbort()
	// RunStart 挂载点（🟢 安全：此时尚未构建 LLM 消息）
	runCtx := &HookContext{RunID: fmt.Sprintf("run-%d", time.Now().UnixNano()), Task: task, AgentName: "mizar"}
	a.Hooks.fireRunStart(runCtx, a.logf)
	msgs := []Message{
		{Role: RoleSystem, Content: a.SystemPrompt()},
	}
	msgs = append(msgs, a.Initial...)
	msgs = append(msgs, Message{Role: RoleUser, Content: task})
	for step := 0; step < a.MaxSteps; step++ {
		stepCtx := &HookContext{RunID: runCtx.RunID, Step: step, Task: task, Messages: msgs}
		// StepStart 挂载点（🟡 中等：可读，不建议改 Messages）
		a.Hooks.fireStepStart(stepCtx, a.logf)

		// 快速插入检查：用户/外部发送的纠正消息在 LLM 调用前注入。
		// 优先级高于压缩——用户纠正优先被模型看到。
		if sm := a.drainSteer(); sm != nil {
			a.log("step %d: 快速插入纠正: %s", step, truncate(sm.content, 120))
			msgs = append(msgs, Message{Role: RoleUser, Content: "【用户快速纠正】" + sm.content})
		}
		// abort 检查：被请求停止则不再调用 LLM。
		if a.Aborted() {
			a.Hooks.fireError(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Err: errors.New("aborted by user")}, a.logf)
			return "", ErrAborted
		}

		// 压缩检查：在每次 LLM 调用前，若超限则先压缩。
		// 缓存友好：压缩是低频操作（只在超限时发生），压缩后摘要作为
		// 稳定前缀插入 system 之后，后续步骤重新建立可缓存前缀。
		if a.Compactor != nil {
			est := EstimateMessages(msgs)
			if a.Compactor.ShouldCompact(est) {
				a.log("step %d: 触发压缩 (est=%d tokens > window=%d-reserve=%d)", step, est, a.Compactor.ContextWindow, a.Compactor.ReserveTokens)
				// CompactionBefore 挂载点（🟢 安全）
				a.Hooks.fireCompactionBefore(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, EstTokens: est, Messages: msgs}, a.logf)
				var err error
				msgs, err = a.Compactor.Compact(msgs)
				if err != nil {
					a.log("step %d: 压缩失败（继续未压缩执行）: %v", step, err)
				} else {
					a.log("step %d: 压缩完成 -> %d 条消息 (est=%d tokens)", step, len(msgs), EstimateMessages(msgs))
				}
				// CompactionAfter 挂载点（🟢 安全：摘要已固定）
				a.Hooks.fireCompactionAfter(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Compacted: true, EstTokens: est, Summary: summaryOf(msgs), Messages: msgs}, a.logf)
			}
		}

		// LLMRequest 挂载点（🔴 严重：修改 Messages 会使缓存前缀失效）
		llmCtx := &HookContext{RunID: runCtx.RunID, Step: step, Task: task, Messages: msgs}
		a.Hooks.fireLLMRequest(llmCtx, a.logf)
		msgs = llmCtx.Messages

		reply, err := a.LLM.Chat(msgs)
		if err != nil {
			a.Hooks.fireError(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Err: err}, a.logf)
			return "", fmt.Errorf("llm chat: %w", err)
		}
		// LLMResponse 挂载点（🟢 安全）
		a.Hooks.fireLLMResponse(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Reply: reply}, a.logf)
		a.log("step %d: model: %s", step, truncate(reply, 200))
		req, err := a.callParser(reply)
		if err != nil {
			// 解析失败：把错误喂回模型让它修复
			a.Hooks.fireError(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Err: fmt.Errorf("parse: %w", err)}, a.logf)
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
			msgs = append(msgs, Message{Role: RoleUser, Content: "解析你的回复失败：" + err.Error() + "。请严格按格式输出 JSON。"})
			a.Hooks.fireStepEnd(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Messages: msgs}, a.logf)
			continue
		}
		if req.Action == "reply" {
			// StepEnd + RunEnd 挂载点（🟢 安全）
			a.Hooks.fireStepEnd(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Reply: req.Text, Messages: msgs}, a.logf)
			a.Hooks.fireRunEnd(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Reply: req.Text}, a.logf)
			return req.Text, nil
		}
		// 工具调用
		// ToolCall 挂载点（🟢 安全）
		a.Hooks.fireToolCall(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Tool: req.Tool, Args: req.Args}, a.logf)
		out, err := a.Plugins.Call(req.Tool, req.Args)
		tr := ToolResult{ToolName: req.Tool, Args: req.Args, Output: out}
		if err != nil {
			tr.Error = err.Error()
			a.Hooks.fireError(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Tool: req.Tool, Args: req.Args, Err: err}, a.logf)
		}
		b, _ := json.Marshal(tr)
		msgs = append(msgs, Message{Role: RoleAssistant, Content: reply, Kind: KindToolCall})
		// 注意：工具结果以普通文本喂回（非 OpenAI 原生 tool role），
		// 因为本循环使用文本 JSON 调用协议，不依赖 tool_call_id 配对。
		// Kind 标记仅用于压缩切点识别（tool_result 不可作切点）。
		msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b), Kind: KindToolResult})
		// ToolResult 挂载点（🟡 中等：结果已进入消息流）
		a.Hooks.fireToolResult(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Tool: req.Tool, Args: req.Args, Result: string(b), Err: err, Messages: msgs}, a.logf)
		a.Hooks.fireStepEnd(&HookContext{RunID: runCtx.RunID, Step: step, Task: task, Messages: msgs}, a.logf)
	}
	a.Hooks.fireError(&HookContext{RunID: runCtx.RunID, Task: task, Err: errors.New("max steps exceeded")}, a.logf)
	return "", errors.New("max steps exceeded")
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
	// 去掉围栏
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) == 2 {
			s = strings.TrimSuffix(lines[1], "```")
		}
	}
	// 找第一个 { 和最后一个 }
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
