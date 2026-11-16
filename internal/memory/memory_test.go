package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCategory(t *testing.T) {
	cases := map[string]string{
		"user":       CategoryUser,
		"  FEEDBACK": CategoryFeedback,
		"project":    CategoryProject,
		"reference":  CategoryReference,
		"unknown":    CategoryReference,
		"":           CategoryReference,
	}
	for in, want := range cases {
		if got := NormalizeCategory(in); got != want {
			t.Errorf("NormalizeCategory(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStoreAddLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Add("user", "用户偏好结论先行"); err != nil {
		t.Fatal(err)
	}
	if err := st.Add("project", "所有 Go 代码走 gofmt"); err != nil {
		t.Fatal(err)
	}
	// 未知分类归 reference
	if err := st.Add("bogus", "外部文档链接 X"); err != nil {
		t.Fatal(err)
	}

	// 文件存在且结构正确
	b, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	for _, want := range []string{"# MEMORY", "## user", "## feedback", "## project", "## reference", "用户偏好结论先行", "所有 Go 代码走 gofmt", "外部文档链接 X"} {
		if !strings.Contains(content, want) {
			t.Errorf("MEMORY.md 缺少 %q\n%s", want, content)
		}
	}

	// 重新加载
	st2 := NewStore(dir)
	if err := st2.Load(); err != nil {
		t.Fatal(err)
	}
	m, err := st2.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(m[CategoryUser]) != 1 || m[CategoryUser][0] != "用户偏好结论先行" {
		t.Errorf("user 分类 round-trip 失败: %v", m[CategoryUser])
	}
	if len(m[CategoryReference]) != 1 {
		t.Errorf("未知分类应归 reference: %v", m[CategoryReference])
	}
}

func TestStoreIdempotentAdd(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Add("user", "同一条")
	_ = st.Add("user", "同一条")
	n, _ := st.Count()
	if n != 1 {
		t.Errorf("重复条目应幂等，count=%d", n)
	}
}

func TestStoreMultilineFlattened(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Add("project", "第一行\n第二行")
	m, _ := st.List("project")
	if len(m[CategoryProject]) != 1 || strings.Contains(m[CategoryProject][0], "\n") {
		t.Errorf("多行应折叠为单行: %v", m[CategoryProject])
	}
}

func TestStoreRemoveAndClear(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Add("user", "a")
	_ = st.Add("user", "b")
	ok, err := st.Remove("user", 1)
	if err != nil || !ok {
		t.Fatalf("Remove: %v %v", ok, err)
	}
	m, _ := st.List("user")
	if len(m[CategoryUser]) != 1 || m[CategoryUser][0] != "b" {
		t.Errorf("删除后应为 [b]: %v", m[CategoryUser])
	}
	if _, err := st.Remove("user", 5); err == nil {
		t.Error("越界删除应报错")
	}
	_ = st.Add("project", "p")
	if err := st.Clear(""); err != nil {
		t.Fatal(err)
	}
	n, _ := st.Count()
	if n != 0 {
		t.Errorf("Clear 全部后 count 应为 0，got %d", n)
	}
}

func TestStoreRenderEmpty(t *testing.T) {
	st := NewStore(t.TempDir())
	if s := st.Render(); s != "" {
		t.Errorf("空记忆 Render 应为空，got %q", s)
	}
}

func TestStoreRenderContainsCategories(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Add("user", "偏好 X")
	_ = st.Add("feedback", "不要过度设计")
	out := st.Render()
	if !strings.Contains(out, "结构化记忆") || !strings.Contains(out, "### user") || !strings.Contains(out, "偏好 X") {
		t.Errorf("Render 输出异常:\n%s", out)
	}
}

func TestStoreRenderTruncation(t *testing.T) {
	st := NewStore(t.TempDir())
	for i := 0; i < 100; i++ {
		_ = st.Add("reference", strings.Repeat("x", 190)+fmt.Sprintf("#%03d", i))
	}
	out := st.Render()
	if !strings.Contains(out, "已截断") {
		t.Errorf("应触发截断提示，输出长度 %d", len(out))
	}
	if len(out) > MaxInjectBytes+2000 {
		t.Errorf("截断后仍过大: %d", len(out))
	}
}

// TestGlobalRenderSkipsProject 回归（2026-11-16）：全局 MEMORY.md 的 project
// 条目不得注入——跨项目注入会让模型误认当前项目（曾因全局记忆全是 hmem-go
// 条目，WebUI 新会话跑去探索 hmem-go）。
func TestGlobalRenderSkipsProject(t *testing.T) {
	st := NewStore(t.TempDir())
	_ = st.Add(CategoryProject, "hmem-go 由 supervisor 管理")
	_ = st.Add(CategoryUser, "偏好结论先行")
	out := st.Render()
	if strings.Contains(out, "hmem-go") || strings.Contains(out, "### project") {
		t.Errorf("全局 Render 不该包含 project 条目:\n%s", out)
	}
	if !strings.Contains(out, "偏好结论先行") {
		t.Errorf("user 条目应保留:\n%s", out)
	}
}

// TestProjectStoreRoundTrip 项目记忆存储（按路径分文件）：只承载/渲染 project 段。
func TestProjectStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := NewStorePath(filepath.Join(dir, "--data-codes-mizar--", "MEMORY.md"), true)
	_ = p.Add(CategoryProject, "mizar 的构建命令是 go build ./...")
	_ = p.Add(CategoryUser, "不该写进项目文件")
	// 落盘重开（模拟下次会话加载）
	p2 := NewStorePath(filepath.Join(dir, "--data-codes-mizar--", "MEMORY.md"), true)
	if err := p2.Load(); err != nil {
		t.Fatal(err)
	}
	out := p2.Render()
	if !strings.Contains(out, "go build") || !strings.Contains(out, "### project") {
		t.Errorf("项目 Render 应含 project 条目:\n%s", out)
	}
	if strings.Contains(out, "不该写进项目文件") || strings.Contains(out, "### user") {
		t.Errorf("项目文件不该承载 user 分类:\n%s", out)
	}
	m, _ := p2.List("")
	if len(m[CategoryProject]) != 1 || len(m[CategoryUser]) != 0 {
		t.Errorf("List 应只有 project 一条: %v", m)
	}
}

func TestParseIgnoresScatteredEntries(t *testing.T) {
	content := "# MEMORY\n- 散落条目\n## user\n- 合法\n"
	m := parse(content)
	if len(m[CategoryReference]) != 0 {
		t.Errorf("章节外条目应被忽略: %v", m[CategoryReference])
	}
	if len(m[CategoryUser]) != 1 || m[CategoryUser][0] != "合法" {
		t.Errorf("user 解析失败: %v", m[CategoryUser])
	}
}

func TestPathInDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	st := NewStore(dir)
	if filepath.Base(st.Path()) != "MEMORY.md" {
		t.Errorf("索引文件名应为 MEMORY.md: %s", st.Path())
	}
}
