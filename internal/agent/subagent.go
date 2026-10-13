// Package agent 实现子代理委派（复刻 hermes delegate_tool + subagent_lifecycle）。
package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mizar/internal/plugins"
)

// ============================================================================
// SubagentState
// ============================================================================

type SubagentState string

const (
	SubagentPending   SubagentState = "PENDING"
	SubagentRunning   SubagentState = "RUNNING"
	SubagentSucceeded SubagentState = "SUCCEEDED"
	SubagentFailed    SubagentState = "FAILED"
	SubagentCancelled SubagentState = "CANCELLED"
)

// ============================================================================
// SubagentLaunchRequest
// ============================================================================

type SubagentLaunchRequest struct {
	Goal         string   `json:"goal"`
	Context      string   `json:"context,omitempty"`
	Role         string   `json:"role"`
	Model        string   `json:"model,omitempty"`
	BlockedTools []string `json:"blocked_tools,omitempty"`
	WorkingDir   string   `json:"working_dir,omitempty"`
	TimeoutSecs  int      `json:"timeout_seconds,omitempty"`
	MaxSteps     int      `json:"max_steps,omitempty"`
}

// ============================================================================
// SubagentHandle
// ============================================================================

type SubagentHandle struct {
	ID        string        `json:"id"`
	Goal      string        `json:"goal"`
	State     SubagentState `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
	Depth     int           `json:"depth"`
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	StepsUsed int           `json:"steps_used,omitempty"`
}

// ============================================================================
// SubagentManager
// ============================================================================

type SubagentManager struct {
	mu           sync.RWMutex
	subs         map[string]*SubagentHandle
	nextID       atomic.Int64
	maxDepth     int
	maxSteps     int
	parentLLM    ToolCallLLM
	parentSystem string
}

func NewSubagentManager(parentLLM ToolCallLLM, parentSystem string) *SubagentManager {
	return &SubagentManager{
		subs:         make(map[string]*SubagentHandle),
		maxDepth:     3,
		maxSteps:     50,
		parentLLM:    parentLLM,
		parentSystem: parentSystem,
	}
}

func (sm *SubagentManager) Launch(req SubagentLaunchRequest) (*SubagentHandle, error) {
	if req.Goal == "" {
		return nil, fmt.Errorf("subagent: goal required")
	}
	if len(req.BlockedTools) == 0 {
		req.BlockedTools = []string{"delegate_task"}
	}
	if req.Role == "" {
		req.Role = "leaf"
	}
	if req.MaxSteps <= 0 {
		req.MaxSteps = sm.maxSteps
	}

	handle := &SubagentHandle{
		ID:        fmt.Sprintf("sub-%d", sm.nextID.Add(1)),
		Goal:      req.Goal,
		State:     SubagentPending,
		CreatedAt: time.Now(),
		Depth:     1,
	}
	go sm.runSubagent(handle, req)
	return handle, nil
}

func (sm *SubagentManager) runSubagent(handle *SubagentHandle, req SubagentLaunchRequest) {
	handle.State = SubagentRunning
	defer func() {
		if handle.State == SubagentPending || handle.State == SubagentRunning {
			if handle.Error != "" {
				handle.State = SubagentFailed
			} else {
				handle.State = SubagentSucceeded
			}
		}
	}()

	childSystem := fmt.Sprintf("%s\n\nYou are a delegated subagent working on: %s\n\n%s",
		sm.parentSystem, req.Goal, req.Context)

	msgs := []Message{
		{Role: RoleSystem, Content: childSystem},
		{Role: RoleUser, Content: req.Goal},
	}
	// lastText 记录最近一轮模型给出的文字（含 reply.text / 开场说明），
	// 供“跑满步数 / LLM 出错”时作为兑底结果，避免产出全丢。
	lastText := ""

	for step := 0; step < req.MaxSteps; step++ {
		reply, err := sm.parentLLM.ChatWithTools(msgs, nil)
		if err != nil {
			handle.Error = err.Error()
			handle.Result = lastText // 出错也要交出此前轮次的产出
			return
		}
		handle.StepsUsed = step + 1

		var parsed struct {
			Action string `json:"action"`
			Text   string `json:"text,omitempty"`
		}
		if err := json.Unmarshal([]byte(reply), &parsed); err != nil || parsed.Action != "reply" {
			// 子 agent 本轮没给最终回答（工具调用轮 / 畸形 JSON / 多调用轮 / 纯文字）：
			// 记录本轮文本作为**候选结果**，再要求它直接回结果。
			// 关键：若它最终跑满步数或 LLM 出错，候选结果会在收尾处兑底成为 handle.Result——
			// 旧实现只在成功分支赋值 handle.Result，导致所有轮次的产出全部丢失，
			// 调用方只能拿到“step limit exceeded”而拿不到任何内容。
			if t := extractAssistantText(reply); t != "" {
				lastText = t
			}
			msgs = append(msgs, Message{Role: RoleAssistant, Content: reply})
			msgs = append(msgs, Message{Role: RoleUser, Content: "Subagent mode: please reply directly with your result."})
			continue
		}
		handle.Result = parsed.Text
		return
	}
	handle.Error = "subagent: step limit exceeded"
	handle.Result = lastText // 步数耗尽：交回最后一轮产出，而不是空结果
}

// extractAssistantText 从模型一轮输出里取出“可当结果用”的文字。
//   - {"action":"reply","text":"..."} → text
//   - 混合文本（先说话再调工具）      → 去掉控制 JSON 后的说明文字
//   - 其它畸形/空                     → ""
//
// 抽为独立函数是为了让 subagent 的兑底逻辑可测，且不依赖 agent 包内的流式提取器。
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
	// 非 JSON（或畸形）：若不像工具控制文本，就当作模型直说的话
	if IsToolCallText(reply) {
		return ""
	}
	return strings.TrimSpace(reply)
}

func (sm *SubagentManager) Get(id string) (*SubagentHandle, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	h, ok := sm.subs[id]
	return h, ok
}

func (sm *SubagentManager) List() []*SubagentHandle {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]*SubagentHandle, 0, len(sm.subs))
	for _, h := range sm.subs {
		cp := *h
		out = append(out, &cp)
	}
	return out
}

func (sm *SubagentManager) Cancel(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	h, ok := sm.subs[id]
	if !ok {
		return fmt.Errorf("subagent: %q not found", id)
	}
	h.State = SubagentCancelled
	return nil
}

func (sm *SubagentManager) RenderResults() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.subs) == 0 {
		return "（无活跃子代理）"
	}
	var sb string
	for _, h := range sm.subs {
		sb += fmt.Sprintf("  [%s] %s → %s", h.ID, h.State, truncate(h.Goal, 60))
		if h.Result != "" {
			sb += fmt.Sprintf("\n       结果：%s", truncate(h.Result, 100))
		}
		if h.Error != "" {
			sb += fmt.Sprintf("\n       错误：%s", h.Error)
		}
		sb += "\n"
	}
	return sb
}

// ============================================================================
// Tool factories
// ============================================================================

func DelegateTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "delegate_task",
		Description: "Spawn a subagent to work on a subtask independently. Args: {goal, context?, role?, blocked_tools?, working_dir?, max_steps?}. Returns {id, state}.",
		Run: func(args string) (string, error) {
			var req SubagentLaunchRequest
			if err := json.Unmarshal([]byte(args), &req); err != nil || req.Goal == "" {
				return `{"error": "goal required"}`, fmt.Errorf("delegate_task: {goal} required")
			}
			handle, err := sm.Launch(req)
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error()), err
			}
			b, _ := json.Marshal(handle)
			return string(b), nil
		},
	}
}

func GetSubagentTool(sm *SubagentManager) plugins.Tool {
	return plugins.Tool{
		Name:        "get_subagent",
		Description: "Check status and result of a delegated subagent. Args: {id}.",
		Run: func(args string) (string, error) {
			var p struct{ ID string `json:"id"` }
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
		Description: "List all delegated subagents and their current status.",
		Run: func(args string) (string, error) {
			_ = args
			return sm.RenderResults(), nil
		},
	}
}
