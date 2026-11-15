package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostBudgetBlocksAmplification P3 回归：超预算的宿主调用被拒绝。
func TestHostBudgetBlocksAmplification(t *testing.T) {
	dir := t.TempDir()
	h := &HostFuncs{
		FSWrite: func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
		FSRead:  func(p string) (string, error) { b, err := os.ReadFile(p); return string(b), err },
	}
	e, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	e.SetHostBudget(3)

	target := filepath.Join(dir, "out.txt")
	script := `
var lastErr = "";
for (var i = 0; i < 6; i++) {
  try { fs_write("` + target + `", "x" + i); }
  catch (e) { lastErr = String(e); }
}
`
	if err := e.RunScript("budget", script); err != nil {
		t.Fatalf("run: %v", err)
	}
	lastErr := e.vm.Get("lastErr").String()
	if lastErr == "" || !strings.Contains(lastErr, "超预算") {
		t.Errorf("超预算应报错，got %q", lastErr)
	}
}

// TestHostBudgetUnlimitedByDefault 默认不限（保持现行为）。
func TestHostBudgetUnlimitedByDefault(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "o.txt")
	e, _ := New(&HostFuncs{
		FSWrite: func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
	})
	for i := 0; i < 50; i++ {
		if err := e.RunScript("w", `fs_write("`+target+`", "x")`); err != nil {
			t.Fatalf("默认应不限：%v", err)
		}
	}
}

// TestHookBudgetHostFunc 插件可通过 hook_budget() 自设预算。
func TestHookBudgetHostFunc(t *testing.T) {
	e, _ := New(&HostFuncs{
		FSWrite: func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
	})
	if err := e.RunScript("set", `hook_budget(2)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := `
var errs = 0;
for (var i = 0; i < 4; i++) {
  try { fs_write("` + filepath.Join(dir, "a.txt") + `", "x"); } catch (e) { errs++; }
}
`
	if err := e.RunScript("run", script); err != nil {
		t.Fatal(err)
	}
	if got := e.vm.Get("errs").ToInteger(); got != 2 {
		t.Errorf("应 2 次被拒，got %d", got)
	}
}
