package builtins

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mizar/internal/plugins"
	"mizar/internal/schedule"
)

// ScheduleTools 返回 schedule_create / schedule_list / schedule_delete 三个工具。
func ScheduleTools(reg *schedule.Registry) []plugins.Tool {
	return []plugins.Tool{
		schedCreateTool(reg),
		schedListTool(reg),
		schedDeleteTool(reg),
	}
}

func schedCreateTool(reg *schedule.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "schedule_create",
		Description: "Schedule a reminder for later. Args: {kind: 'after'|'at'|'every', prompt: string, after_secs?: number, at?: string(RFC3339), every_secs?: number(min 300)}. Returns reminder id.",
		Run: func(args string) (string, error) {
			var p struct {
				Kind      string `json:"kind"`
				Prompt    string `json:"prompt"`
				AfterSecs int    `json:"after_secs"`
				At        string `json:"at"`
				EverySecs int    `json:"every_secs"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Prompt) == "" || p.Kind == "" {
				return "", fmt.Errorf("schedule_create: args {kind, prompt} required (kind=after|at|every)")
			}
			var atTime time.Time
			if p.At != "" {
				t, err := time.Parse(time.RFC3339, p.At)
				if err != nil {
					return "", fmt.Errorf("schedule_create: invalid at time (need RFC3339): %w", err)
				}
				atTime = t
			}
			id, err := reg.Create(p.Kind, p.Prompt, p.AfterSecs, atTime, p.EverySecs)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("✓ 提醒已创建: %s（%s）", id, p.Kind), nil
		},
	}
}

func schedListTool(reg *schedule.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "schedule_list",
		Description: "List all pending (undelivered) scheduled reminders.",
		Run: func(args string) (string, error) {
			_ = args
			rs := reg.List()
			return schedule.RenderList(rs), nil
		},
	}
}

func schedDeleteTool(reg *schedule.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "schedule_delete",
		Description: "Cancel a scheduled reminder by id.",
		Run: func(args string) (string, error) {
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.ID == "" {
				return "", fmt.Errorf("schedule_delete: args {id} required")
			}
			if err := reg.Delete(p.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("✓ 提醒 %s 已取消", p.ID), nil
		},
	}
}
