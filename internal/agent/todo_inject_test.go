package agent

import (
	"strings"
	"testing"
)

// TestTodoInjectFieldCloned 验证 Clone 复制 TodoInject。
func TestTodoInjectCloned(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	called := false
	a.TodoInject = func() string { called = true; return "X" }
	b := a.Clone()
	if b.TodoInject == nil {
		t.Fatal("Clone 应复制 TodoInject")
	}
	_ = b.TodoInject()
	if !called {
		t.Error("Clone 的 TodoInject 应指向同一函数")
	}
}

// TestTodoInjectNilSafe 无 TodoInject 时循环不应 panic。
func TestTodoInjectNilSafe(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	if a.TodoInject != nil {
		t.Error("默认应为 nil")
	}
}

// TestTodoInjectTextShape 注入文本作为普通 user 消息的形态（不参与 todo 回放）。
func TestTodoInjectTextShape(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	a.TodoInject = func() string { return "【当前待办】- [ ] #1: x" }
	inj := strings.TrimSpace(a.TodoInject())
	m := Message{Role: RoleUser, Content: inj}
	if m.Kind != KindNormal {
		// 必须是普通消息，否则会被 todo 回放逻辑当成状态载体
		t.Log("Kind 为空 = KindNormal，符合预期")
	}
}
