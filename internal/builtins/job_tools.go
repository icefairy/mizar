package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/jobs"
	"mizar/internal/plugins"
)

// agentAskUser 全局回调：由 main.go 在 TUI 模式设置（由 tuiModel 实现）。
// Server/headless 模式保持 nil（工具返回降级提示）。
var agentAskUser func(questionsJSON string) (string, error)

// SetAskUser 设置 ask_user_question 工具的回调（由 TUI 调用）。
func SetAskUser(fn func(questionsJSON string) (string, error)) {
	agentAskUser = fn
}

// JobTools 返回 job_list / job_output / job_kill 三个工具。
// 需要传入 jobs.Registry 实例（与 Agent.Jobs 同一实例，保证跨工具一致性）。
func JobTools(reg *jobs.Registry) []plugins.Tool {
	return []plugins.Tool{
		jobListTool(reg),
		jobOutputTool(reg),
		jobKillTool(reg),
	}
}

func jobListTool(reg *jobs.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "job_list",
		Description: "List background jobs (running and finished) with their ids, kinds, and statuses.",
		Run: func(args string) (string, error) {
			js := reg.List()
			if len(js) == 0 {
				return "（无后台任务）", nil
			}
			var sb strings.Builder
			for _, j := range js {
				errLine := ""
				if j.Error != "" {
					errLine = " error:" + truncate(j.Error, 80)
				}
				fmt.Fprintf(&sb, "  %-12s %-10s %-10s%s\n", j.ID, j.Kind, j.Status, j.Label)
				if errLine != "" {
					fmt.Fprintf(&sb, "           %s\n", errLine)
				}
			}
			return sb.String(), nil
		},
	}
}

func jobOutputTool(reg *jobs.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "job_output",
		Description: "Read a background job's output. Args: {job_id, wait?: bool, timeout_ms?: number}. Returns full output once terminal; with wait=true blocks until done or timeout.",
		Run: func(args string) (string, error) {
			var p struct {
				JobID     string `json:"job_id"`
				Wait      bool   `json:"wait"`
				TimeoutMs int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.JobID == "" {
				return "", fmt.Errorf("job_output: args {job_id} required")
			}
			out, status, ok := reg.Output(p.JobID)
			if !ok {
				return "", fmt.Errorf("unknown job: %s", p.JobID)
			}
			if p.Wait && status == "running" {
				if p.TimeoutMs <= 0 {
					p.TimeoutMs = 30_000
				}
				for i := 0; i < p.TimeoutMs/100; i++ {
					_, st, ok := reg.Output(p.JobID)
					if !ok || st != "running" {
						if ok {
							out, status, _ = reg.Output(p.JobID)
						}
						break
					}
				}
			}
			sb := fmt.Sprintf("[status: %s]\n", status)
			if out != "" {
				sb += out
			}
			if status == "errored" || status == "killed" {
				sb += fmt.Sprintf("\nerror: %s", truncate(out, 200))
			}
			return sb, nil
		},
	}
}

func jobKillTool(reg *jobs.Registry) plugins.Tool {
	return plugins.Tool{
		Name:        "job_kill",
		Description: "Request cancellation of a running background job by job id. Returns immediately; the job settles as killed once its work actually stops.",
		Run: func(args string) (string, error) {
			var p struct {
				JobID  string `json:"job_id"`
				Reason string `json:"reason,omitempty"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.JobID == "" {
				return "", fmt.Errorf("job_kill: args {job_id} required")
			}
			reason := p.Reason
			if reason == "" {
				reason = "killed via job_kill"
			}
			if err := reg.Kill(p.JobID, reason); err != nil {
				return "", err
			}
			return fmt.Sprintf("✓ job %s 已请求终止（原因: %s）", p.JobID, reason), nil
		},
	}
}
