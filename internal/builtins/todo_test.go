package builtins

import (
	"strings"
	"testing"
)

func TestTodoWriteBasic(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	out, err := tw.Run(`[{"content":"写插件","status":"in_progress"},{"content":"写测试","status":"pending"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "✓") {
		t.Fatalf("expected checkmark in output: %q", out)
	}
	items := ListTodos()
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Content != "写插件" || items[0].Status != "in_progress" {
		t.Fatalf("item[0] unexpected: %+v", items[0])
	}
	if items[1].Content != "写测试" || items[1].Status != "pending" {
		t.Fatalf("item[1] unexpected: %+v", items[1])
	}
}

func TestTodoWriteRejectEmptyContent(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, err := tw.Run(`[{"content":"  ","status":"pending"}]`)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestTodoWriteRejectDuplicate(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, err := tw.Run(`[{"content":"same","status":"pending"},{"content":"same","status":"completed"}]`)
	if err == nil {
		t.Fatal("expected error for duplicate content")
	}
}

func TestTodoWriteRejectMultipleInProgress(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, err := tw.Run(`[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]`)
	if err == nil {
		t.Fatal("expected error for multiple in_progress")
	}
}

func TestTodoWriteReplaceList(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, _ = tw.Run(`[{"content":"first","status":"pending"}]`)
	_, _ = tw.Run(`[{"content":"second","status":"completed"}]`)
	items := ListTodos()
	if len(items) != 1 || items[0].Content != "second" {
		t.Fatalf("list should be replaced, got %+v", items)
	}
}

func TestTodoRender(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, _ = tw.Run(`[{"content":"todo-a","status":"in_progress"},{"content":"todo-b","status":"completed"},{"content":"todo-c","status":"pending"}]`)
	out := RenderTodos()
	if !strings.Contains(out, "▶ todo-a") {
		t.Fatalf("in_progress should render ▶: %q", out)
	}
	if !strings.Contains(out, "✓ todo-b") {
		t.Fatalf("completed should render ✓: %q", out)
	}
	if !strings.Contains(out, "○ todo-c") {
		t.Fatalf("pending should render ○: %q", out)
	}
}

func TestTodoWriteReset(t *testing.T) {
	ResetTodo()
	tw := toolTodoWrite()
	_, _ = tw.Run(`[{"content":"x","status":"pending"}]`)
	ResetTodo()
	if len(ListTodos()) != 0 {
		t.Fatal("reset should clear todos")
	}
}
