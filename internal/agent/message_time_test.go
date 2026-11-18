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
		if strings.Contains(m.Content, "当前实时时间") {
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
	// 时间戳必须只出现在最后一条（本轮 user 消息尾部）——放进 system 或历史之前
	// 会把它之后所有 token 的前缀缓存作废，等于每轮全量重算。
	if len(got) < 5 {
		t.Fatalf("消息数异常: %d", len(got))
	}
	for i, m := range got[:len(got)-1] {
		if strings.Contains(m.Content, "当前实时时间") {
			t.Fatalf("时间戳出现在第 %d 条（%s），必须在最后一条 user 消息里: %q", i, m.Role, m.Content[:80])
		}
	}
	if !strings.Contains(got[len(got)-1].Content, "当前实时时间") {
		t.Fatalf("最后一条不是带时间的 user 消息: %q", got[len(got)-1].Content)
	}
	if got[len(got)-1].Role != RoleUser {
		t.Fatalf("带时间的消息角色 = %s，应为 user", got[len(got)-1].Role)
	}
	// 间隔提示仍在历史之前（system 之后紧邻）
	if !strings.Contains(got[1].Content, "【时间提示】") {
		t.Fatalf("时间提示位置不对: idx1=%q", got[1].Content)
	}
}

// 前缀缓存不变式：两轮之间（除最后一条 user 消息外）消息必须逐字节相同。
// 时间戳每秒都变，只要它漏进 system/历史，缓存命中率就归零。
func TestRunKeepsCacheablePrefixStable(t *testing.T) {
	var runs [][]Message
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		runs = append(runs, append([]Message(nil), msgs...))
		return `{"action":"reply","text":"ok"}`, nil
	}}
	a := New(llm, testManager(t))
	a.Hooks = NewHooks()

	old := TimeNow
	defer func() { TimeNow = old }()
	oldTS := nowTimestamp
	defer func() { nowTimestamp = oldTS }()
	t1 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.Local)
	TimeNow = func() time.Time { return t1 }
	nowTimestamp = func() string { return t1.Format(time.RFC3339) }

	if _, err := a.Run("第一轮"); err != nil {
		t.Fatal(err)
	}
	// 模拟第二轮：把上一轮结果塞回历史，时间前进 1 分钟
	if len(runs) == 0 {
		t.Fatal("没有捕获到 LLM 请求")
	}
	a.Initial = []Message{
		{Role: RoleUser, Content: "第一轮"},
		{Role: RoleAssistant, Content: "ok"},
	}
	TimeNow = func() time.Time { return t1.Add(time.Minute) }
	nowTimestamp = func() string { return t1.Add(time.Minute).Format(time.RFC3339) }
	if _, err := a.Run("第二轮"); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("捕获请求数 = %d，应为 2", len(runs))
	}
	// 第二轮的「system + 历史」前缀必须与第一轮完全一致，逐条比对
	n := len(runs[0]) - 1 // 去掉第一轮最后那条带时间的 user 消息
	for i := 0; i < n; i++ {
		if i >= len(runs[1]) {
			t.Fatalf("第二轮消息数偏少: %d vs %d", len(runs[1]), len(runs[0]))
		}
		if runs[0][i].Role != runs[1][i].Role || runs[0][i].Content != runs[1][i].Content {
			t.Fatalf("第 %d 条前缀发生变化（缓存会失效）：\n第一轮 %.80q\n第二轮 %.80q",
				i, runs[0][i].Content, runs[1][i].Content)
		}
	}
	// 时间戳各不相同，且都在尾部
	last0 := runs[0][len(runs[0])-1].Content
	last1 := runs[1][len(runs[1])-1].Content
	if !strings.Contains(last0, "10:00:00") || !strings.Contains(last1, "10:01:00") {
		t.Fatalf("尾部时间不对：%q / %q", last0, last1)
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
