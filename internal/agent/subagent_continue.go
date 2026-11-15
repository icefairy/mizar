// 子代理对话留档与续跑（P1，2026-11-15，对齐 hermes delegate_tool action=continue）。
//
// 问题：子代理一旦终态就彻底结束——句柄里只有 Result 摘要，拿不到它「怎么走到
// 这个结论」的对话历史，也就无法追加一轮（"把刚改的地方补测试"）。
//
// 方案（分两块）：
//  1. **留档**：child.OnTranscript 在每次 Run 结束时把消息流交给壳里，存进句柄
//     Transcript；快照落盘时做裁剪（见 transcriptForPersist）——工具结果动辄几十
//     KB，全存会把会话 JSONL 撑爆。
//  2. **续跑**：delegate_task(action=continue) 用句柄里的 Transcript 重建 child，
//     把 message 作为新的一轮 user 消息追加进去，沿用原 Request 的 role/model/
//     working_dir/工具白名单——子代理因此「记得」上次做了什么。
package agent

import (
	"fmt"
	"strings"
)

const (
	// transcriptMaxMessages 留档最多保留的消息条数（超出保留最近的）。
	transcriptMaxMessages = 40
	// transcriptMaxToolResultChars 单条工具结果在留档里的最大字符数。
	// 完整输出已落在会话/日志里，这里只需要「做过什么」的线索。
	transcriptMaxToolResultChars = 600
	// transcriptMaxTotalChars 整个留档的字符预算（防止极端情况）。
	transcriptMaxTotalChars = 32000
)

// transcriptForPersist 裁剪对话历史用于落盘：
//   - 丢掉 system 前缀（子代理重建时由 child.System 提供）
//   - 工具结果截断到 transcriptMaxToolResultChars（保留头部，够看「跑的是什么」）
//   - 只保留最近 transcriptMaxMessages 条
//   - 总字符超预算时从最老的开始丢（但保证最近一条还在）
//
// 返回 nil 表示无需留档（空对话）。
func transcriptForPersist(msgs []Message) []Message {
	if len(msgs) == 0 {
		return nil
	}
	// 过滤 system（重建时会重新注入）
	filtered := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleSystem {
			continue
		}
		if m.Kind == KindToolResult && len(m.Content) > transcriptMaxToolResultChars {
			m.Content = m.Content[:transcriptMaxToolResultChars] + "…（截断，完整输出见原会话）"
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return nil
	}
	// 只留最近 N 条
	if len(filtered) > transcriptMaxMessages {
		filtered = filtered[len(filtered)-transcriptMaxMessages:]
	}
	// 总预算：从尾部往前累计，超预算就从头丢
	total := 0
	keepFrom := 0
	for i := len(filtered) - 1; i >= 0; i-- {
		n := len(filtered[i].Content) + len(filtered[i].ToolArgs)
		if total+n > transcriptMaxTotalChars && i < len(filtered)-1 {
			keepFrom = i + 1
			break
		}
		total += n
	}
	if keepFrom > 0 {
		filtered = filtered[keepFrom:]
	}
	// 深拷贝一份（避免与活消息共享切片）
	out := make([]Message, len(filtered))
	copy(out, filtered)
	return out
}

// CanContinue 报告该句柄是否允许续跑。
// 只有终态（完成/失败/取消）且留有对话历史的才能续。
func (h *SubagentHandle) CanContinue() bool {
	if h == nil {
		return false
	}
	switch h.State {
	case SubagentSucceeded, SubagentFailed, SubagentCancelled:
	default:
		return false // 运行中请用 steer_subagent
	}
	return len(h.Transcript) > 0
}

// Continue 给已终态的子代理追加一轮（P1）。
//
// 语义（对齐 hermes）：
//   - 沿用原 Request 的 role / model / working_dir / 工具白名单（route 不变）
//   - 历史从句柄 Transcript 加载（transcript 追加，子代理「记得」上一轮）
//   - message 成为它的下一轮 user 消息
//   - 保持同一个 subagent_id（新结果仍走正常完成回灌）
//
// 拒绝情况：运行中（请用 steer）、无留档、深度超限、goal 为空。
func (sm *SubagentManager) Continue(id, message string) (*SubagentHandle, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("subagent continue: message 不能为空")
	}
	sm.mu.RLock()
	h, ok := sm.subs[id]
	var snap SubagentHandle
	if ok {
		snap = *h
	}
	sm.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("subagent continue: 句柄 %q 不存在（list_subagents 查看）", id)
	}
	if snap.State == SubagentRunning || snap.State == SubagentPending {
		return nil, fmt.Errorf("subagent %s 仍在运行，请用 steer_subagent 注入纠正", id)
	}
	if len(snap.Transcript) == 0 {
		return nil, fmt.Errorf("subagent %s 没有对话留档，无法续跑（可用 delegate_task 重新派发）", id)
	}

	req := snap.Request
	if strings.TrimSpace(req.Goal) == "" {
		req.Goal = snap.Goal
	}
	// 新任务目标 = 上一轮目标摘要 + 本轮追加要求（提示里说清楚，模型才好接）
	req.Goal = fmt.Sprintf("（续跑自子任务 %s）上一轮目标：%s\n\n本轮追加要求：%s", id, snap.Goal, message)
	req.Context = ""
	if req.TimeoutSecs <= 0 {
		req.TimeoutSecs = defaultTimeoutSecs
	}
	if req.MaxSteps <= 0 {
		req.MaxSteps = sm.maxSteps
	}

	// 复用原 id（保持可寻址），状态回到 PENDING 再跑
	sm.mu.Lock()
	h2, still := sm.subs[id]
	if !still {
		sm.mu.Unlock()
		return nil, fmt.Errorf("subagent %s 已被清理，无法续跑", id)
	}
	h2.State = SubagentPending
	h2.Error = ""
	h2.Continues++
	baseTranscript := append([]Message(nil), h2.Transcript...)
	sm.mu.Unlock()

	go sm.runSubagentWithTranscript(h2, req, baseTranscript)
	return h2, nil
}

// runSubagentWithTranscript 与 runSubagent 相同，但用给定历史作为 child.Initial。
func (sm *SubagentManager) runSubagentWithTranscript(h *SubagentHandle, req SubagentLaunchRequest, transcript []Message) {
	sm.runSubagentInner(h, req, transcript)
}
