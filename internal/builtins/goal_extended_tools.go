// Package builtins 扩展 goal 工具：add_gate / add_subgoal / clear_subgoals。
package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// GoalExtendedTools 返回 goal 扩展工具（门禁/子目标管理）。
func GoalExtendedTools(gs *agent.GoalService) []plugins.Tool {
	return []plugins.Tool{
		goalAddGateTool(gs),
		goalAddSubgoalTool(gs),
		goalClearSubgoalsTool(gs),
	}
}

func goalAddGateTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "add_gate",
		Description: "Add a quality gate (deterministic shell command) to the current goal. Args: {command, timeout_seconds?: int, max_retries?: int}. The gate must pass before the goal can be declared done.",
		Run: func(args string) (string, error) {
			var p struct {
				Command        string `json:"command"`
				TimeoutSeconds int    `json:"timeout_seconds"`
				MaxRetries     int    `json:"max_retries"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Command) == "" {
				return `{"error": "command required"}`, fmt.Errorf("add_gate: {command} required")
			}
			if err := gs.AddGate(strings.TrimSpace(p.Command), p.TimeoutSeconds, p.MaxRetries); err != nil {
				return `{"error": "` + err.Error() + `"}`, err
			}
			return `{"ok": true, "message": "gate added"}`, nil
		},
	}
}

func goalAddSubgoalTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "add_subgoal",
		Description: "Add a sub-criterion to the current goal. Args: {text}. All subgoals must be satisfied for the goal to be done.",
		Run: func(args string) (string, error) {
			text := strings.TrimSpace(args)
			if text == "" {
				return `{"error": "text required"}`, fmt.Errorf("add_subgoal: text required")
			}
			if err := gs.AddSubgoal(text); err != nil {
				return `{"error": "` + err.Error() + `"}`, err
			}
			return `{"ok": true, "message": "subgoal added"}`, nil
		},
	}
}

func goalClearSubgoalsTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "clear_subgoals",
		Description: "Clear all sub-goals from the current goal.",
		Run: func(args string) (string, error) {
			_ = args
			if err := gs.ClearSubgoals(); err != nil {
				return `{"error": "` + err.Error() + `"}`, err
			}
			return `{"ok": true, "message": "subgoals cleared"}`, nil
		},
	}
}
