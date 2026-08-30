package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestGenerateQueryID_Unique(t *testing.T) {
	ids := make(map[QueryID]bool)
	for i := 0; i < 100; i++ {
		id := GenerateQueryID()
		if ids[id] {
			t.Fatalf("duplicate QueryID: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateQueryID_Format(t *testing.T) {
	id := GenerateQueryID()
	s := string(id)
	if len(s) < 3 || s[0] != 'q' || s[1] != '-' {
		t.Fatalf("unexpected QueryID format: %s", id)
	}
}

func TestNewQuery_Basic(t *testing.T) {
	q := NewQuery("test-source")
	if q.QueryID() == "" {
		t.Fatal("expected non-empty QueryID")
	}
	ctx := q.Context()
	if ctx.Source != "test-source" {
		t.Fatalf("expected source 'test-source', got %q", ctx.Source)
	}
	if ctx.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", ctx.Generation)
	}
	if ctx.Step != 0 {
		t.Fatalf("expected step 0, got %d", ctx.Step)
	}
}

func TestQueryLifecycle_BeginStep(t *testing.T) {
	q := NewQuery("test")
	q.BeginStep()
	ctx := q.Context()
	if ctx.Step != 1 {
		t.Fatalf("expected step 1, got %d", ctx.Step)
	}
	q.BeginStep()
	q.BeginStep()
	ctx = q.Context()
	if ctx.Step != 3 {
		t.Fatalf("expected step 3, got %d", ctx.Step)
	}
}

func TestQueryLifecycle_BeginEndOperation(t *testing.T) {
	q := NewQuery("test")

	q.BeginOperation("llm-call")
	q.BeginOperation("tool-exec")
	snap := q.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 active ops, got %d", len(snap))
	}
	if snap[0].opType != "llm-call" || snap[1].opType != "tool-exec" {
		t.Fatalf("unexpected op types: %v", snap)
	}

	q.EndOperation("llm-call")
	snap = q.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 active op after end, got %d", len(snap))
	}
	if snap[0].opType != "tool-exec" {
		t.Fatalf("expected 'tool-exec', got %q", snap[0].opType)
	}

	q.EndOperation("tool-exec")
	snap = q.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected 0 active ops, got %d", len(snap))
	}
}

func TestQueryLifecycle_EndOperation_NotFound(t *testing.T) {
	q := NewQuery("test")
	q.BeginOperation("llm-call")
	// 结束不存在的操作不应 panic
	q.EndOperation("nonexistent")
	snap := q.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 op remaining, got %d", len(snap))
	}
}

func TestQueryLifecycle_Snapshot_Isolation(t *testing.T) {
	q := NewQuery("test")
	q.BeginOperation("op1")
	snap := q.Snapshot()
	q.BeginOperation("op2")
	// 修改不应影响之前的快照
	if len(snap) != 1 {
		t.Fatalf("snapshot should be isolated, expected 1, got %d", len(snap))
	}
	if len(q.Snapshot()) != 2 {
		t.Fatalf("expected 2 ops now, got %d", len(q.Snapshot()))
	}
}

func TestQueryLifecycle_Complete(t *testing.T) {
	q := NewQuery("test")
	start := time.Now()
	q.Complete(nil)
	if q.completedAt.IsZero() {
		t.Fatal("expected completedAt to be set")
	}
	if q.terminalErr != nil {
		t.Fatalf("expected nil error, got %v", q.terminalErr)
	}

	q2 := NewQuery("test2")
	q2.Complete(context.Canceled)
	if q2.terminalErr != context.Canceled {
		t.Fatalf("expected time.ErrCanceled, got %v", q2.terminalErr)
	}
	_ = start
}

func TestQueryLifecycle_Elapsed(t *testing.T) {
	q := NewQuery("test")
	time.Sleep(10 * time.Millisecond)
	elapsed := q.Elapsed()
	if elapsed < 10*time.Millisecond {
		t.Fatalf("expected elapsed >= 10ms, got %v", elapsed)
	}

	// 完成后 elapsed 应固定
	q.Complete(nil)
	elapsed1 := q.Elapsed()
	time.Sleep(10 * time.Millisecond)
	elapsed2 := q.Elapsed()
	if elapsed1 != elapsed2 {
		t.Fatalf("elapsed should be fixed after Complete: %v != %v", elapsed1, elapsed2)
	}
}

func TestQueryLifecycle_FormatLog(t *testing.T) {
	q := NewQuery("test-source")
	q.BeginStep()
	log := q.FormatLog("started")
	if log == "" {
		t.Fatal("expected non-empty log")
	}
	// 应包含关键字段
	for _, want := range []string{"query.started", "queryId=", "generation=1", "source=test-source", "step=1"} {
		if !containsStr(log, want) {
			t.Fatalf("log missing %q: %s", want, log)
		}
	}

	// 带 extras
	log = q.FormatLog("error", "reason=timeout", "retry=3")
	if !containsStr(log, "reason=timeout") || !containsStr(log, "retry=3") {
		t.Fatalf("log missing extras: %s", log)
	}
}

func TestContextWithQuery_RoundTrip(t *testing.T) {
	q := NewQuery("test")
	ctx := ContextWithQuery(context.Background(), q)
	got := QueryFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil QueryLifecycle from context")
	}
	if got.QueryID() != q.QueryID() {
		t.Fatalf("QueryID mismatch: %q vs %q", got.QueryID(), q.QueryID())
	}
}

func TestQueryFromContext_Empty(t *testing.T) {
	got := QueryFromContext(context.Background())
	if got != nil {
		t.Fatalf("expected nil for empty context, got %v", got)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
