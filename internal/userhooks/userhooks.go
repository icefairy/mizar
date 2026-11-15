// Package userhooks 用户级 hooks（C-6，对齐 CodeBuddy/Claude Code 的用户 settings hooks）。
//
// 插件 hooks（internal/agent/hooks.go）是 Go 层扩展点，面向插件作者；
// 本包让**用户**在 config.json 里配 shell 命令响应工具事件，不写 Go/TS 也能
// 拦截与观察：
//
//	{
//	  "hooks": {
//	    "pre_tool_use":  [{"command": "deny-dangerous.sh"}],
//	    "post_tool_use": [{"command": "audit-log.sh"}]
//	  }
//	}
//
// 事件上下文经 stdin 传 JSON（另注入 MIZAR_HOOK=1 环境变量，方便一行式脚本）：
//
//	pre_tool_use:  {"hook":"pre_tool_use","tool":"bash","args":"{...}","session":"...","cwd":"..."}
//	post_tool_use: {"hook":"post_tool_use","tool":...,"args":...,"output":"...(截断)","error":"..."}
//
// pre_tool_use 裁决语义（与 Claude Code 对齐）：
//   - stdout 输出 JSON {"decision":"deny","reason":"..."}          → 拒绝执行，reason 回填给模型
//   - stdout 输出 {"decision":"rewrite","tool":"...","args":"..."} → 改写后执行
//   - stdout 输出 {"decision":"allow"} 或空输出 + exit 0           → 放行
//   - exit 2                                                       → 拒绝，stderr 作为 reason
//   - 其它非零 exit / 超时 / 启动失败                               → 记日志放行（hook 故障不阻塞主流程）
//
// post_tool_use 只观察：stdout/stderr 记诊断日志，非零 exit 不影响任何流程。
//
// 本包不 import agent（internal/context → config 依赖链会把 import 环带回来）：
// 裁决/观察回调用自己的上下文参数，main.go 负责适配到 agent.Hooks。
package userhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"mizar/internal/shell"
)

// UserHook 单条 hook 配置。
type UserHook struct {
	Command   string `json:"command"`             // shell 命令（经平台默认 shell 执行）
	TimeoutMs int    `json:"timeout_ms,omitempty"` // 超时（默认 10s，上限 60s）
}

// Config config.json 的 hooks 段。
type Config struct {
	PreToolUse  []UserHook `json:"pre_tool_use,omitempty"`
	PostToolUse []UserHook `json:"post_tool_use,omitempty"`
}

// Decision pre_tool_use 的裁决值。
type Decision string

const (
	DecisionAllow   Decision = "allow"
	DecisionDeny    Decision = "deny"
	DecisionRewrite Decision = "rewrite"
)

// PreToolResult pre_tool_use 裁决结果。Rewrite 时 NewTool/NewArgs 为改写值。
type PreToolResult struct {
	Decision Decision
	Reason   string
	NewTool  string
	NewArgs  string
}

const (
	defaultHookTimeout = 10 * time.Second
	maxHookTimeout     = 60 * time.Second
	// maxPostOutput post_tool_use 上下文里工具输出的截断上限（太大撑爆 hook 进程）
	maxPostOutput = 4 * 1024
)

// Empty 是否未配置任何 hook（main.go 跳过接线的快速判断）。
func (c *Config) Empty() bool {
	return c == nil || (len(c.PreToolUse) == 0 && len(c.PostToolUse) == 0)
}

