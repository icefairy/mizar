package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetGetReset(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, ok := s.Get("/tmp/proj"); ok {
		t.Error("初始不应有记录")
	}
	if err := s.Set("/tmp/proj", Trusted); err != nil {
		t.Fatal(err)
	}
	e, ok := s.Get("/tmp/proj")
	if !ok || e.Decision != Trusted {
		t.Errorf("记录未生效: %+v ok=%v", e, ok)
	}
	// 重新打开应持久化
	s2 := NewStore(dir)
	if e, ok := s2.Get("/tmp/proj"); !ok || e.Decision != Trusted {
		t.Errorf("未持久化: %+v", e)
	}
	if err := s2.Reset("/tmp/proj"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("/tmp/proj"); ok {
		t.Error("Reset 后应无记录")
	}
}

func TestNormalizePath(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	// 相对路径与绝对路径应归一为同一条
	_ = s.Set(dir, Trusted)
	e, ok := s.Get(filepath.Join(dir, "."))
	if !ok || e.Decision != Trusted {
		t.Error("路径归一化失败（相对/绝对应同键）")
	}
}

func TestProjectResources(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, ".mizar", "prompts"), 0o755)

	res := ProjectResources(sub)
	joined := strings.Join(res, "\n")
	if !strings.Contains(joined, "AGENTS.md") {
		t.Errorf("应找到上层 AGENTS.md: %v", res)
	}
	if !strings.Contains(joined, ".mizar") {
		t.Errorf("应找到 .mizar/prompts: %v", res)
	}
}

func TestEnsureNoResources(t *testing.T) {
	s := NewStore(t.TempDir())
	ok, hadResources := Ensure(s, t.TempDir(), nil)
	if !ok {
		t.Error("无项目资源应视为可信")
	}
	if hadResources {
		t.Error("无项目资源时 hadResources 应为 false")
	}
}

func TestEnsureNonInteractiveDenies(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("x"), 0o644)
	s := NewStore(t.TempDir())
	ok, had := Ensure(s, root, nil)
	if ok || !had {
		t.Errorf("非交互且未记录应拒绝: ok=%v had=%v", ok, had)
	}
}

func TestEnsureAskTrusted(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("x"), 0o644)
	s := NewStore(t.TempDir())
	asked := false
	ok, had := Ensure(s, root, func(res []string) bool {
		asked = true
		if len(res) == 0 {
			t.Error("询问时应提供资源列表")
		}
		return true
	})
	if !ok || !had || !asked {
		t.Errorf("应询问并信任: ok=%v had=%v asked=%v", ok, had, asked)
	}
	// 记录已写入，二次不再询问
	asked2 := false
	ok2, _ := Ensure(s, root, func([]string) bool { asked2 = true; return false })
	if !ok2 || asked2 {
		t.Error("已记录应直接返回，不再询问")
	}
}

func TestEnsureAskDenied(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("x"), 0o644)
	s := NewStore(t.TempDir())
	ok, _ := Ensure(s, root, func([]string) bool { return false })
	if ok {
		t.Error("用户拒绝应不可信")
	}
	e, found := s.Get(root)
	if !found || e.Decision != Denied {
		t.Errorf("应记录 denied: %+v found=%v", e, found)
	}
}

func TestDescribe(t *testing.T) {
	s := NewStore(t.TempDir())
	if !strings.Contains(Describe(s), "无项目信任记录") {
		t.Error("空记录文案缺失")
	}
	_ = s.Set("/p", Trusted)
	if !strings.Contains(Describe(s), "/p") {
		t.Error("记录文案缺失")
	}
}
