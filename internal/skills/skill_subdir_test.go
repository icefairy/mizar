package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// 子目录技能（<name>/SKILL.md）应被加载，且 name 取目录名。
func TestLoadSubdirSkills(t *testing.T) {
	dir := t.TempDir()
	// 子目录技能
	os.MkdirAll(filepath.Join(dir, "handoff"), 0o755)
	os.WriteFile(filepath.Join(dir, "handoff", "SKILL.md"),
		[]byte("---\nname: handoff\ndescription: 交接技能\n---\n\n正文内容\n"), 0o644)
	// 顶层单文件技能（兼容）
	os.WriteFile(filepath.Join(dir, "db.md"),
		[]byte("---\nname: db\ndescription: 数据库\n---\n\ndb 正文\n"), 0o644)
	// 带附属文件的子目录技能
	os.MkdirAll(filepath.Join(dir, "refskill", "references"), 0o755)
	os.WriteFile(filepath.Join(dir, "refskill", "SKILL.md"),
		[]byte("# refskill\n\n技能正文\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "refskill", "references", "api.md"), []byte("ref"), 0o644)

	m := NewManager(dir)
	loaded, failed := m.LoadAll()
	if len(failed) > 0 {
		t.Fatalf("failed: %v", failed)
	}
	for _, want := range []string{"handoff", "db", "refskill"} {
		s, ok := m.Get(want)
		if !ok {
			t.Fatalf("技能 %q 未加载（loaded=%v）", want, loaded)
		}
		if s.Path == "" {
			t.Fatalf("技能 %q 路径为空", want)
		}
	}
	// refskill 无 frontmatter，name 应取目录名
	if s, _ := m.Get("refskill"); s.Name != "refskill" {
		t.Fatalf("无 frontmatter 子目录技能 name 应为 refskill，得到 %q", s.Name)
	}
	// 附属文件不应被当作技能
	if _, ok := m.Get("api"); ok {
		t.Fatal("references/api.md 不应被加载为技能")
	}
	t.Logf("loaded=%v", loaded)
}
