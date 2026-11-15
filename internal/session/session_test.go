package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
)

func TestSessionAppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Append("sess1", agent.Message{Role: agent.RoleUser, Content: "你好"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("sess1", agent.Message{Role: agent.RoleAssistant, Content: "你好呀"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Load("sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 msgs got %d", len(msgs))
	}
	if msgs[0].Content != "你好" || msgs[1].Role != agent.RoleAssistant {
		t.Fatalf("msgs: %+v", msgs)
	}
}

func TestSessionAppendWritesTimestamp(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	// 调用方不关心时间戳：Append 自动补写
	if err := s.Append("ts", agent.Message{Role: agent.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Load("ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Timestamp == "" {
		t.Fatalf("expected timestamp, got %+v", msgs)
	}
	if _, err := time.Parse(time.RFC3339, msgs[0].Timestamp); err != nil {
		t.Fatalf("timestamp not RFC3339: %q (%v)", msgs[0].Timestamp, err)
	}
}

func TestSessionAppendKeepsGivenTimestamp(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	fixed := "2020-01-02T03:04:05.000+08:00"
	if err := s.Append("ts2", agent.Message{Role: agent.RoleUser, Content: "hi", Timestamp: fixed}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Load("ts2")
	if len(msgs) != 1 || msgs[0].Timestamp != fixed {
		t.Fatalf("timestamp overwritten: %+v", msgs)
	}
}

func TestSessionList(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Append("a", agent.Message{Role: agent.RoleUser, Content: "x"})
	s.Append("b", agent.Message{Role: agent.RoleUser, Content: "y"})
	names := s.List()
	if len(names) != 2 {
		t.Fatalf("want 2 got %v", names)
	}
}

func TestSessionDelete(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Append("del-me", agent.Message{Role: agent.RoleUser, Content: "x"})
	if err := s.Delete("del-me"); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("session not deleted")
	}
	// 删除不存在的
	if err := s.Delete("nope"); err == nil {
		t.Fatal("expected error deleting missing session")
	}
}

func TestSessionFileFormat(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Append("fmt", agent.Message{Role: agent.RoleUser, Content: "hi"})
	raw, err := os.ReadFile(filepath.Join(dir, "fmt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.Contains(line, `"role":"user"`) || !strings.Contains(line, `"content":"hi"`) {
		t.Fatalf("bad format: %s", line)
	}
}

// TestAppendRedactsSecrets 审计补强回归：落盘前必须脱敏（会话文件不该留明文密钥）。
func TestAppendRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	secret := "sk-proj-AbCdEf0123456789GhIjKlMnOpQrStUv"
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "我的 key 是 " + secret},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash",
			ToolArgs: `{"command":"curl -H 'Authorization: Bearer ` + secret + `' x"}`},
	}
	for _, m := range msgs {
		if err := st.Append("s1", m); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("会话文件里不应出现明文密钥:\n%s", raw)
	}
	if !strings.Contains(string(raw), "***") {
		t.Errorf("应出现掩码:\n%s", raw)
	}
	// 关键：内存里的原始消息不能被改（模型看到的参数必须完整）
	if !strings.Contains(msgs[0].Content, secret) {
		t.Error("Append 不应修改调用方传入的消息（内存内容必须原样）")
	}
	if !strings.Contains(msgs[1].ToolArgs, secret) {
		t.Error("Append 不应修改 ToolArgs 原始值")
	}
}
