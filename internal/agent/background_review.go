// Package agent 实现 BackgroundReview：每轮结束后后台分析对话，自动沉淀 skill 或 memory。
//
// 复刻 hermes background_review.py：
//   - 每轮 LLM 响应后，在后台 goroutine 中 fork 一个辅助 agent
//   - 辅助 agent 只查看当前对话 snapshot，判断是否产生了可复用的 skill 或 memory
//   - 写入 ~/.mizar/skills/ 或 ~/.mizar/memory.md
//   - 主对话完全不受影响（🟢 ImpactSafe）
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ============================================================================
// BackgroundReviewConfig
// ============================================================================

// BackgroundReviewConfig 后台review配置。
type BackgroundReviewConfig struct {
	// Enabled 是否启用（nil=默认开启）。
	Enabled *bool
	// MinTurnsBetweenReviews 至少隔多少轮才触发一次 review（默认 3）。
	MinTurnsBetweenReviews int
	// AuxLLM 辅助模型（用于 review 分析，nil=不启用）。
	AuxLLM AuxiliaryLLM
	// SkillsDir 技能目录路径。
	SkillsDir string
	// MemoryFile 持久化 memory 文件路径（空=不写 memory）。
	//
	// 注意（2026-11-15 审计）：MEMORY.md 已升级为结构化四分类格式（internal/memory），
	// 直接 append 裸行会破坏文件结构。因此优先用 OnMemory 回调写进结构化存储；
	// MemoryFile 仅作为「没有回调时的兼容兜底」。
	MemoryFile string
	// OnMemory 记忆写入回调（推荐）。返回 error 只记录不重试。
	// main.go 会接成 memoryStore.Add("project", entry)，写入即刷新系统提示。
	OnMemory func(entry string) error
	// WorkDir 当前工作目录（用于识别「在哪个项目里干活」）。
	// review 会把项目名带进提示词，让模型产出/更新**项目技能**——
	// 下次在同个项目里开工时，这条技能一加载就等于恢复了相关记忆。
	WorkDir string
	// ProjectSkillPrefix 项目技能名前缀（默认 "project-"）。
	// 项目技能一律落在全局技能库（SkillsDir），不写进项目目录——
	// 这样换个机器/换个 checkout 也还在。
	ProjectSkillPrefix string
	// OnReview 回调：每次 review 完成后调用（可选）。
	OnReview func(summary string)
}

// DefaultBackgroundReviewConfig 返回默认配置。
func DefaultBackgroundReviewConfig() BackgroundReviewConfig {
	return BackgroundReviewConfig{
		MinTurnsBetweenReviews: 3,
		SkillsDir:              "", // 由调用方设置
		ProjectSkillPrefix:     "project-",
	}
}

// ============================================================================
// BackgroundReview
// ============================================================================

// BackgroundReview 管理后台 review goroutine。
type BackgroundReview struct {
	cfg                  BackgroundReviewConfig
	mu                   sync.Mutex
	turnsSinceLastReview int
}

// NewBackgroundReview 创建后台 review 控制器。
func NewBackgroundReview(cfg BackgroundReviewConfig) *BackgroundReview {
	if cfg.MinTurnsBetweenReviews <= 0 {
		cfg.MinTurnsBetweenReviews = 3
	}
	return &BackgroundReview{cfg: cfg}
}

// Advance 每轮调用一次（在 turn 结束后）。
//
// 语义修正（2026-11-15 审计）：此前 must 先 Start() 才工作，而 Start() 全仓从未
// 被调用 → 该功能实际完全失效。现在 Enabled() 只看配置（默认开启），不再依赖
// 后台 loop 是否启动——review 本身就是「每 N 轮起一个一次性 goroutine」，
// 不需要常驻循环。
func (br *BackgroundReview) Advance(lastResponse string, msgs []Message) {
	if !br.Enabled() || br.cfg.AuxLLM == nil {
		return
	}
	br.mu.Lock()
	br.turnsSinceLastReview++
	shouldReview := br.turnsSinceLastReview >= br.cfg.MinTurnsBetweenReviews
	if shouldReview {
		br.turnsSinceLastReview = 0
	}
	br.mu.Unlock()
	if !shouldReview {
		return
	}
	// 异步触发 review（不阻塞主循环）
	go br.triggerReview(lastResponse, msgs)
}

// ForceReview 立即触发一次复盘（技能复盘按「工具迭代数」到期，不走轮次计数）。
// 与 Advance 的区别：不检查轮数阈值、不推进轮次计数——调用方（loop）已经
// 按 ReviewSignals 的双计数器判定过「该做了」。
func (br *BackgroundReview) ForceReview() {
	br.ForceReviewWith("", nil)
}

