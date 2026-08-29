package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillToolLoad(t *testing.T) {
	dir := t.TempDir()
	skillBody := `---
name: test-skill
description: 一个测试技能

# 正文
这是技能的正文内容。
`
	if err := os.WriteFile(filepath.Join(dir, "test-skill.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	st := toolSkill(dir)
	out, err := st.Run("test-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "这是技能的正文内容") {
		t.Fatalf("expected body in output: %q", out)
	}
	if !strings.Contains(out, "[技能:test-skill]") {
		t.Fatalf("expected header in output: %q", out)
	}
}

func TestSkillToolNotFound(t *testing.T) {
	dir := t.TempDir()
	st := toolSkill(dir)
	_, err := st.Run("no-such-skill")
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestSkillToolEmptyName(t *testing.T) {
	dir := t.TempDir()
	st := toolSkill(dir)
	_, err := st.Run("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}
