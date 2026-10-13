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
// 结构: [system?] + [summary(user, KindSummary)] + [cut: 保留的最近消息]
//
// split user span（参照 pi）：当切点落在某个 user span 内部（该 span 自身就超过
// KeepRecentTokens）时，额外为该 span 的前缀生成一份摘要，与历史摘要合并。
// 否则摘要里只有"用户想做什么"的概述，而该轮的具体进展（改了哪些文件、遇到什么错）
// 会因为前半段被摘要、后半段被保留而语义断裂。
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

	// split user span：切点在 span 内部，该 span 的前缀部分需要单独摘要
	if isSplitUserSpan(msgs, cut) {
		prefix := splitSpanPrefix(msgs, cut)
		if len(prefix) > 0 {
			prefixSummary, perr := c.Summarize(prefix)
			if perr == nil && strings.TrimSpace(prefixSummary) != "" {
				summary = summary + "\n\n## 当前用户消息的前半段（已完成部分）\n" + strings.TrimSpace(prefixSummary)
			}
			// 前缀摘要失败不阻断主流程：已有历史摘要仍可用
		}
	}

	// 重建：保留已有 system（不新增空占位），插入摘要，再接保留消息
	var out []Message
	if len(msgs) > 0 && msgs[0].Role == RoleSystem {
		out = append(out, msgs[0])
	}
	out = append(out, Message{Role: RoleUser, Content: summary, Kind: KindSummary})
	out = append(out, keep...)
	return out, nil
}

// isSplitUserSpan 判断切点是否落在某个 user span 的内部。
// user span = 从一条 user 消息开始，到下一个 user 消息之前的所有消息。
// cut 指向 span 内部（即 cut 往前找最近的一条 user 消息之后还有内容被切走）时为 true。
func isSplitUserSpan(msgs []Message, cut int) bool {
	if cut <= 0 || cut >= len(msgs) {
		return false
	}
	// 切点本身就是 user 消息 → 正好切在 span 边界，不是 split
	if msgs[cut].Role == RoleUser && msgs[cut].Kind != KindToolResult {
		return false
	}
	// 从 cut 往前找该 span 的起始 user 消息
	for i := cut - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser && msgs[i].Kind != KindToolResult {
			return true // 找到 span 起点，且它 < cut → 该 span 被切开
		}
	}
	return false
}

// splitSpanPrefix 返回被切开的 user span 中属于"前缀"的那部分消息（span 起点到 cut）。
func splitSpanPrefix(msgs []Message, cut int) []Message {
	start := -1
	for i := cut - 1; i >= 0; i-- {
		if msgs[i].Role == RoleUser && msgs[i].Kind != KindToolResult {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	return msgs[start:cut]
}

// BuildSummaryInput 构造摘要模型的输入正文：序列化对话 + 累计文件清单。
// 由 LLM 层（llm.SummarizeMessages）调用，保证序列化与文件追踪只有一处实现。
func BuildSummaryInput(msgs []Message) string {
	var sb strings.Builder
	sb.WriteString(SummaryPrompt)
	sb.WriteString("\n\n--- conversation ---\n")
	sb.WriteString(serializeConversation(msgs))
	reads, modified := collectFileTracking(msgs)
	if track := renderFileTracking(reads, modified); track != "" {
		sb.WriteString("\n--- files touched ---\n")
		sb.WriteString(track)
	}
	return sb.String()
}

// SummaryPrompt 生成摘要提示词（供 LLM 调用方使用）。
// 参照 pi 的 SUMMARIZATION_PROMPT 结构化格式。
const SummaryPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish?]

## Constraints & Preferences
- [Requirements, constraints, or preferences the user stated — exact wording when it matters]
- [Or "(none)" if the user stated none]

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