// ForceReviewWith 带对话素材的立即复盘（技能侧按工具迭代数到期时由 loop 调用）。
// lastResponse 与 msgs 都为空时退化为「只按项目上下文判断」——信息量有限，
// 但仍可能更新项目技能（例如补齐构建命令）。
func (br *BackgroundReview) ForceReviewWith(lastResponse string, msgs []Message) {
	if br == nil || !br.Enabled() || br.cfg.AuxLLM == nil {
		return
	}
	go br.triggerReview(lastResponse, msgs)
}

// Interval 返回复盘间隔轮数（供 ReviewSignals 复用同一阈值，避免两处真相）。
func (br *BackgroundReview) Interval() int {
	if br == nil {
		return 3
	}
	if br.cfg.MinTurnsBetweenReviews <= 0 {
		return 3
	}
	return br.cfg.MinTurnsBetweenReviews
}

// Enabled 报告后台自学习是否启用：显式 false 才关闭（nil = 默认开启）。
func (br *BackgroundReview) Enabled() bool {
	if br == nil {
		return false
	}
	if br.cfg.Enabled != nil && !*br.cfg.Enabled {
		return false
	}
	return true
}

func (br *BackgroundReview) triggerReview(lastResponse string, msgs []Message) {
	if !br.Enabled() {
		return
	}
	if br.cfg.AuxLLM == nil {
		return
	}

	// 构建 review prompt
	reviewPrompt := br.buildReviewPrompt(lastResponse, msgs)
	reply, err := br.cfg.AuxLLM.Chat([]Message{
		{Role: RoleSystem, Content: buildReviewSystemPrompt()},
		{Role: RoleUser, Content: reviewPrompt},
	})
	if err != nil {
		return // 静默失败，不影响主循环
	}

	// 解析结果
	result, err := parseReviewResult(reply)
	if err != nil {
		return
	}

	// 执行写操作
	// update_skill：显式覆盖已有项目技能（模型已按提示做过合并，写回整份即可）。
	// new_skill：只在不存在时创建，避免把别人（或自己上次）写的内容冲掉。
	if result.Action == "update_skill" && result.NewSkill != "" {
		br.writeSkill(result.NewSkillName, result.NewSkill, true)
	} else if result.NewSkill != "" {
		br.writeSkill(result.NewSkillName, result.NewSkill, false)
	}
	if result.MemoryEntry != "" {
		br.writeMemory(result.MemoryEntry)
	}

	if br.cfg.OnReview != nil {
		summary := fmt.Sprintf("review: skill=%q memory=%v", result.NewSkillName, result.MemoryEntry != "")
		br.cfg.OnReview(summary)
	}
}

func (br *BackgroundReview) buildReviewPrompt(lastResponse string, msgs []Message) string {
	var sb strings.Builder

	// 项目上下文：告诉模型「你在哪个项目里干活」以及该项目的技能叫什么。
	// 这是「聊过一个项目 → 沉淀成技能 → 下次加载即恢复记忆」的关键。
	if name := br.projectSkillName(); name != "" {
		sb.WriteString("当前工作目录（项目）：" + br.cfg.WorkDir + "\n")
		sb.WriteString("本项目对应的技能名：**" + name + "**\n\n")
		if body := br.readProjectSkill(name); body != "" {
			sb.WriteString("该技能当前内容（请在其基础上**增量更新**，不要丢掉已有内容）：\n```markdown\n")
			sb.WriteString(truncate(body, 4000))
			sb.WriteString("\n```\n\n")
		} else {
			sb.WriteString("（该技能尚不存在——如果这轮确实产生了项目相关的可复用知识，就创建它）\n\n")
		}
	}

	sb.WriteString("最近一轮 AI 回复：\n")
	sb.WriteString(truncate(lastResponse, 2000))
	sb.WriteString("\n\n")
	sb.WriteString("对话上下文（最近 5 条消息）：\n")
	start := len(msgs) - 5
	if start < 0 {
		start = 0
	}
	for _, m := range msgs[start:] {
		role := string(m.Role)
		content := truncate(m.Content, 500)
		sb.WriteString(fmt.Sprintf("[%s] %s\n", role, content))
	}
	return sb.String()
}

// projectSkillName 由 WorkDir 推导项目技能名（如 /data/codes/mizar → project-mizar）。
// 目录名为空/不可用时返回空串（此时不做项目技能沉淀）。
func (br *BackgroundReview) projectSkillName() string {
	if br == nil || strings.TrimSpace(br.cfg.WorkDir) == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(br.cfg.WorkDir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	prefix := br.cfg.ProjectSkillPrefix
	if prefix == "" {
		prefix = "project-"
	}
	return prefix + sanitizeSkillSlug(base)
}

// sanitizeSkillSlug 把目录名转成安全的技能名片段（小写，非字母数字转连字符）。
func sanitizeSkillSlug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	return out
}

