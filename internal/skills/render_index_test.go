package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

func skillContent(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
}

// TestRenderIndex 索引注入只含 name/description/路径，不含正文。
func TestRenderIndex(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查", "用法正文"))
	writeSkill(t, dir, "net.md", skillContent("net", "网络诊断", "用法正文"))

	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) != 0 {
		t.Fatalf("load failed: %v", failed)
	}
	out := m.RenderIndex()
	if !strings.Contains(out, "可用技能") {
		t.Fatalf("missing header: %s", out)
	}
	if !strings.Contains(out, "disk") || !strings.Contains(out, "磁盘检查") {
		t.Fatalf("missing disk: %s", out)
	}
	// 关键：正文不注入（缓存友好——正文变化不破坏 System prompt）
	if strings.Contains(out, "用法正文") {
		t.Fatalf("body should not be injected in index mode: %s", out)
	}
	// 路径正确（绝对路径，read 可加载）
	if !strings.Contains(out, filepath.Join(dir, "disk.md")) {
		t.Fatalf("missing path: %s", out)
	}
}

// TestRenderIndexVsFull 两种模式差异：full 含正文，index 不含。
func TestRenderIndexVsFull(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查", "用法正文"))
	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) != 0 {
		t.Fatalf("load failed: %v", failed)
	}
	idx := m.RenderIndex()
	full := m.RenderAll()
	if !strings.Contains(full, "用法正文") {
		t.Fatalf("full should include body: %s", full)
	}
	if strings.Contains(idx, "用法正文") {
		t.Fatalf("index should not include body: %s", idx)
	}
}

// TestIndexStableUnderBodyChange 技能正文变化不改变索引注入
// （System prompt 前缀缓存不被破坏——pi-cache-guardian 的核心目标）。
func TestIndexStableUnderBodyChange(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查 v1", "旧正文"))
	m1 := NewManager(dir)
	if _, failed := m1.LoadAll(); len(failed) != 0 {
		t.Fatalf("load failed: %v", failed)
	}
	p1 := m1.RenderIndex()

	// 修改技能正文，name/description 不变
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查 v1", "完全不同的新正文"))
	m2 := NewManager(dir)
	if _, failed := m2.LoadAll(); len(failed) != 0 {
		t.Fatalf("reload failed: %v", failed)
	}
	p2 := m2.RenderIndex()

	if p1 != p2 {
		t.Fatalf("index should be stable under body change:\n---v1---\n%s\n---v2---\n%s", p1, p2)
	}
	// 反向验证：description 变化会反映（这是特性，不是缺陷）
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查 v2", "完全不同的新正文"))
	m3 := NewManager(dir)
	m3.LoadAll()
	p3 := m3.RenderIndex()
	if p3 == p1 {
		t.Fatal("description change should reflect in index")
	}
}

// TestSkillPath 加载后 Path 字段 = 技能文件绝对路径。
func TestSkillPath(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "disk.md", skillContent("disk", "磁盘检查", "用法正文"))
	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) != 0 {
		t.Fatalf("load failed: %v", failed)
	}
	s, ok := m.Get("disk")
	if !ok {
		t.Fatal("skill not found")
	}
	want := filepath.Join(dir, "disk.md")
	if s.Path != want {
		t.Fatalf("path wrong: got %q want %q", s.Path, want)
	}
}
