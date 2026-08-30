package utils

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRaceAbort_CompletesFirst(t *testing.T) {
	ctx := context.Background()
	result, err := RaceAbort(ctx, func() (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("expected 42, got %d", result)
	}
}

func TestRaceAbort_CompletesWithError(t *testing.T) {
	ctx := context.Background()
	_, err := RaceAbort(ctx, func() (int, error) {
		return 0, errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected 'boom' error, got %v", err)
	}
}

func TestRaceAbort_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	result, err := RaceAbort(ctx, func() (int, error) {
		time.Sleep(100 * time.Millisecond)
		return 42, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if result != 0 {
		t.Fatalf("expected zero value, got %d", result)
	}
}

func TestRaceAbort_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := RaceAbort(ctx, func() (int, error) {
		time.Sleep(200 * time.Millisecond)
		return 42, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestMapWithConcurrency_Basic(t *testing.T) {
	ctx := context.Background()
	items := []int{1, 2, 3, 4, 5}
	results, err := MapWithConcurrency(ctx, items, 3, func(ctx context.Context, item int) (int, error) {
		return item * 2, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, expected := range []int{2, 4, 6, 8, 10} {
		if results[i] != expected {
			t.Fatalf("results[%d]: expected %d, got %d", i, expected, results[i])
		}
	}
}

func TestMapWithConcurrency_Empty(t *testing.T) {
	ctx := context.Background()
	results, err := MapWithConcurrency(ctx, []int{}, 3, func(ctx context.Context, item int) (int, error) {
		return item, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil, got %v", results)
	}
}

func TestMapWithConcurrency_Error(t *testing.T) {
	ctx := context.Background()
	items := []int{1, 2, 3}
	_, err := MapWithConcurrency(ctx, items, 2, func(ctx context.Context, item int) (int, error) {
		if item == 2 {
			return 0, errors.New("item 2 failed")
		}
		return item, nil
	})
	if err == nil || err.Error() != "item 2 failed" {
		t.Fatalf("expected 'item 2 failed' error, got %v", err)
	}
}

func TestMapWithConcurrency_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := MapWithConcurrency(ctx, []int{1, 2, 3}, 2, func(ctx context.Context, item int) (int, error) {
		return item, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMapWithConcurrency_PreservesOrder(t *testing.T) {
	ctx := context.Background()
	items := []int{5, 4, 3, 2, 1}
	results, err := MapWithConcurrency(ctx, items, 1, func(ctx context.Context, item int) (int, error) {
		return item, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, expected := range items {
		if results[i] != expected {
			t.Fatalf("results[%d]: expected %d, got %d", i, expected, results[i])
		}
	}
}

func TestMapWithConcurrency_UnlimitedConcurrency(t *testing.T) {
	ctx := context.Background()
	var counter int32
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	results, err := MapWithConcurrency(ctx, items, 0, func(ctx context.Context, item int) (int, error) {
		atomic.AddInt32(&counter, 1)
		return item * 10, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(counter) != 20 {
		t.Fatalf("expected 20 concurrent executions, got %d", counter)
	}
	for i, r := range results {
		if r != i*10 {
			t.Fatalf("results[%d]: expected %d, got %d", i, i*10, r)
		}
	}
}

func TestThrowIfAborted_NotCanceled(t *testing.T) {
	ctx := context.Background()
	if err := ThrowIfAborted(ctx); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestThrowIfAborted_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ThrowIfAborted(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestThrowIfAborted_DeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if err := ThrowIfAborted(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}
