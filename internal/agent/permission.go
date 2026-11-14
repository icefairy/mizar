// Package agent 权限门禁（PermissionMode，对齐 Claude Code PermissionMode）。
//
// 解决的问题（docs/gap-analysis.md GAP-S2/C-5）：主循环工具执行没有审批层——
// 子代理拿到全量工具（bash/edit/write）不带任何确认。本文件引入四模式 +
// 三级风险分级的执行前门禁，主代理与子代理共用同一个 gate 实例（进程级
// 策略，模式运行时可变，见 clone.go 共享清单）。
//
// 设计要点：
//   - 出厂默认 PermAuto（全放行）= 现行为零破坏；用户经 config.json
//     permission_mode 或 /permission 命令 opt-in（2026-11-14 用户确认）。
//   - bash 走命令级危险匹配而非整工具确认——每次 bash 都问等于不可用。
//     宁可漏报不误报：清单只收高确定性危险模式，普通命令（go test、rm 单文件）放行。
//   - 确认复用 builtins.SetAskUser 包级全局通路（TUI/interactive/Server 三处
//     已接线）；gate 内 askMu 串行化审批——TUI 的 askPending 是单槽，并发
//     确认请求排队而非互相覆盖弹窗。
//   - headless 无交互环境（ask == nil）：PermAsk 一律拒绝（fail-safe，用户
//     已确认），拒绝消息给模型三条出路（改配置/换交互模式/换方案）。
//   - 「本会话允许」写进程内 approve 缓存（key 用 CanonicalizeArgs 规范化，
//     键序无关）；容量 100 FIFO；刻意不持久化——避免"昨天批准的 rm -rf
//     今天静默通过"。
package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ============================================================================
// 模式与分级
// ============================================================================

// PermissionMode 权限模式。
type PermissionMode string

const (
	// PermAuto 全放行（出厂默认 = 现行为）。
	PermAuto PermissionMode = "auto"
	// PermDefault 只读放行；写类工具与 bash 危险命令需人工确认。
	PermDefault PermissionMode = "default"
	// PermAcceptEdits 写类工具放行；bash 危险命令需人工确认。
	PermAcceptEdits PermissionMode = "acceptEdits"
	// PermPlan 只读放行；写类与危险命令直接拒绝（不询问）。
	PermPlan PermissionMode = "plan"
)

// ParsePermissionMode 解析配置/命令传入的模式字符串。
// 空串视为 auto（未配置）；大小写不敏感；非法值返回 (PermAuto, false)。
func ParsePermissionMode(s string) (PermissionMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return PermAuto, true
	case "default":
		return PermDefault, true
	case "acceptedits", "accept_edits":
		return PermAcceptEdits, true
	case "plan":
		return PermPlan, true
	case "":
		return PermAuto, true
	}
	return PermAuto, false
}

// ToolTier 工具风险分级。
type ToolTier int

const (
	TierSafe   ToolTier = iota // 只读/无落盘副作用（未列出的工具也归此类）
	TierEdit                   // 写类：改文件/改机器状态
	TierDanger                 // bash 危险命令
)

// PermissionDecision 单次判定结果。
type PermissionDecision int

const (
	PermAllow PermissionDecision = iota
	PermAsk
	PermDeny
)

// ============================================================================
// PermissionGate
// ============================================================================

// AskFunc 确认回调，与 builtins.SetAskUser 的回调同形（agent 包不能 import
// builtins，防循环依赖；接线处直接传 m.askViaChat / srv.AskUser 等）。
type AskFunc func(questionsJSON string) (string, error)

// approveCacheMax 「本会话允许」缓存容量，FIFO 淘汰。
const approveCacheMax = 100

// PermissionGate 权限门禁。多 Run（Web 会话克隆 + 子代理）并发共用同一实例，
// mode/approve 由 mu 保护；askMu 单独串行化审批（不与 mu 嵌套）。
type PermissionGate struct {
	mu      sync.Mutex
	mode    PermissionMode
	approve map[string]bool // key = tool + "|" + CanonicalizeArgs(args)
	order   []string        // FIFO 淘汰序

	askMu sync.Mutex // 串行化审批：同一时刻只挂一个确认弹窗（TUI askPending 单槽）
	ask   AskFunc    // nil = 无交互环境
}

