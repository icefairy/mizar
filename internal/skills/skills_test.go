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
