package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
)

func TestDeriveTitle(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "你是 Agent"},
		{Role: agent.RoleUser, Content: "帮我写一个 Go 函数计算平方根"},
		{Role: agent.RoleAssistant, Content: "好的，我来写..."},
	}
	got := DeriveTitle(msgs)
	want := "帮我写一个 Go 函数计算平方根"
	if got != want {
		t.Fatalf("title want %q got %q", want, got)
	}
}

func TestDeriveTitleTruncate(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	msgs := []agent.Message{{Role: agent.RoleUser, Content: long}}
	got := DeriveTitle(msgs)
	if len(got) > 63 { // 60 + "…"
		t.Fatalf("truncated title too long: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("should end with ellipsis")
	}
}

func TestDeriveTitleNoUser(t *testing.T) {
	msgs := []agent.Message{
		{Role: agent.RoleSystem, Content: "system"},
	}
	if got := DeriveTitle(msgs); got != "" {
		t.Fatalf("empty title want '', got %q", got)
	}
}

func TestTitleCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tc, err := NewTitleCache(dir)
	if err != nil {
		t.Fatalf("NewTitleCache: %v", err)
	}
	if got := tc.Get("id1"); got != "" {
		t.Fatalf("unset want '', got %q", got)
	}
	if err := tc.Set("id1", "首条消息标题"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := tc.Get("id1"); got != "首条消息标题" {
		t.Fatalf("Get want '首条消息标题', got %q", got)
	}
	// 持久化：重载后仍存在
	tc2, err := NewTitleCache(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := tc2.Get("id1"); got != "首条消息标题" {
		t.Fatalf("reload Get want '首条消息标题', got %q", got)
	}
	// 删除
	if err := tc.Set("id1", ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if got := tc.Get("id1"); got != "" {
		t.Fatalf("after delete want '', got %q", got)
	}
	// 验证 titles.json 被写入
	data, err := os.ReadFile(filepath.Join(dir, "titles.json"))
	if err != nil {
		t.Fatalf("titles.json read: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("titles.json should not be empty after a Set")
	}
}