// NewPermissionGate 构造指定模式的门禁。
func NewPermissionGate(mode PermissionMode) *PermissionGate {
	return &PermissionGate{
		mode:    mode,
		approve: map[string]bool{},
	}
}

// SetMode 切换模式（内存即时生效；持久化由 /permission 命令写 config）。
func (g *PermissionGate) SetMode(m PermissionMode) {
	g.mu.Lock()
	g.mode = m
	g.mu.Unlock()
}

// Mode 当前模式。
func (g *PermissionGate) Mode() PermissionMode {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mode
}

// SetAskUser 注入确认回调（与 builtins.SetAskUser 同一函数接线）。
func (g *PermissionGate) SetAskUser(fn AskFunc) {
	g.mu.Lock()
	g.ask = fn
	g.mu.Unlock()
}

// ApprovalCount 当前「本会话允许」缓存条数（/permission 展示用）。
func (g *PermissionGate) ApprovalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.approve)
}

// ResetApprovals 清空审批缓存，返回清掉的条数。
func (g *PermissionGate) ResetApprovals() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.approve)
	g.approve = map[string]bool{}
	g.order = nil
	return n
}

// Authorize 工具执行前的唯一入口（loop.go runOneToolCall 调用）。
// 返回 (allowed, denyMsg)：false 时 denyMsg 为回填给模型的拒绝原因。
func (g *PermissionGate) Authorize(tool, args string) (bool, string) {
	tier := toolTier(tool)
	if tool == "bash" {
		tier = bashTier(args)
	}
	g.mu.Lock()
	mode := g.mode
	key := tool + "|" + CanonicalizeArgs(args)
	cached := g.approve[key]
	g.mu.Unlock()

	if mode == PermAuto || tier == TierSafe || cached {
		return true, ""
	}

	// 决策矩阵
	var d PermissionDecision
	switch {
	case mode == PermPlan && tier != TierSafe:
		d = PermDeny
	case mode == PermDefault && tier != TierSafe:
		d = PermAsk
	case mode == PermAcceptEdits && tier == TierDanger:
		d = PermAsk
	default:
		d = PermAllow
	}
	if d == PermAllow {
		return true, ""
	}
	if d == PermDeny {
		return false, buildDenyMsg(mode, tool, tier, args)
	}

	// PermAsk：串行化确认（TUI askPending 单槽，第二个并发请求在此排队）
	g.askMu.Lock()
	defer g.askMu.Unlock()
	g.mu.Lock()
	ask := g.ask
	g.mu.Unlock()
	if ask == nil {
		// 双检后仍无交互环境：fail-safe 拒绝（用户已确认的策略）
		return false, buildNoAskDenyMsg(mode, tool, args)
	}
	ans, _ := ask(buildAskJSON(mode, tool, args))
	switch parseAskAnswer(ans) {
	case "allow-once":
		return true, ""
	case "allow-session":
		g.mu.Lock()
		g.addApprove(key)
		g.mu.Unlock()
		return true, ""
	default: // 拒绝 / 空串 / 中止 / 未知 label 一律 deny
		return false, buildUserDenyMsg(tool, args)
	}
}

// addApprove 写缓存（调用方必须已持有 g.mu），超限 FIFO 淘汰最早一条。
func (g *PermissionGate) addApprove(key string) {
	if g.approve[key] {
		return
	}
	g.approve[key] = true
	g.order = append(g.order, key)
	if len(g.order) > approveCacheMax {
		oldest := g.order[0]
		g.order = g.order[1:]
		delete(g.approve, oldest)
	}
}

// ============================================================================
// 风险分级
// ============================================================================

