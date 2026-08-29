package agent

import (
	"testing"
)

func TestGuardReset(t *testing.T) {
	g := NewRepeatGuard(nil)
	g.Observe("bash", "ls")
	g.Observe("bash", "ls")
	if g.Count() != 2 {
		t.Fatalf("count want 2 got %d", g.Count())
	}
	g.Reset()
	if g.Count() != 0 {
		t.Fatalf("after reset count want 0 got %d", g.Count())
	}
}

func TestGuardThresholds(t *testing.T) {
	g := NewRepeatGuard(nil)
	// 默认阈值 [3,5,8]，前两次无提醒
	rem, term := g.Observe("bash", "ls")
	if rem != "" || term {
		t.Fatal("1st call should not trigger")
	}
	g.Observe("bash", "ls")
	rem, term = g.Observe("bash", "ls")
	if rem == "" || term {
		t.Fatal("3rd call should gentle remind")
	}
	if !contains(rem, "重复") {
		t.Fatalf("gentle reminder expected '重复', got %q", rem)
	}
	// 第 5 次详细提醒
	g.Observe("bash", "ls")
	rem, term = g.Observe("bash", "ls")
	if rem == "" || term {
		t.Fatal("5th call should detailed remind")
	}
	if !contains(rem, "连续次数: 5") {
		t.Fatalf("detailed reminder should name count, got %q", rem)
	}
}

func TestGuardTerminate(t *testing.T) {
	g := NewRepeatGuard(nil)
	for i := 0; i < 8; i++ {
		g.Observe("bash", "ls")
	}
	rem, term := g.Observe("bash", "ls")
	if !term {
		t.Fatal("9th identical call should terminate")
	}
	if rem == "" {
		t.Fatal("termination reminder should not be empty")
	}
}

func TestGuardChangeResets(t *testing.T) {
	g := NewRepeatGuard(nil)
	g.Observe("bash", "ls")
	g.Observe("bash", "ls")
	// 换工具 → 重置计数
	g.Observe("read", "a")
	if g.Count() != 1 {
		t.Fatalf("after tool change count want 1 got %d", g.Count())
	}
	// 换参数 → 重置计数
	g.Observe("bash", "pwd")
	if g.Count() != 1 {
		t.Fatalf("after arg change count want 1 got %d", g.Count())
	}
}

func TestGuardCanonicalizeArgs(t *testing.T) {
	a1 := `{"path":"/tmp","recursive":true}`
	a2 := `{"recursive":true,"path":"/tmp"}`
	if CanonicalizeArgs(a1) != CanonicalizeArgs(a2) {
		t.Fatal("canonicalize should ignore key order")
	}
	// 非 JSON → 原文
	if CanonicalizeArgs("not json") != "not json" {
		t.Fatal("non-json should pass through")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && (len(s) >= len(sub)) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
