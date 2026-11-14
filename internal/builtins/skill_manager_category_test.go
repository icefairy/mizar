package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillManagerCreateWithCategory 带 category 创建应落到 <cat>/<name>/SKILL.md。
func TestSkillManagerCreateWithCategory(t *testing.T) {
	base := t.TempDir()
	sm := NewSkillManager(base)
	out, err := sm.run(`{"action":"create","name":"docker-ops","category":"devops","body":"---\nname: docker-ops\ndescription: Docker 运维\n---\n正文"}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "devops") {
		t.Fatalf("out=%q 未提示分类", out)
	}
	p := filepath.Join(base, "devops", "docker-ops", "SKILL.md")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("文件未落到分类目录: %v", err)
	}
}

// TestSkillManagerListGroupedByCategory list 应按分类分组显示。
func TestSkillManagerListGroupedByCategory(t *testing.T) {
	base := t.TempDir()
	sm := NewSkillManager(base)
	if _, err := sm.run(`{"action":"create","name":"a","category":"devops","body":"---\nname: a\ndescription: A\n---\nx"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.run(`{"action":"create","name":"b","category":"reverse","body":"---\nname: b\ndescription: B\n---\nx"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.run(`{"action":"create","name":"c","body":"---\nname: c\ndescription: C\n---\nx"}`); err != nil {
		t.Fatal(err)
	}
	out, err := sm.run(`{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[devops]", "[reverse]", "[未分类]", "a", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺 %q:\n%s", want, out)
		}
	}
	// 按分类过滤
	out, err = sm.run(`{"action":"list","category":"devops"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a") || strings.Contains(out, "b") {
		t.Errorf("分类过滤失败:\n%s", out)
	}
}

// TestSkillManagerEditPatchDeleteCategorized 分类技能也能按名 edit/patch/delete。
func TestSkillManagerEditPatchDeleteCategorized(t *testing.T) {
	base := t.TempDir()
	sm := NewSkillManager(base)
	if _, err := sm.run(`{"action":"create","name":"x","category":"devops","body":"---\nname: x\ndescription: X\n---\n旧内容"}`); err != nil {
		t.Fatal(err)
	}
	// patch 找得到分类里的技能
	if _, err := sm.run(`{"action":"patch","name":"x","find":"旧内容","replace":"新内容"}`); err != nil {
		t.Fatalf("patch 分类技能: %v", err)
	}
	p := filepath.Join(base, "devops", "x", "SKILL.md")
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "新内容") {
		t.Fatalf("patch 未生效: %s", data)
	}
	// edit 也能定位
	if _, err := sm.run(`{"action":"edit","name":"devops/x","body":"---\nname: x\ndescription: X\n---\n重写"}`); err != nil {
		t.Fatalf("edit 带分类路径: %v", err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "重写") {
		t.Fatalf("edit 未生效: %s", data)
	}
	// delete 清掉技能与空分类目录
	if _, err := sm.run(`{"action":"delete","name":"x"}`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "devops")); !os.IsNotExist(err) {
		t.Fatal("空分类目录未被清理")
	}
}

// TestSkillManagerRejectsTraversal 名字/分类含路径分隔符必须拒绝。
func TestSkillManagerRejectsTraversal(t *testing.T) {
	sm := NewSkillManager(t.TempDir())
	if _, err := sm.run(`{"action":"create","name":"../../evil","body":"x"}`); err == nil {
		t.Fatal("应拒绝目录穿越 name")
	}
	if _, err := sm.run(`{"action":"create","name":"ok","category":"../x","body":"x"}`); err == nil {
		t.Fatal("应拒绝目录穿越 category")
	}
}