// tierEditTools 写类工具显式清单：改文件 / 改机器状态。未列出的工具一律
// TierSafe（宁可少打扰）：mcp_call、用户插件、delegate_task（委派不落盘，
// 风险由子代理内逐工具把关）、todo/goal/schedule 等会话状态管理类。
var tierEditTools = map[string]bool{
	"write":         true,
	"edit":          true,
	"skill_manage":  true, // 会写技能文件
	"skill_manager": true, // 同上的历史别名
	"move_mouse":    true,
	"click":         true,
	"send_key":      true,
	"type_text":     true,
	"drag":          true,
}

// toolTier 工具风险分级（bash 不在此表，走 bashTier 命令级判定）。
func toolTier(tool string) ToolTier {
	if tierEditTools[strings.ToLower(strings.TrimSpace(tool))] {
		return TierEdit
	}
	return TierSafe
}

// 危险命令模式（宁可漏报不误报，只收高确定性模式）。
var dangerousBashPatterns = []*regexp.Regexp{
	// 递归删除：rm 后（可隔着其他参数）出现含 r/R 的 flag 组（-r/-rf/-fr/-rd）；
	// rm 单文件 / rm -f 单文件不拦。
	regexp.MustCompile(`\brm\s+(?:\S+\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\b`),
	regexp.MustCompile(`\bmkfs\b`),                                   // 格式化
	regexp.MustCompile(`\bdd\b[^|;&]*\bof=/dev/`),                    // 裸设备写入
	regexp.MustCompile(`\b(shutdown|reboot|poweroff|halt)\b`),        // 关机重启
	regexp.MustCompile(`\bgit\s+push\b[^|;&]*(--force\b|\s-f\b)`),    // 强推
	regexp.MustCompile(`(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba)?sh\b`), // 管道执行远端脚本
	regexp.MustCompile(`\bchmod\s+(-R\s+)?777\s+/(?:\s|$)`),          // 根目录全开放
	regexp.MustCompile(`\btruncate\s+-s\s*0?\s*/dev/`),               // 清空裸设备
	regexp.MustCompile(`>\s*/dev/sd[a-z]`),                           // 重定向写裸设备
	regexp.MustCompile(`\bfind\s+(?:/|~)\s?[^|;&]*-delete`),          // find 全盘/家目录删除
	regexp.MustCompile(`\binit\s+[06]\b`),                            // 切换运行级
}

// forkBomb fork 炸弹字面量（正则转义太绕，直接子串匹配）。
const forkBombLiteral = ":(){ :|:& };:"

// isDangerousBash 判断命令是否危险。cmd 已从 args JSON 提取。
func isDangerousBash(cmd string) bool {
	if strings.TrimSpace(cmd) == "" {
		return true // 拿不到命令内容：保守视为危险（上层 args 解析失败同样走这里）
	}
	if strings.Contains(cmd, forkBombLiteral) {
		return true
	}
	for _, re := range dangerousBashPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// bashTier bash 工具的命令级分级：普通命令放行（default 下每次确认等于不可用），
// 危险命令 TierDanger。args 非 JSON / 无 command 字段 → 保守 TierDanger。
func bashTier(args string) ToolTier {
	cmd := extractCommand(args)
	if isDangerousBash(cmd) {
		return TierDanger
	}
	return TierSafe
}

// extractCommand 从 bash 工具 args JSON 取 command 字段（宽容剥壳，思路同
// unwrapArgsJSON：模型可能多包一层 args）。
func extractCommand(args string) string {
	trimmed := strings.TrimSpace(args)
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return ""
	}
	for depth := 0; depth < 3; depth++ {
		m, ok := v.(map[string]any)
		if !ok {
			break
		}
		if cmd, ok := m["command"].(string); ok {
			return cmd
		}
		next, found := m["args"], false
		if x, ok := m["arguments"].(string); ok {
			next, found = x, true
		} else if x, ok := m["args"]; ok {
			next, found = x, true
		}
		if !found {
			break
		}
		if s, ok := next.(string); ok {
			var inner any
			if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &inner); err != nil {
				break
			}
			v = inner
			continue
		}
		v = next
	}
	return ""
}

