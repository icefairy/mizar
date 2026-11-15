package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTranscriptForPersist(t *testing.T) {
	// system 被过滤
	msgs := []Message{
		{Role: RoleSystem, Content: "system prompt"},
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, Content: "reply"},
	}
	out := transcriptForPersist(msgs)
	if len(out) != 2 {
		t.Fatalf("应过滤 system，got %d", len(out))
	}
	for _, m := range out {
		if m.Role == RoleSystem {
			t.Error("system 不应留档")
		}
	}
}

func TestTranscriptTruncatesToolResult(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	out := transcriptForPersist([]Message{
		{Role: RoleUser, Kind: KindToolResult, Content: huge},
	})
	if len(out) != 1 {
		t.Fatal("应保留 1 条")
	}
	if len(out[0].Content) > transcriptMaxToolResultChars+100 {
		t.Errorf("工具结果应截断，got %d", len(out[0].Content))
	}
	if !strings.Contains(out[0].Content, "截断") {
		t.Error("应标注已截断")
	}
}

func TestTranscriptKeepsRecent(t *testing.T) {
	var msgs []Message
	for i := 0; i < transcriptMaxMessages+20; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: fmt.Sprintf("m%d", i)})
	}
	out := transcriptForPersist(msgs)
	if len(out) > transcriptMaxMessages {
		t.Errorf("应只留最近 %d 条，got %d", transcriptMaxMessages, len(out))
	}
	// 最后一条必须还在
	if !strings.Contains(out[len(out)-1].Content, fmt.Sprintf("m%d", transcriptMaxMessages+19)) {
		t.Error("最近的应保留")
	}
}

func TestTranscriptEmpty(t *testing.T) {
	if out := transcriptForPersist(nil); out != nil {
		t.Error("空输入应返回 nil")
	}
	if out := transcriptForPersist([]Message{{Role: RoleSystem, Content: "s"}}); out != nil {
		t.Error("只有 system 应返回 nil")
	}
}

func TestHandleCanContinue(t *testing.T) {
	cases := []struct {
		state SubagentState
		trans []Message
		want  bool
	}{
		{SubagentSucceeded, []Message{{Role: RoleUser, Content: "x"}}, true},
		{SubagentFailed, []Message{{Role: RoleUser, Content: "x"}}, true},
		{SubagentCancelled, []Message{{Role: RoleUser, Content: "x"}}, true},
		{SubagentRunning, []Message{{Role: RoleUser, Content: "x"}}, false},
		{SubagentPending, []Message{{Role: RoleUser, Content: "x"}}, false},
		{SubagentSucceeded, nil, false},
	}
	for _, c := range cases {
		h := &SubagentHandle{State: c.state, Transcript: c.trans}
		if got := h.CanContinue(); got != c.want {
			t.Errorf("state=%s trans=%d → %v want %v", c.state, len(c.trans), got, c.want)
		}
	}
}

func TestContinueRejectsRunning(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{ParentLLM: &scriptLLM{}, Plugins: testManager(t)})
	h := &SubagentHandle{ID: "sub-1-1", State: SubagentRunning, Transcript: []Message{{Role: RoleUser, Content: "x"}}}
	sm.register(h)
	_, err := sm.Continue("sub-1-1", "再来一轮")
	if err == nil {
		t.Fatal("运行中的子代理应拒绝 continue（应用 steer）")
	}
	if !strings.Contains(err.Error(), "steer") {
		t.Errorf("错误信息应提示改用 steer，got %v", err)
	}
}

func TestContinueRejectsUnknownAndNoTranscript(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{ParentLLM: &scriptLLM{}, Plugins: testManager(t)})
	if _, err := sm.Continue("nope", "x"); err == nil {
		t.Error("不存在的 id 应报错")
	}
	h := &SubagentHandle{ID: "sub-2-1", State: SubagentSucceeded}
	sm.register(h)
	if _, err := sm.Continue("sub-2-1", "x"); err == nil || !strings.Contains(err.Error(), "留档") {
		t.Errorf("无留档应报错，got %v", err)
	}
	if _, err := sm.Continue("sub-2-1", "   "); err == nil {
		t.Error("空 message 应报错")
	}
}

// TestContinueStartsAgain 续跑应重置状态并累加 Continues。
func TestContinueStartsAgain(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{ParentLLM: &scriptLLM{}, Plugins: testManager(t)})
	h := &SubagentHandle{
		ID: "sub-3-1", Goal: "改代码", State: SubagentSucceeded,
		Transcript: []Message{{Role: RoleUser, Content: "改代码"}, {Role: RoleAssistant, Content: "改好了"}},
		Request:    SubagentLaunchRequest{Goal: "改代码", Role: "implement", TimeoutSecs: 5, MaxSteps: 2},
	}
	sm.register(h)
	out, err := sm.Continue("sub-3-1", "补测试")
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if out.ID != "sub-3-1" {
		t.Errorf("应复用原 id，got %s", out.ID)
	}
	if out.Continues != 1 {
		t.Errorf("Continues 应 1，got %d", out.Continues)
	}
	// 状态已被 runSubagentInner 推进（PENDING→RUNNING 或已 FAILED，取决于 LLM mock）
	time.Sleep(200 * time.Millisecond)
	got, _ := sm.Get("sub-3-1")
	if got == nil {
		t.Fatal("句柄应仍可查")
	}
	if got.State == SubagentSucceeded && got.Continues != 1 {
		t.Error("续跑后 Continues 应保留")
	}
}

// TestDelegateToolContinueArgs delegate_task 的 continue 分支参数解析。
func TestDelegateToolContinueArgs(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{ParentLLM: &scriptLLM{}, Plugins: testManager(t)})
	tool := DelegateTool(sm)
	// 缺 id 应报错
	if _, err := tool.Run(`{"action":"continue","message":"x"}`); err == nil {
		t.Error("continue 缺 id 应报错")
	}
	// 不存在的 id
	if _, err := tool.Run(`{"action":"continue","id":"nope","message":"x"}`); err == nil {
		t.Error("不存在的 id 应报错")
	}
	// 正常 goal 派发仍工作
	out, err := tool.Run(`{"goal":"测试任务","max_steps":1}`)
	if err != nil {
		t.Fatalf("普通派发失败: %v", err)
	}
	if !strings.Contains(out, "sub-") {
		t.Errorf("应返回句柄: %s", out)
	}
}
