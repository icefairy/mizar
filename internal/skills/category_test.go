package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkillRel 在 dir 下的相对路径写入 SKILL.md 内容（支持多级目录）。
func writeSkillRel(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skillDoc 生成一个带 frontmatter 的 SKILL.md 正文。
func skillDoc(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n正文\n"
}

// TestLoadCategorized 分类布局 <category>/<skill>/SKILL.md 应被加载且带 Category。
func TestLoadCategorized(t *testing.T) {
	dir := t.TempDir()
	writeSkillRel(t, dir, "devops/docker-ops/SKILL.md", skillDoc("docker-ops", "Docker 运维"))
	writeSkillRel(t, dir, "reverse/ida-tips/SKILL.md", skillDoc("ida-tips", "IDA 技巧"))
	writeSkillRel(t, dir, "flat-skill/SKILL.md", skillDoc("flat-skill", "无分类技能"))

	m := NewManager(dir)
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("failed: %v", failed)
	}
	if len(loaded) != 3 {
		t.Fatalf("loaded=%v, want 3", loaded)
	}
	cases := map[string]string{
		"docker-ops": "devops",
		"ida-tips":   "reverse",
		"flat-skill": "", // 顶层单层目录 → 无分类
	}
	for name, wantCat := range cases {
		s, ok := m.Get(name)
		if !ok {
			t.Fatalf("skill %q 未加载", name)
		}
		if s.Category != wantCat {
			t.Errorf("skill %q Category=%q, want %q", name, s.Category, wantCat)
		}
	}
}

// TestLoadThreeLevelIgnoredAsSkill 分类目录本身不含 SKILL.md，不能变成技能。
func TestLoadThreeLevelIgnoredAsSkill(t *testing.T) {
	dir := t.TempDir()
	// 分类目录里只有子技能，没有自己的 SKILL.md
	writeSkillRel(t, dir, "devops/a/SKILL.md", skillDoc("a", "A"))
	writeSkillRel(t, dir, "devops/b/SKILL.md", skillDoc("b", "B"))

	m := NewManager(dir)
	loaded, _ := m.LoadAll()
	if len(loaded) != 2 {
		t.Fatalf("loaded=%v, want 2（分类目录不能算技能）", loaded)
	}
	if _, ok := m.Get("devops"); ok {
		t.Fatal("分类目录 devops 被误当成技能")
	}
}

// TestRenderIndexShowsCategory 索引注入要带分类，便于模型按领域筛选。
func TestRenderIndexShowsCategory(t *testing.T) {
	dir := t.TempDir()
	writeSkillRel(t, dir, "devops/docker-ops/SKILL.md", skillDoc("docker-ops", "Docker 运维"))
	m := NewManager(dir)
	m.LoadAll()
	out := m.RenderIndex()
	if !contains(out, "devops") {
		t.Fatalf("RenderIndex 未出现分类: %s", out)
	}
	if !contains(out, "docker-ops") {
		t.Fatalf("RenderIndex 未出现技能名: %s", out)
	}
}

// TestCategories 分类清单去重排序。
func TestCategories(t *testing.T) {
	dir := t.TempDir()
	writeSkillRel(t, dir, "devops/a/SKILL.md", skillDoc("a", "A"))
	writeSkillRel(t, dir, "devops/b/SKILL.md", skillDoc("b", "B"))
	writeSkillRel(t, dir, "reverse/c/SKILL.md", skillDoc("c", "C"))
	writeSkillRel(t, dir, "flat/SKILL.md", skillDoc("flat", "无分类"))
	m := NewManager(dir)
	m.LoadAll()
	cats := m.Categories()
	want := []string{"devops", "reverse"}
	if len(cats) != len(want) {
		t.Fatalf("Categories=%v, want %v", cats, want)
	}
	for i := range want {
		if cats[i] != want[i] {
			t.Fatalf("Categories=%v, want %v", cats, want)
		}
	}
}

// TestAllByCategory 按分类过滤。
func TestAllByCategory(t *testing.T) {
	dir := t.TempDir()
	writeSkillRel(t, dir, "devops/a/SKILL.md", skillDoc("a", "A"))
	writeSkillRel(t, dir, "reverse/c/SKILL.md", skillDoc("c", "C"))
	m := NewManager(dir)
	m.LoadAll()
	got := m.AllByCategory("devops")
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("AllByCategory(devops)=%v", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
