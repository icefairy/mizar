package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Compactor 会话压缩器。
//
// 设计目标（综合 pi compaction 源码 + 2026 缓存最佳实践）：
//  1. 触发：估算 context tokens > ContextWindow - ReserveTokens
//  2. 切点：从后往前累计 KeepRecentTokens，只在普通 user/assistant 消息处切
//     （绝不切在 tool_result——它必须跟随 tool_call）
//  3. 摘要：结构化格式（Goal/Progress/Decisions/Next Steps/Critical Context），
//     保留精确文件路径/函数名/错误消息
//  4. 缓存友好：摘要插入位置固定（system 之后），摘要内容在两次压缩之间保持稳定，
//     使压缩后的前缀依然可命中缓存
type Compactor struct {
	ContextWindow    int   // 模型上下文窗口（token）
	ReserveTokens    int   // 窗口保留余量（默认 16384）
	KeepRecentTokens int   // 保留最近多少 token（默认 20000）
	MaxSummaryTokens int   // 摘要最大 token（默认 2000）

	// Summarize 实际执行摘要的 LLM 调用（由调用方注入，便于 mock 与独立路由）。
	// messages 是切点之前的所有消息，返回摘要文本。
	Summarize func(messages []Message) (string, error)
}

// DefaultCompactor 返回默认配置的压缩器。
func DefaultCompactor(summarize func([]Message) (string, error)) *Compactor {
	return &Compactor{
		ContextWindow:    128000,
		ReserveTokens:    16384,
		KeepRecentTokens: 20000,
		MaxSummaryTokens: 2000,
		Summarize:        summarize,
	}
}

// EstimateTokens 估算消息 token 数（保守启发式：中文按字符，其他按 4 字符/token）。
// 参照 pi 的 estimateTokens + chars/4 启发式，对中文更保守。
func EstimateTokens(m Message) int {
	if m.Content == "" {
		return 0
	}
	chars := utf8.RuneCountInString(m.Content)
	cjk := 0
	for _, r := range m.Content {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		}
	}
	nonCJK := chars - cjk
	return cjk + (nonCJK+3)/4
}

// EstimateMessages 估算消息列表总 token。
func EstimateMessages(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m)
	}
	return total
}

// ShouldCompact 判断是否应触发压缩。
func (c *Compactor) ShouldCompact(estimated int) bool {
	if c == nil || c.Summarize == nil {
		return false
	}
	return estimated > c.ContextWindow-c.ReserveTokens
}

// FindCutPoint 找切点：从后往前累计消息，直到达到 KeepRecentTokens。
// 返回切点索引（从该索引开始保留），切点前的消息将被摘要。
// 返回 -1 表示没有合法切点（全部保留）。
//
// 算法（参照 pi findCutPoint）：
//   - 从后往前累计消息 token，超过预算的最近一个合法切点即为 cutIndex
//   - 合法切点 = IsCutPoint() 为 true（普通 user/assistant，非 tool_result/summary）
//   - 保证保留消息以切点为界，不会截断 tool_call 与其 tool_result 的配对
func (c *Compactor) FindCutPoint(msgs []Message) int {
	if c == nil {
		return -1
	}
	// 从后往前找最后一个合法切点（作为候选下界）
	lastCut := -1
	for i, m := range msgs {
		if m.IsCutPoint() {
			lastCut = i
		}
	}
	if lastCut < 0 {
		return -1 // 没有可切的消息
	}
	// 从后往前累计
	acc := 0
	cut := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		acc += EstimateTokens(msgs[i])
		if acc >= c.KeepRecentTokens {
			// 找最近的合法切点（<= i）
			for j := i; j >= 0; j-- {
				if msgs[j].IsCutPoint() {
					cut = j
					break
				}
			}
			break
		}
	}
	// 若累计不足 KeepRecentTokens，说明全部内容都在预算内，无需压缩。
	if cut < 0 {
		return -1
	}
	// 不能切在 index 0（system 之外的第一条），至少保留一条
	if cut < 0 {
		return -1
	}
	return cut
}

// Compact 执行压缩：找到切点 → 对切点前消息生成摘要 → 返回新消息列表。
// 结构: [system] + [summary(user, KindSummary)] + [cut: 保留的最近消息]
func (c *Compactor) Compact(msgs []Message) ([]Message, error) {
	if c == nil || c.Summarize == nil {
		return msgs, nil
	}
	cut := c.FindCutPoint(msgs)
	if cut <= 0 {
		return msgs, nil
	}
	toSummarize := msgs[:cut]
	keep := msgs[cut:]
	summary, err := c.Summarize(toSummarize)
	if err != nil {
		return msgs, fmt.Errorf("summarize: %w", err)
	}
	summary = strings.TrimSpace(summary)
	// 重建：保留 system（若有），插入摘要，再接保留消息
	var out []Message
	for _, m := range msgs {
		if m.Role == RoleSystem {
			out = append(out, m)
			break
		}
	}
	if len(out) == 0 {
		out = append(out, Message{Role: RoleSystem, Content: ""})
	}
	out = append(out, Message{Role: RoleUser, Content: summary, Kind: KindSummary})
	out = append(out, keep...)
	return out, nil
}

// SummaryPrompt 生成摘要提示词（供 LLM 调用方使用）。
// 参照 pi 的 SUMMARIZATION_PROMPT 结构化格式。
const SummaryPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish?]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Data, examples, references, exact file paths, function names, error messages needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`
