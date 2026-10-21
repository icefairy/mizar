package agent

import (
	"strings"
	"testing"
	"time"
)

// 时间感知：历史消息隔了很久再继续时，模型必须能意识到时间已流逝。

func TestHistoryTimeNoticeShortGapSilent(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	msgs := []Message{
		{Role: RoleUser, Content: "hi", Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339)},
	}
	if got := HistoryTimeNotice(msgs, now); got != "" {
		t.Fatalf("短间隔不应提示，got %q", got)
	}
}

func TestHistoryTimeNoticeLongGap(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	msgs := []Message{
		{Role: RoleUser, Content: "hi", Timestamp: now.Add(-25 * time.Hour).Format(time.RFC3339)},
	}
	got := HistoryTimeNotice(msgs, now)
	if got == "" {
		t.Fatal("隔一天应提示")
	}
	if !strings.Contains(got, "1 天") {
		t.Fatalf("应包含间隔时长，got %q", got)
	}
	if !strings.Contains(got, "请勿假定上下文仍然连贯") {
		t.Fatalf("应明确提示不要假定连贯，got %q", got)
	}
}

func TestHistoryTimeNoticeSkipsUntimestampedTail(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	// 尾部消息无时间戳（旧数据）→ 向前找最近一条带时间戳的
	msgs := []Message{
		{Role: RoleUser, Content: "old", Timestamp: now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)},
		{Role: RoleAssistant, Content: "reply"},
	}
	got := HistoryTimeNotice(msgs, now)
	if !strings.Contains(got, "3 天") {
		t.Fatalf("应跳过无时间戳的尾部消息，got %q", got)
	}
}

func TestHistoryTimeNoticeNoTimestamp(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	if got := HistoryTimeNotice(msgs, time.Now()); got != "" {
		t.Fatalf("无时间戳不应编造提示，got %q", got)
	}
}

func TestTimeContext(t *testing.T) {
	ts := time.Date(2026, 10, 21, 15, 4, 5, 0, time.Local).Format(time.RFC3339)
	got := TimeContext(ts)
	if !strings.Contains(got, "2026-10-21 15:04:05") || !strings.Contains(got, "周三") {
		t.Fatalf("时间说明不完整: %q", got)
	}
	if TimeContext("") != "" || TimeContext("not-a-time") != "" {
		t.Fatal("非法时间戳应返回空串")
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "不到 1 分钟"},
		{5 * time.Minute, "5 分钟"},
		{2*time.Hour + 30*time.Minute, "2 小时"},
		{25 * time.Hour, "1 天 1 小时"},
		{10 * 24 * time.Hour, "10 天"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestRunInjectsTimeContext 验证 Run 每轮都注入当前时间，且长间隔时追加时间提示。
func TestRunInjectsTimeContext(t *testing.T) {
	var got []Message
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		got = append([]Message(nil), msgs...)
		return `{"action":"reply","text":"ok"}`, nil
	}}
	a := New(llm, testManager(t))
	a.Hooks = NewHooks()

	old := TimeNow
	defer func() { TimeNow = old }()
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	TimeNow = func() time.Time { return now }

	// 历史最后一条在 2 天前
	a.Initial = []Message{
		{Role: RoleUser, Content: "之前的问题", Timestamp: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{Role: RoleAssistant, Content: "之前的回答", Timestamp: now.Add(-48 * time.Hour).Format(time.RFC3339)},
	}
	if _, err := a.Run("继续"); err != nil {
		t.Fatal(err)
	}

	var hasTimeCtx, hasGapNotice bool
	for _, m := range got {
		if m.Role == RoleSystem && strings.Contains(m.Content, "当前实时时间") {
			hasTimeCtx = true
		}
		if strings.Contains(m.Content, "【时间提示】") {
			hasGapNotice = true
		}
	}
	if !hasTimeCtx {
		t.Fatalf("缺少当前时间注入: %+v", got)
	}
	if !hasGapNotice {
		t.Fatalf("缺少时间间隔提示: %+v", got)
	}
	// 注入必须发生在历史之前（提示紧跟在 system 之后，历史保持原序）
	if len(got) < 5 {
		t.Fatalf("消息数异常: %d", len(got))
	}
	if !strings.Contains(got[2].Content, "【时间提示】") {
		t.Fatalf("时间提示位置不对: idx2=%q", got[2].Content)
	}
}

// TestRunTimeContextDoesNotPolluteInitial 注入不得写回 a.Initial（否则每轮累积）。
func TestRunTimeContextDoesNotPolluteInitial(t *testing.T) {
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"ok"}`, nil
	}}
	a := New(llm, testManager(t))
	a.Hooks = NewHooks()
	before := len(a.Initial)
	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
	if len(a.Initial) != before {
		t.Fatalf("Initial 被污染: %d → %d", before, len(a.Initial))
	}
}
