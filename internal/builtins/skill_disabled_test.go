package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillToolRejectsDisabled 审计修复回归：禁用的技能连按名加载也拒绝。
func TestSkillToolRejectsDisabled(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "alpha"), 0o755)
	os.WriteFile(filepath.Join(dir, "alpha", "SKILL.md"),
		[]byte("---\nname: alpha\ndescription: A\n---\nbody-here\n"), 0o644)

	tool := toolSkill(dir)
	// 未禁用：可读
	out, err := tool.Run("alpha")
	if err != nil || !strings.Contains(out, "body-here") {
		t.Fatalf("未禁用时应可读: out=%q err=%v", out, err)
	}

	// 禁用后：拒绝
	SetDisabledSkills([]string{"alpha"})
	defer SetDisabledSkills(nil)
	if _, err := tool.Run("alpha"); err == nil {
		t.Fatal("禁用的技能应拒绝按名加载")
	} else if !strings.Contains(err.Error(), "禁用") {
		t.Errorf("错误信息应说明已禁用: %v", err)
	}
}

func TestSetDisabledSkillsNilSafe(t *testing.T) {
	SetDisabledSkills(nil)
	if skillIsDisabled("anything") {
		t.Error("空名单不应禁用任何技能")
	}
}