// PreToolDecision 返回工具调用前的裁决回调；未配置 pre_tool_use 时返回 nil。
// 多条 hook 依序执行，任一 deny 即拒绝；rewrite 各条依次生效（后一条看到前一条
// 的改写结果）；全部放行返回 allow。回调不 panic：hook 故障一律记日志放行。
func (c *Config) PreToolDecision(logf func(format string, args ...any)) func(tool, args, session string) PreToolResult {
	if c == nil || len(c.PreToolUse) == 0 {
		return nil
	}
	return func(tool, args, session string) PreToolResult {
		result := PreToolResult{Decision: DecisionAllow}
		for i, h := range c.PreToolUse {
			// 每条 hook 单独序列化：前一条 rewrite 的结果喂给下一条（裁决链语义与 agent.Hooks 一致）
			payload, _ := json.Marshal(map[string]string{
				"hook":    "pre_tool_use",
				"tool":    tool,
				"args":    args,
				"session": session,
				"cwd":     workDir(),
			})
			stdout, stderr, exitCode, err := runHook(h, payload)
			// 1) 结构化裁决优先（stdout 合法 JSON 且带 decision 字段）
			if d, ok := parseDecision(stdout); ok {
				switch Decision(d.Decision) {
				case DecisionDeny:
					reason := firstNonEmpty(d.Reason, "被用户 hook 拒绝")
					logf("userhooks pre[%d] deny tool=%s reason=%s", i, tool, truncateLog(reason))
					return PreToolResult{Decision: DecisionDeny, Reason: reason}
				case DecisionRewrite:
					if strings.TrimSpace(d.Tool) != "" {
						tool = d.Tool
					}
					if d.Args != "" {
						args = d.Args
					}
					result = PreToolResult{Decision: DecisionRewrite, Reason: d.Reason, NewTool: tool, NewArgs: args}
					logf("userhooks pre[%d] rewrite tool=%s args=%s", i, tool, truncateLog(args))
					continue
				default: // allow / 未知值
					continue
				}
			}
			// 2) exit 2 = 阻断（Claude Code 约定），stderr 作为给模型的原因
			if exitCode == 2 {
				reason := firstNonEmpty(stderr, "被用户 hook 拒绝（exit 2）")
				logf("userhooks pre[%d] deny(exit2) tool=%s reason=%s", i, tool, truncateLog(reason))
				return PreToolResult{Decision: DecisionDeny, Reason: reason}
			}
			// 3) 其余异常（非零 exit / 超时 / 启动失败）不阻塞主流程，只记日志
			if err != nil || exitCode != 0 {
				logf("userhooks pre[%d] 非阻断失败 tool=%s exit=%d err=%v stderr=%s",
					i, tool, exitCode, err, truncateLog(stderr))
			}
		}
		return result
	}
}

// PostToolObserve 返回工具结果观察回调；未配置 post_tool_use 时返回 nil。
// 只观察不干预：hook 失败只记日志，不影响任何流程。
func (c *Config) PostToolObserve(logf func(format string, args ...any)) func(tool, args, output string, toolErr error, session string) {
	if c == nil || len(c.PostToolUse) == 0 {
		return nil
	}
	return func(tool, args, output string, toolErr error, session string) {
		if len(output) > maxPostOutput {
			output = output[:maxPostOutput] + "…(截断)"
		}
		payload, _ := json.Marshal(map[string]string{
			"hook":    "post_tool_use",
			"tool":    tool,
			"args":    args,
			"output":  output,
			"error":   errText(toolErr),
			"session": session,
			"cwd":     workDir(),
		})
		for i, h := range c.PostToolUse {
			stdout, stderr, exitCode, err := runHook(h, payload)
			if err != nil || exitCode != 0 {
				logf("userhooks post[%d] 失败 tool=%s exit=%d err=%v stderr=%s",
					i, tool, exitCode, err, truncateLog(stderr))
				continue
			}
			if out := strings.TrimSpace(stdout); out != "" {
				logf("userhooks post[%d] tool=%s stdout=%s", i, tool, truncateLog(out))
			}
		}
	}
}

// hookDecision stdout 结构化裁决的载荷。
type hookDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Tool     string `json:"tool"`
	Args     string `json:"args"`
}

// parseDecision 解析 stdout 的结构化裁决；非 JSON / 无 decision 字段返回 false。
func parseDecision(stdout string) (hookDecision, bool) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return hookDecision{}, false
	}
	var d hookDecision
	if err := json.Unmarshal([]byte(trimmed), &d); err != nil || d.Decision == "" {
		return hookDecision{}, false
	}
	return d, true
}

// runHook 执行单条 hook：stdin 喂 JSON 上下文，注入 MIZAR_HOOK=1 环境变量。
// 返回分离的 stdout/stderr 与退出码（启动失败时 exitCode=-1）。
func runHook(h UserHook, stdin []byte) (stdout, stderr string, exitCode int, err error) {
	timeout := time.Duration(h.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	if timeout > maxHookTimeout {
		timeout = maxHookTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := shell.CommandContext(ctx, h.Command)
	c.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	c.Stdout, c.Stderr = &outBuf, &errBuf
	c.Env = append(os.Environ(), "MIZAR_HOOK=1")
	// 超时杀掉的是 shell 进程，其子进程（如 sleep）可能继承 stdout/stderr 管道
	// 继续存活；exec.Cmd.Wait 会等管道拷贝结束 → 超时形同虚设。WaitDelay 强制
	// 在进程退出后 1s 内掐断管道拷贝，孤儿输出丢弃（hook 场景可接受）。
	c.WaitDelay = time.Second
	runErr := c.Run()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exit, runErr
}

func workDir() string {
	wd, _ := os.Getwd()
	return wd
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func truncateLog(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}