// readProjectSkill 读已有项目技能正文（不存在返回空串）。
func (br *BackgroundReview) readProjectSkill(name string) string {
	if br == nil || br.cfg.SkillsDir == "" || name == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(br.cfg.SkillsDir, name, "SKILL.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func buildReviewSystemPrompt() string {
	return `You are a knowledge distillation assistant. Analyze an AI agent's recent conversation turn and decide whether anything worth remembering was produced.

Decide ONE of:
- "update_skill": The named project skill already exists and should be EXTENDED (add a rule, a pitfall, a path, a command). Output the FULL new SKILL.md body — merge old content with the new knowledge, never drop what was already there.
- "new_skill": A reusable procedural pattern worth capturing (a way to do a specific type of task), and no project skill exists yet. Output its name and full SKILL.md body.
- "memory": An important standalone fact worth remembering (preference, convention, external system). Output the entry.
- "nothing": Nothing worth persisting.

Reply ONLY with a single JSON object on one line:
{"action": "update_skill", "skill_name": "project-foo", "skill_body": "..."}
{"action": "new_skill", "skill_name": "my-skill", "skill_body": "..."}
{"action": "memory", "entry": "important fact here..."}
{"action": "nothing"}

PROJECT SKILLS (highest priority when a project is in play):
- If the prompt names a project skill and that skill already exists, prefer "update_skill" — the whole point
  is that next time the user works on this project, loading that one skill restores the relevant memory.
- Project skills are cumulative project notes. Good content: what this project is for, its layout and key
  files, build/test/run commands, conventions that must be followed, gotchas hit in practice, external
  endpoints/paths that matter. Bad content: one-off task results, timestamps, "today we fixed X".
- Patching beats rewriting: keep existing sections, add the new knowledge where it belongs. If a section
  would grow past ~30 lines, compress it to the rule rather than the transcript.
- If the project skill does NOT exist yet and this session produced real project knowledge, create it
  with "new_skill" using exactly the name the prompt suggests.

Rules:
- "new_skill" must be a general procedure, not a one-time task result.
- "memory" must be a factual statement, not a plan or goal; one fact per entry.
- Never fabricate skills or memories that weren't clearly demonstrated in the conversation.
- If the session produced nothing durable, reply "nothing" — a wrong note misleads every future session.`
}

func parseReviewResult(text string) (struct {
	NewSkillName, NewSkill, MemoryEntry string
	Action                              string
}, error) {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 {
		return struct {
			NewSkillName, NewSkill, MemoryEntry string
			Action                              string
		}{}, fmt.Errorf("no JSON in review output")
	}
	var result struct {
		Action    string `json:"action"`
		SkillName string `json:"skill_name"`
		SkillBody string `json:"skill_body"`
		Entry     string `json:"entry"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &result); err != nil {
		return struct {
			NewSkillName, NewSkill, MemoryEntry string
			Action                              string
		}{}, err
	}
	return struct {
		NewSkillName, NewSkill, MemoryEntry string
		Action                              string
	}{
		Action:       result.Action,
		NewSkillName: result.SkillName,
		NewSkill:     result.SkillBody,
		MemoryEntry:  result.Entry,
	}, nil
}

// writeSkill 写技能文件。
//
// overwrite=false（new_skill）：目标已存在则**跳过**——后台复盘不该覆盖已有内容
// （可能是用户手写的、或上一轮更完整的版本）。
// overwrite=true（update_skill）：模型已按要求做过合并，整份写回。
func (br *BackgroundReview) writeSkill(name, body string, overwrite bool) {
	if name == "" || body == "" || br.cfg.SkillsDir == "" {
		return
	}
	dir := filepath.Join(br.cfg.SkillsDir, name)
	path := filepath.Join(dir, "SKILL.md")
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return // 已存在，保留原样
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return
	}
}

// writeNewSkill 兼容旧调用点（等价于 writeSkill(..., false)）。
func (br *BackgroundReview) writeNewSkill(name, body string) {
	br.writeSkill(name, body, false)
}

func (br *BackgroundReview) writeMemory(entry string) {
	if entry == "" {
		return
	}
	// 优先走回调（结构化存储，写入即刷新系统提示）
	if br.cfg.OnMemory != nil {
		_ = br.cfg.OnMemory(entry)
		return
	}
	if br.cfg.MemoryFile == "" {
		return
	}
	f, err := os.OpenFile(br.cfg.MemoryFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", entry)
}
