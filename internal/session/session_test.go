package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
