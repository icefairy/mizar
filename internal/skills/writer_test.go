package skills

import (
	"os"
	"strings"
	"testing"
)

func TestCreateFromPlugin(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)
	path, err := w.CreateFromPlugin("disk-watch", "监控磁盘空间", "disk_watch.ts",
		[]string{"disk_watch"}, "调用 disk_watch 工具，参数为目标路径。")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	s := string(body)
	for _, want := range []string{"name: disk-watch", "description: 监控磁盘空间", "disk_watch.ts", "disk_watch", "自举沉淀"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	// 能重新加载回管理器
	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	if _, ok := m.Get("disk-watch"); !ok {
		t.Fatal("skill not reloadable")
	}
}

func TestCreateFromText(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)
	path, err := w.CreateFromText("git-tips", "Git 常用技巧", "# 提交\n- 用清晰 message")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "git-tips") {
		t.Fatalf("bad content: %s", body)
	}
	_ = path
}

func TestCreateEmptyName(t *testing.T) {
	w := NewWriter(t.TempDir())
	if _, err := w.CreateFromPlugin("", "d", "p.ts", nil, "u"); err == nil {
		t.Fatal("expected error for empty name")
	}
}
