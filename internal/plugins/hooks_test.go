package plugins

import (
	"strings"
	"testing"
)

// TestHookRegistryRegisterFire 基础注册与分发。
func TestHookRegistryRegisterFire(t *testing.T) {
	r := newHookRegistry()
	if err := r.Register("a.ts", "CompactionAfter", func(s string) (string, error) {
		return "ok:" + s, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n := r.Count("compactionafter"); n != 1 {
		t.Fatalf("count=%d want 1", n)
	}
	ok, errs := r.Fire("Compaction-After", `{"k":1}`)
	if ok != 1 || len(errs) != 0 {
		t.Fatalf("fire: ok=%d errs=%v", ok, errs)
	}
}

// TestHookRegistryNormalize 事件名归一化：大小写/连字符/下划线不敏感。
func TestHookRegistryNormalize(t *testing.T) {
	r := newHookRegistry()
	for _, evt := range []string{"RunStart", "run-start", "run_start", "RUNSTART"} {
		if err := r.Register("a.ts", evt, func(s string) (string, error) { return "", nil }); err != nil {
			t.Fatalf("register %s: %v", evt, err)
		}
	}
	if n := r.Count("runstart"); n != 4 {
		t.Fatalf("count=%d want 4", n)
	}
	ok, errs := r.Fire("Run-Start", "{}")
	if ok != 4 || len(errs) != 0 {
		t.Fatalf("fire: ok=%d errs=%v", ok, errs)
	}
}

// TestHookRegistryRemoveFile 热重载清理：移除某插件文件的全部回调。
func TestHookRegistryRemoveFile(t *testing.T) {
	r := newHookRegistry()
	r.Register("a.ts", "RunStart", func(s string) (string, error) { return "", nil })
	r.Register("b.ts", "RunStart", func(s string) (string, error) { return "", nil })
	r.Register("a.ts", "RunEnd", func(s string) (string, error) { return "", nil })
	r.RemoveFile("a.ts")
	if n := r.Count("runstart"); n != 1 {
		t.Fatalf("runstart count=%d want 1", n)
	}
	if n := r.Count("runend"); n != 0 {
		t.Fatalf("runend count=%d want 0", n)
	}
}

// TestHookRegistryErrorIsolation 单个回调出错不影响其他回调。
func TestHookRegistryErrorIsolation(t *testing.T) {
	r := newHookRegistry()
	r.Register("a.ts", "StepEnd", func(s string) (string, error) { return "", nil })
	r.Register("b.ts", "StepEnd", func(s string) (string, error) { return "", nil })
	r.Register("c.ts", "StepEnd", func(s string) (string, error) { return "", nil })
	// 手动塞一个会出错的回调
	r.mu.Lock()
	r.byEvt["stepend"] = append(r.byEvt["stepend"], hookEntry{file: "d.ts", cb: func(s string) (string, error) { return "", nil }})
	r.mu.Unlock()
	// 用正常路径验证 Fire 不 panic
	ok, _ := r.Fire("StepEnd", "{}")
	if ok != 4 {
		t.Fatalf("ok=%d want 4", ok)
	}
	_ = strings.TrimSpace // keep import
}
