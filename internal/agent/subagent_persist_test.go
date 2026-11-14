package agent

import (
	"strings"
	"testing"
	"time"
)

// GAP-S4 子任务句柄落盘与回放。

func TestSubagentStateMessageRoundTrip(t *testing.T) {
	snap := SubagentHandle{
		ID: "sub-7-1", Goal: "调研方案", State: SubagentSucceeded,
		CreatedAt: time.Now(), Depth: 1, Model: "glm-4", Result: "结论：选方案B",
		Progress: []string{"step 1: read file", "step 2: bash grep"},
	}
	m := SubagentStateMessage(snap)
	if m.Role != RoleAssistant {
		t.Fatalf("role = %q", m.Role)
	}
	parsed, ok := parseSubagentStateMessage(m)
	if !ok {
		t.Fatalf("parse failed: %q", m.Content)
	}
	if parsed.ID != snap.ID || parsed.Goal != snap.Goal || parsed.State != snap.State ||
		parsed.Result != snap.Result || parsed.Model != snap.Model || len(parsed.Progress) != 2 {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	// 快照列表提取：多条消息按出现顺序回放，每个 id 取最后一次出现的快照
	msgs := []Message{
		SubagentStateMessage(SubagentHandle{ID: "sub-7-1", Goal: "旧快照", State: SubagentRunning, CreatedAt: snap.CreatedAt.Add(-time.Second)}),
		m,
		SubagentStateMessage(SubagentHandle{ID: "sub-7-2", Goal: "另一个", State: SubagentFailed, Error: "boom", CreatedAt: snap.CreatedAt.Add(time.Second)}),
		{Role: RoleUser, Content: "普通消息 <!--subagent-state:{\"id\":\"fake\"}--> 不应解析（role 不符）"},
	}
	snaps := SubagentSnapshotsFromMessages(msgs)
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %+v", len(snaps), snaps)
	}
	// 排序按 CreatedAt：sub-7-1（真快照）在前
	if snaps[0].ID != "sub-7-1" || snaps[0].Result != "结论：选方案B" {
		t.Fatalf("latest snapshot 应胜出: %+v", snaps[0])
	}
	if snaps[1].ID != "sub-7-2" || snaps[1].Error != "boom" {
		t.Fatalf("second snapshot wrong: %+v", snaps[1])
	}
}

func TestStripSubagentStateMessages(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "问题"},
		SubagentStateMessage(SubagentHandle{ID: "sub-1-1", State: SubagentSucceeded}),
		{Role: RoleAssistant, Content: "回答"},
	}
	out := StripSubagentStateMessages(msgs)
	if len(out) != 2 || out[0].Content != "问题" || out[1].Content != "回答" {
		t.Fatalf("strip failed: %+v", out)
	}
	// 无标记时原样返回（零拷贝路径）
	plain := []Message{{Role: RoleUser, Content: "hi"}}
	if got := StripSubagentStateMessages(plain); len(got) != 1 {
		t.Fatalf("plain strip failed")
	}
}

func TestSubagentPersisterOnTerminal(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{})
	var persisted []SubagentHandle
	sm.SetPersister(func(s SubagentHandle) { persisted = append(persisted, s) })

	h, err := sm.Launch(SubagentLaunchRequest{Goal: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// 非终态不落盘
	sm.setState(h.ID, SubagentRunning, "", "")
	if len(persisted) != 0 {
		t.Fatalf("RUNNING 不应落盘: %+v", persisted)
	}
	sm.setState(h.ID, SubagentSucceeded, "结果内容", "")
	if len(persisted) != 1 || persisted[0].ID != h.ID || persisted[0].Result != "结果内容" || persisted[0].State != SubagentSucceeded {
		t.Fatalf("终态未落盘: %+v", persisted)
	}
	// Cancel 路径也落盘
	h2, _ := sm.Launch(SubagentLaunchRequest{Goal: "y"})
	if err := sm.Cancel(h2.ID); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 || persisted[1].State != SubagentCancelled {
		t.Fatalf("cancel 未落盘: %+v", persisted)
	}
	// 关闭 persister 后不再落盘
	sm.SetPersister(nil)
	h3, _ := sm.Launch(SubagentLaunchRequest{Goal: "z"})
	sm.setState(h3.ID, SubagentFailed, "", "e")
	if len(persisted) != 2 {
		t.Fatalf("nil persister 不应落盘")
	}
}

func TestSubagentRestore(t *testing.T) {
	now := time.Now()
	snaps := []SubagentHandle{
		{ID: "sub-5-1", Goal: "已完成", State: SubagentSucceeded, Result: "R1", CreatedAt: now.Add(-2 * time.Second)},
		{ID: "sub-5-2", Goal: "崩溃时还在跑", State: SubagentRunning, CreatedAt: now.Add(-time.Second)},
		{ID: "sub-5-3", Goal: "排队中崩溃", State: SubagentPending, CreatedAt: now},
	}
	sm := NewSubagentManager(SubagentConfig{})
	sm.Restore(snaps)

	if h, ok := sm.Get("sub-5-1"); !ok || h.State != SubagentSucceeded || h.Result != "R1" {
		t.Fatalf("终态快照未恢复: %+v %+v", h, ok)
	}
	// RUNNING/PENDING 恢复为 CANCELLED（进程重启中断）
	if h, ok := sm.Get("sub-5-2"); !ok || h.State != SubagentCancelled || !strings.Contains(h.Error, "进程重启") {
		t.Fatalf("RUNNING 快照应标记中断: %+v", h)
	}
	if h, ok := sm.Get("sub-5-3"); !ok || h.State != SubagentCancelled {
		t.Fatalf("PENDING 快照应标记中断: %+v", h)
	}
	// 恢复句柄不覆盖内存活句柄
	live, _ := sm.Launch(SubagentLaunchRequest{Goal: "活任务"})
	sm.setState(live.ID, SubagentSucceeded, "live-result", "")
	sm.Restore([]SubagentHandle{{ID: live.ID, State: SubagentFailed, Error: "旧快照"}})
	if h, _ := sm.Get(live.ID); h.State != SubagentSucceeded || h.Result != "live-result" {
		t.Fatalf("活句柄被恢复覆盖: %+v", h)
	}
	// id 计数器已推进：新派任务不与恢复句柄撞 id
	h, err := sm.Launch(SubagentLaunchRequest{Goal: "新任务"})
	if err != nil {
		t.Fatal(err)
	}
	if h.ID == "sub-5-1" || h.ID == "sub-5-2" || h.ID == "sub-5-3" || h.ID == live.ID {
		t.Fatalf("新句柄 id 与恢复句柄冲突: %s", h.ID)
	}
}
