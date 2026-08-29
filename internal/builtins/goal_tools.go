package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

// GoalTools 返回 get_goal / create_goal / update_goal 三个工具。
func GoalTools(gs *agent.GoalService) []plugins.Tool {
	return []plugins.Tool{
		goalGetTool(gs),
		goalCreateTool(gs),
		goalUpdateTool(gs),
	}
}

func goalGetTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "get_goal",
		Description: "Read the current session goal. Returns null if no goal is active.",
		Run: func(args string) (string, error) {
			_ = args
			return gs.RenderJSON(), nil
		},
	}
}

func goalCreateTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "create_goal",
		Description: "Create a new long-running goal for the current session. Args: {objective, max_goal_rounds?: number}. Requires a direct human message in the current turn.",
		Run: func(args string) (string, error) {
			var p struct {
				Objective     string `json:"objective"`
				MaxGoalRounds int    `json:"max_goal_rounds"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Objective) == "" {
				return `{"goal": null}`, fmt.Errorf("create_goal: args {objective} required")
			}
			if err := gs.Create(strings.TrimSpace(p.Objective), p.MaxGoalRounds); err != nil {
				return `{"goal": null}`, err
			}
			g := gs.Get()
			return fmt.Sprintf(`{"goal": {"id": %q, "revision": %d, "objective": %q, "phase": %q, "roundsStarted": 0}}`,
				g.ID, g.Revision, g.Objective, g.Phase), nil
		},
	}
}

func goalUpdateTool(gs *agent.GoalService) plugins.Tool {
	return plugins.Tool{
		Name:        "update_goal",
		Description: "Edit, pause, resume, complete, or block the current goal. Args: {goal_id, revision, action, objective?, max_goal_rounds?, blocked_reason?}. Call get_goal first to read the exact id and revision.",
		Run: func(args string) (string, error) {
			var p struct {
				GoalID        string `json:"goal_id"`
				Revision      string `json:"revision"`
				Action        string `json:"action"`
				Objective     string `json:"objective"`
				MaxGoalRounds int    `json:"max_goal_rounds"`
				BlockedReason string `json:"blocked_reason"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.GoalID == "" || p.Revision == "" || p.Action == "" {
				return `{"goal": null}`, fmt.Errorf("update_goal: args {goal_id, revision, action} required")
			}
			if err := gs.Update(p.GoalID, p.Revision, p.Action, p.Objective, p.MaxGoalRounds, p.BlockedReason); err != nil {
				return `{"goal": null}`, err
			}
			return gs.RenderJSON(), nil
		},
	}
}
