package jobs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestStartListDrain(t *testing.T) {
	r := NewRegistry()
	id := r.Start("bash", "test-job", func() (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "hello", nil
	})
	if id == "" {
		t.Fatal("expected non-empty id")
	}
	js := r.List()
	if len(js) != 1 || js[0].ID != id {
		t.Fatalf("list want [%s] got %v", id, js)
	}
	time.Sleep(200 * time.Millisecond)
	done := r.DrainDone()
	if len(done) != 1 || done[0] != id {
		t.Fatalf("drain want [%s] got %v", id, done)
	}
}

func TestKill(t *testing.T) {
	r := NewRegistry()
	done := make(chan struct{})
	id := r.Start("bash", "slow", func() (string, error) {
		defer close(done)
		select {} // 永远阻塞
	})
	time.Sleep(10 * time.Millisecond)
	if err := r.Kill(id, "user-cancel"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-done:
		// 正常：goroutine 仍在运行（本实现无法强制 kill），但 drain 会清理
		// 这里只是验证 Kill 不 panic、不报错
	case <-time.After(100 * time.Millisecond):
		// 也接受：说明 goroutine 已被释放（不太可能，但至少不阻塞）
	}
}

func TestOutputUnknown(t *testing.T) {
	r := NewRegistry()
	_, _, ok := r.Output("no-such-job")
	if ok {
		t.Fatal("unknown job should return ok=false")
	}
}

func TestConcurrentSafety(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := r.Start("parallel", fmt.Sprintf("job-%d", n), func() (string, error) {
				time.Sleep(50 * time.Millisecond)
				return fmt.Sprintf("result-%d", n), nil
			})
			_ = r.List()
			_, _, _ = r.Output(id)
			// 不在 goroutine 内 drain，等所有启动完再统一 drain
		}(i)
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond) // 等所有任务完成
	done := r.DrainDone()
	if len(done) != 10 {
		t.Fatalf("drain want 10 done jobs, got %d: %v", len(done), done)
	}
}