// ============================================================================
// 确认弹窗（复用 builtins.SetAskUser 通路）
// ============================================================================

// buildAskJSON 构造单问题确认 JSON（形状对齐 builtins.Question，TUI/WebUI
// 现有弹窗直接渲染，无需前端改动）。
func buildAskJSON(mode PermissionMode, tool, args string) string {
	summary := truncate(strings.TrimSpace(args), 200)
	if tool == "bash" {
		if cmd := extractCommand(args); cmd != "" {
			summary = truncate(strings.TrimSpace(cmd), 200)
		}
	}
	questions := []map[string]any{{
		"id":       "perm-confirm",
		"header":   "权限确认",
		"question": fmt.Sprintf("⚠️ 权限确认（模式: %s）\n工具: %s\n内容: %s\n\n是否允许执行？", mode, tool, summary),
		"options": []map[string]string{
			{"label": "允许一次", "description": "仅本次执行，下次同命令仍会询问"},
			{"label": "本会话允许", "description": "记住该命令（同工具同参数），本会话内不再询问"},
			{"label": "拒绝", "description": "不执行，模型需要换方案"},
		},
	}}
	b, _ := json.Marshal(questions)
	return string(b)
}

// parseAskAnswer 解析确认弹窗的回答，返回 "allow-once" | "allow-session" | "deny"。
// 空串（ESC 中止）/ 解析失败 / 未知 label 一律 deny（fail-safe）。
func parseAskAnswer(answersJSON string) string {
	var answers []struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(answersJSON)), &answers); err != nil || len(answers) == 0 {
		return "deny"
	}
	v := strings.TrimSpace(answers[0].Value)
	switch {
	case strings.Contains(v, "本会话允许") || strings.Contains(v, "本会话"):
		return "allow-session"
	case strings.Contains(v, "允许一次") || strings.Contains(v, "允许"):
		return "allow-once"
	}
	return "deny"
}

// ============================================================================
// 拒绝文案（回填给模型，随工具结果消息进入对话）
// ============================================================================

func buildDenyMsg(mode PermissionMode, tool string, tier ToolTier, args string) string {
	what := toolSummary(tool, args)
	switch tier {
	case TierEdit:
		return fmt.Sprintf("权限门禁拒绝（模式: %s）：%s 属于写类操作，计划模式下只允许只读操作。"+
			"请把要做的修改整理进当前计划，或请用户执行 /permission acceptEdits（或 auto）切换权限模式。", mode, what)
	default:
		return fmt.Sprintf("权限门禁拒绝（模式: %s）：危险命令 %s 在计划模式下不允许执行。"+
			"请改用只读方式确认现状，或请用户切换权限模式（/permission acceptEdits 或 auto）。", mode, what)
	}
}

func buildUserDenyMsg(tool, args string) string {
	return fmt.Sprintf("用户拒绝了本次调用（%s）。不要原样重试，请换一个影响更小的方案，"+
		"或先用 ask_user_question 向用户说明理由再请求授权。", toolSummary(tool, args))
}

func buildNoAskDenyMsg(mode PermissionMode, tool, args string) string {
	return fmt.Sprintf("权限门禁拒绝（模式: %s）：操作 %s 需要人工确认，但当前无交互界面无法询问。可选处理："+
		"1) 请用户在配置中把 permission_mode 设为 auto；2) 切换到交互模式运行；"+
		"3) 改用无需确认的替代方案。不要盲目重试。", mode, toolSummary(tool, args))
}

// toolSummary 单行摘要：bash 显示命令内容，其他工具显示 args 摘要。
func toolSummary(tool, args string) string {
	if tool == "bash" {
		if cmd := extractCommand(args); cmd != "" {
			return fmt.Sprintf("bash: %q", truncate(strings.TrimSpace(cmd), 120))
		}
	}
	return fmt.Sprintf("%s(%s)", tool, truncate(strings.TrimSpace(args), 120))
}
