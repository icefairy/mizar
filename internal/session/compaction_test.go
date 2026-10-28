package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
)

// TestAppendCompactionAndLoad 压缩记录落盘并能在加载时重建上下文。
//
// 对齐 pi 的 CompactionEntry 设计：压缩是「追加一条记录」，原始消息一行不删，
// 加载时若存在压缩记录，则重建为 summary + 保留范围，避免跨进程丢失压缩成果。
func TestAppendCompactionAndLoad(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)

	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "第一条用户消息"},
		{Role: agent.RoleAssistant, Content: "第一条助手回复"},
		{Role: agent.RoleUser, Content: "第二条用户消息"},
		{Role: agent.RoleAssistant, Content: "第二条助手回复"},
	}
	for _, m := range msgs {
		if err := st.Append("s1", m); err != nil {
			t.Fatal(err)
		}
	}
	// 压缩前 2 条（索引 0、1），保留索引 2 起的消息
	if err := st.AppendCompaction("s1", "这是前两条的摘要", 2, 1200); err != nil {
		t.Fatal(err)
	}
	// 压缩后继续工作
	if err := st.Append("s1", agent.Message{Role: agent.RoleUser, Content: "压缩后的新消息"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load("s1")
	if err != nil {
		t.Fatal(err)
	}

	// 期望：[summary] + 保留的 2、3 条 + 压缩后的新消息
	if len(got) != 4 {
		t.Fatalf("加载 %d 条，期望 4 条: %+v", len(got), got)
	}
	if got[0].Kind != agent.KindSummary || !strings.Contains(got[0].Content, "前两条的摘要") {
		t.Errorf("首条应为摘要，实际: kind=%q content=%q", got[0].Kind, got[0].Content)
	}
	if got[1].Content != "第二条用户消息" {
		t.Errorf("保留范围起点错误: %q", got[1].Content)
	}
	if got[3].Content != "压缩后的新消息" {
		t.Errorf("压缩后追加的消息丢失: %q", got[3].Content)
	}
}

// TestLoadRawKeepsAllMessages 原始消息一行不删（压缩不改写历史）。
func TestLoadRawKeepsAllMessages(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	for i, c := range []string{"a", "b", "c", "d"} {
		if err := st.Append("s2", agent.Message{Role: agent.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	if err := st.AppendCompaction("s2", "摘要", 2, 500); err != nil {
		t.Fatal(err)
	}

	raw, err := st.LoadRaw("s2")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 4 {
		t.Fatalf("原始消息应保留 4 条，实际 %d", len(raw))
	}

	b, err := os.ReadFile(filepath.Join(dir, "s2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// 文件里 4 条消息 + 1 条压缩记录 = 5 行
	if n := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; n != 5 {
		t.Errorf("文件应有 5 行（4 消息 + 1 压缩记录），实际 %d\n%s", n, b)
	}
}

// TestLoadLegacySessionWithoutCompaction 旧会话（无压缩记录）按原样加载，向后兼容。
func TestLoadLegacySessionWithoutCompaction(t *testing.T) {
	dir := t.TempDir()
	// 手工写一个旧格式文件（裸 Message，无 type 字段）
	legacy := `{"role":"user","content":"旧消息一"}
{"role":"assistant","content":"旧回复一"}
`
	if err := os.WriteFile(filepath.Join(dir, "legacy.jsonl"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	st := New(dir)
	got, err := st.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("旧格式应加载 2 条，实际 %d", len(got))
	}
	if got[0].Content != "旧消息一" || got[1].Content != "旧回复一" {
		t.Errorf("旧格式内容错误: %+v", got)
	}
}

// TestMultipleCompactionsUseLatestCompaction 多次压缩只取最新一条生效。
func TestMultipleCompactionsUseLatestCompaction(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	for _, c := range []string{"m0", "m1", "m2", "m3", "m4", "m5"} {
		if err := st.Append("s3", agent.Message{Role: agent.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	// 第一次压缩：摘要 m0,m1，保留 m2 起
	if err := st.AppendCompaction("s3", "第一次摘要", 2, 100); err != nil {
		t.Fatal(err)
	}
	// 第二次压缩：摘要到 m3，保留 m4 起
	if err := st.AppendCompaction("s3", "第二次摘要", 4, 90); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load("s3")
	if err != nil {
		t.Fatal(err)
	}
	// 期望：第二次摘要 + m4 + m5
	if len(got) != 3 {
		t.Fatalf("期望 3 条（最新摘要 + m4 + m5），实际 %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Content, "第二次摘要") {
		t.Errorf("应取最新摘要，实际 %q", got[0].Content)
	}
	if got[1].Content != "m4" || got[2].Content != "m5" {
		t.Errorf("保留范围错误: %q %q", got[1].Content, got[2].Content)
	}
}

// TestCompactionChainPreservesSurvivors 链式压缩不丢上次幸存的消息。
//
// 对齐 pi：下次摘要起点是「上次的保留边界」，因此上次幸存消息也进入下次摘要，
// 最终结果里不应出现断层。
func TestCompactionChainPreservesSurvivors(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	for _, c := range []string{"a0", "a1", "a2", "a3"} {
		if err := st.Append("s4", agent.Message{Role: agent.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendCompaction("s4", "摘要1", 2, 100); err != nil {
		t.Fatal(err)
	}
	// 模拟第二次压缩：摘要范围包含上次保留的 a2，保留 a3 起
	if err := st.AppendCompaction("s4", "摘要2", 3, 80); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("s4")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(got[0].Content, "摘要2") || got[1].Content != "a3" {
		t.Fatalf("链式压缩结果错误: %+v", got)
	}
}

// TestCompactionRetainNone retain-none：保留边界等于消息总数时不保留任何前置消息。
func TestCompactionRetainNone(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	for _, c := range []string{"x0", "x1"} {
		if err := st.Append("s5", agent.Message{Role: agent.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendCompaction("s5", "全量摘要", 2, 50); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("s5")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != agent.KindSummary {
		t.Fatalf("retain-none 应只剩摘要，实际: %+v", got)
	}
}

// TestAppendEmergencyTrim 自愈裁剪记录：无摘要，只保留 firstKeptIndex 起（含之前追加的消息）。
func TestAppendEmergencyTrim(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	for _, c := range []string{"e0", "e1", "e2", "e3"} {
		if err := st.Append("s6", agent.Message{Role: agent.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	// 应急裁剪：丢掉前 3 条，从索引 3 开始保留
	if err := st.AppendEmergencyTrim("s6", 3, 9999, 60); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("s6")
	if err != nil {
		t.Fatal(err)
	}
	// 无摘要 → 只有保留的 1 条
	if len(got) != 1 || got[0].Content != "e3" {
		t.Fatalf("应急裁剪结果错误: %+v", got)
	}
	// 原始消息仍完整
	raw, err := st.LoadRaw("s6")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 4 {
		t.Errorf("原始消息应保留 4 条，实际 %d", len(raw))
	}
}
