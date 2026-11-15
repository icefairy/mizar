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
	// OnReview 回调：每次 review 完成后调用（可选）。
	OnReview func(summary string)
}

// DefaultBackgroundReviewConfig 返回默认配置。
func DefaultBackgroundReviewConfig() BackgroundReviewConfig {
	return BackgroundReviewConfig{
		MinTurnsBetweenReviews: 3,
		SkillsDir:              "", // 由调用方设置
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
	if result.NewSkill != "" {
		br.writeNewSkill(result.NewSkillName, result.NewSkill)
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

func buildReviewSystemPrompt() string {
	return `You are a knowledge distillation assistant. Your job is to analyze an AI agent's recent conversation turn and decide if anything worth remembering was produced.

Analyze the output and decide ONE of:
- "new_skill": You identified a reusable procedural pattern (a way to do a specific type of task). Output the skill name and full SKILL.md body.
- "memory": You identified an important fact or context worth remembering for future turns. Output the memory entry.
- "nothing": Nothing worth persisting.

Reply ONLY with a single JSON object on one line:
{"action": "new_skill", "skill_name": "my-skill", "skill_body": "..."}
{"action": "memory", "entry": "important fact here..."}
{"action": "nothing"}

Rules:
- A "new_skill" must be a general procedure, not a one-time task result
- A "memory" must be a factual statement, not a plan or goal
- Never fabricate skills or memories that weren't clearly demonstrated`
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

func (br *BackgroundReview) writeNewSkill(name, body string) {
	if name == "" || body == "" {
		return
	}
	dir := filepath.Join(br.cfg.SkillsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return
	}
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
