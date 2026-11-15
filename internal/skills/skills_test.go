package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill 写一个测试技能文件。
func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFrontmatter(t *testing.T) {
	body := `---
name: test-skill
description: 测试技能
tags: [a, b]
---
# 正文
按以下步骤做：
1. 第一步
2. 第二步
`
	s, err := parseSkill("test.md", body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "test-skill" {
		t.Fatalf("name=%q", s.Name)
	}
	if s.Description != "测试技能" {
		t.Fatalf("desc=%q", s.Description)
	}
	if !strings.Contains(s.Body, "第一步") {
		t.Fatalf("body missing: %q", s.Body)
	}
	if s.Source != "test.md" {
		t.Fatalf("source=%q", s.Source)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	body := `# 没有 frontmatter 的技能
直接正文`
	s, err := parseSkill("plain.md", body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name == "" {
		t.Fatal("expected derived name")
	}
	if s.Description == "" {
		t.Fatal("expected derived description")
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "skill-a.md", `---
name: skill-a
description: 技能A
---
内容A
`)
	writeSkill(t, dir, "skill-b.md", `---
name: skill-b
description: 技能B
---
内容B
`)
	writeSkill(t, dir, "not-a-skill.txt", "忽略我")
	writeSkill(t, dir, "README.md", "忽略根 README")

	m := NewManager(dir)
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 loaded got %v", loaded)
	}
	if _, ok := m.Get("skill-a"); !ok {
		t.Fatal("skill-a missing")
	}
}

func TestRenderPrompt(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "s.md", `---
name: s
description: 技能S
---
# 步骤
- 做X
- 做Y
`)
	m := NewManager(dir)
	m.LoadAll()
	s, ok := m.Get("s")
	if !ok {
		t.Fatal("skill missing")
	}
	prompt := s.Render()
	if !strings.Contains(prompt, "做X") {
		t.Fatalf("render missing body: %q", prompt)
	}
}

func TestEmptyDir(t *testing.T) {
	m := NewManager(t.TempDir())
	loaded, failed := m.LoadAll()
	if len(loaded) != 0 || len(failed) != 0 {
		t.Fatalf("want empty got loaded=%v failed=%v", loaded, failed)
	}
}

// TestManagerDisabledFiltered 审计修复回归：禁用的技能不进 All()/渲染。
func TestManagerDisabledFiltered(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, desc string) {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+desc+"\n---\nbody\n"), 0o644)
	}
	mk("alpha", "A 技能")
	mk("beta", "B 技能")

	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}
	if len(m.All()) != 2 {
		t.Fatalf("初始应 2 个技能，got %d", len(m.All()))
	}

	m.SetDisabledFromList([]string{"alpha"})
	all := m.All()
	if len(all) != 1 || all[0].Name != "beta" {
		t.Errorf("禁用 alpha 后 All() 应只剩 beta，got %+v", all)
	}
	if !m.IsDisabled("alpha") || m.IsDisabled("beta") {
		t.Error("IsDisabled 判定错误")
	}
	// 渲染也不含
	for _, out := range []string{m.RenderIndex(), m.RenderAll()} {
		if strings.Contains(out, "alpha") {
			t.Errorf("禁用技能不应出现在渲染结果: %s", out)
		}
		if !strings.Contains(out, "beta") {
			t.Errorf("未禁用技能应出现: %s", out)
		}
	}

	// 恢复
	m.SetDisabledFromList(nil)
	if len(m.All()) != 2 {
		t.Error("清除禁用后应恢复 2 个")
	}
}
