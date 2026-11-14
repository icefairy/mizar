// 子任务句柄落盘与回放（GAP-S4）。
//
// 问题：SubagentManager 的 subs map 纯内存，进程重启后 get_subagent /
// list_subagents 全部 "not found"，长任务的中间结论无法跨会话引用。
//
// 方案：照抄 todo 的「状态内嵌回放」模式（internal/builtins/todo_pi.go）——
// 终态句柄快照序列化成 <!--subagent-state:{json}--> 标记消息，随会话 JSONL
// 追加落盘；会话恢复时按历史顺序回放，每个 id 取最后一次出现的快照重建。
//
// 与 todo 的差异：todo 的标记内嵌在真实工具结果里；子代理没有天然载体，
// 用独立的 assistant 标记消息承载。这类消息在恢复时从 LLM 上下文中剔除
// （StripSubagentStateMessages），只服务于状态重建——不进模型视野、不占
// token，也永远不会被模型模仿输出。会话文件里保留原样（append-only）。
package agent

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const (
	subagentStatePrefix = "<!--subagent-state:"
	subagentStateSuffix = "-->"
)

// SubagentStateMessage 把终态句柄快照编码成落盘消息（main.go 经 session.Store.Append 持久化）。
func SubagentStateMessage(s SubagentHandle) Message {
	b, err := json.Marshal(s)
	if err != nil {
		return Message{}
	}
	return Message{Role: RoleAssistant, Content: subagentStatePrefix + string(b) + subagentStateSuffix}
}

// SubagentSnapshotsFromMessages 从会话历史消息里回放出子代理快照：
// 同一 id 取最后一次出现的快照（与 pi reconstructState 语义一致），
// 按创建时间排序返回。
func SubagentSnapshotsFromMessages(msgs []Message) []SubagentHandle {
	latest := map[string]SubagentHandle{}
	for _, m := range msgs {
		s, ok := parseSubagentStateMessage(m)
		if !ok {
			continue
		}
		latest[s.ID] = s
	}
	out := make([]SubagentHandle, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// StripSubagentStateMessages 剔除标记消息（恢复会话时用于 LLM 上下文与 TUI 渲染：
// 标记只服务于状态重建，不应进入模型视野）。
func StripSubagentStateMessages(msgs []Message) []Message {
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, subagentStatePrefix) {
			found = true
			break
		}
	}
	if !found {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if strings.Contains(m.Content, subagentStatePrefix) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func parseSubagentStateMessage(m Message) (SubagentHandle, bool) {
	if m.Role != RoleAssistant {
		return SubagentHandle{}, false
	}
	i := strings.Index(m.Content, subagentStatePrefix)
	if i < 0 {
		return SubagentHandle{}, false
	}
	start := i + len(subagentStatePrefix)
	end := strings.Index(m.Content[start:], subagentStateSuffix)
	if end < 0 {
		return SubagentHandle{}, false
	}
	var s SubagentHandle
	if err := json.Unmarshal([]byte(m.Content[start:start+end]), &s); err != nil || s.ID == "" {
		return SubagentHandle{}, false
	}
	return s, true
}

// RestoreFromMessages 从会话历史消息回放并恢复子代理句柄（main.go 接线用）。
func (sm *SubagentManager) RestoreFromMessages(msgs []Message) {
	sm.Restore(SubagentSnapshotsFromMessages(msgs))
}

// Restore 把回放出的快照合并进 subs map（只补缺，不覆盖内存中的活句柄）。
// 快照里仍是 PENDING/RUNNING 的（写入后进程崩溃，没等到终态落盘）标记为
// CANCELLED「进程重启中断」——它们不可能还在跑，挂运行态会误导查询方。
// 同时把 ID 计数器推到快照之后，避免新派任务与恢复句柄撞 id。
func (sm *SubagentManager) Restore(snaps []SubagentHandle) {
	if len(snaps) == 0 {
		return
	}
	sm.mu.Lock()
	maxNum := 0
	for _, s := range snaps {
		if _, exists := sm.subs[s.ID]; exists {
			continue
		}
		if s.State == SubagentPending || s.State == SubagentRunning {
			s.State = SubagentCancelled
			s.Error = "进程重启，任务中断"
		}
		sh := s
		sm.subs[s.ID] = &sh
		if n := subagentIDCounter(s.ID); n > maxNum {
			maxNum = n
		}
	}
	if maxNum > 0 {
		sm.nextID.Store(int64(maxNum))
	}
	sm.mu.Unlock()
}

// subagentIDCounter 从 "sub-<n>-<i>" 取出计数器 n（ID 的第一段）。
// 解析失败返回 0（容错：不影响其余快照恢复）。
func subagentIDCounter(id string) int {
	rest := strings.TrimPrefix(id, "sub-")
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 0 {
		return 0
	}
	return n
}
