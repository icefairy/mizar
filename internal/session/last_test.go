package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mizar/internal/agent"
)

func TestSetLastAndLast(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).Scoped("--data-codes-mizar--")
	if id, _ := s.Last(); id != "" {
		t.Fatalf("expected empty last, got %q", id)
	}
	if err := s.SetLast("abc-123"); err != nil {
		t.Fatal(err)
	}
	id, scope := s.Last()
	if id != "abc-123" || scope != "--data-codes-mizar--" {
		t.Fatalf("want abc-123/--data-codes-mizar--, got %q/%q", id, scope)
	}
	// 状态文件放在会话根目录（全局唯一），不落在作用域子目录里
	if _, err := os.Stat(filepath.Join(dir, lastFile)); err != nil {
		t.Fatalf("last-session.json should live at session root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "--data-codes-mizar--", lastFile)); err == nil {
		t.Fatal("last-session.json should not be inside scope dir")
	}
	// 状态文件不应被当成会话条目
	if ids := s.List(); len(ids) != 0 {
		t.Fatalf("last-session should not appear in List, got %v", ids)
	}
}

func TestLastIsGlobalNotPerScope(t *testing.T) {
	dir := t.TempDir()
	// 先在作用域 A 记录，再在作用域 B 记录：只保留全局最新一份
	a := New(dir).Scoped("--path-a--")
	if err := a.SetLast("id-a"); err != nil {
		t.Fatal(err)
	}
	b := New(dir).Scoped("--path-b--")
	if err := b.SetLast("id-b"); err != nil {
		t.Fatal(err)
	}
	// 任意作用域（甚至无关作用域）读到的都是全局最后一条
	for _, sc := range []string{"--path-a--", "--path-b--", "--no-such--"} {
		id, scope := New(dir).Scoped(sc).Last()
		if id != "id-b" || scope != "--path-b--" {
			t.Fatalf("scope %s: want id-b/--path-b--, got %q/%q", sc, id, scope)
		}
	}
}

func TestLastCompatPlainText(t *testing.T) {
	dir := t.TempDir()
	// 旧格式：纯文本 ID（无 scope）
	if err := os.WriteFile(filepath.Join(dir, lastFile), []byte("legacy-id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, scope := New(dir).Last()
	if id != "legacy-id" || scope != "" {
		t.Fatalf("want legacy-id/\"\", got %q/%q", id, scope)
	}
}

func TestLastLegacyScopedFile(t *testing.T) {
	dir := t.TempDir()
	// 更早的实现把纯文本记录散落在各作用域子目录，无根 last-session.json
	scopeDir := filepath.Join(dir, "--other--")
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "last-session"), []byte("oldid"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 当前路径作用域下也放一份，但更新 → 应取更新那份
	curDir := filepath.Join(dir, "--cur--")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(scopeDir, "last-session"), old, old)
	if err := os.WriteFile(filepath.Join(curDir, "last-session"), []byte("newid"), 0o644); err != nil {
		t.Fatal(err)
	}

	id, scope := New(dir).Last()
	if id != "newid" || scope != "--cur--" {
		t.Fatalf("want newid/--cur--, got %q/%q", id, scope)
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if s.Exists("nope") {
		t.Fatal("nope should not exist")
	}
	if err := s.Append("s1", agent.Message{Role: agent.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("s1") {
		t.Fatal("s1 should exist")
	}
}
