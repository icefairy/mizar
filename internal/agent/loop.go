// Package agent 实现 Agent 主循环：规划 → 工具调用 → 结果反馈 → 修复。
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mizar/internal/plugins"
)

// LLM 抽象：任何 OpenAI 兼容客户端或测试 mock 都可实现。
type LLM interface {
	// Chat 发送消息列表，返回模型回复文本。
	Chat(messages []Message) (string, error)
}

// Message 对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Role 常量
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

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
	MaxSteps   int   // 最大循环步数（默认 20）
	VerboseLog func(string) // 可选日志回调

	// 工具调用解析策略
	callParser func(text string) (*callRequest, error)
}

type callRequest struct {
	Action string `json:"action"` // "tool" | "reply"
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Text   string `json:"text,omitempty"`
}

// New 创建 Agent。
func New(llm LLM, pm *plugins.Manager) *Agent {
	return &Agent{
		LLM:      llm,
		Plugins:  pm,
		MaxSteps: 20,
		callParser: parseCallJSON,
	}
}

// SystemPrompt 构建系统提示（含工具列表）。
func (a *Agent) SystemPrompt() string {
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
	return sb.String()
}

// Run 执行任务。返回最终回复。
func (a *Agent) Run(task string) (string, error) {
	if a.MaxSteps <= 0 {
		a.MaxSteps = 20
	}
	msgs := []Message{
		{Role: RoleSystem, Content: a.SystemPrompt()},
		{Role: RoleUser, Content: task},
	}
	for step := 0; step < a.MaxSteps; step++ {
		reply, err := a.LLM.Chat(msgs)
		if err != nil {
			return "", fmt.Errorf("llm chat: %w", err)
		}
		a.log("step %d: model: %s", step, truncate(reply, 200))
		req, err := a.callParser(reply)
		if err != nil {
			// 解析失败：把错误喂回模型让它修复
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
			msgs = append(msgs, Message{Role: RoleUser, Content: "解析你的回复失败：" + err.Error() + "。请严格按格式输出 JSON。"})
			continue
		}
		if req.Action == "reply" {
			return req.Text, nil
		}
		// 工具调用
		out, err := a.Plugins.Call(req.Tool, req.Args)
		tr := ToolResult{ToolName: req.Tool, Args: req.Args, Output: out}
		if err != nil {
			tr.Error = err.Error()
		}
		b, _ := json.Marshal(tr)
		msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
		// 注意：工具结果以普通文本喂回（非 OpenAI 原生 tool role），
		// 因为本循环使用文本 JSON 调用协议，不依赖 tool_call_id 配对。
		msgs = append(msgs, Message{Role: RoleUser, Content: "工具结果: " + string(b)})
	}
	return "", errors.New("max steps exceeded")
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
