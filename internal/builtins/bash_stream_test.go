package builtins

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// 增量输出 writer：既要累计完整输出（供截断/返回），又要节流回调（供 UI 实时显示）。
func TestStreamWriterAccumulates(t *testing.T) {
	var got []string
	w := newStreamWriter(func(chunk string) { got = append(got, chunk) }, 0)
	w.Write([]byte("hello "))
	w.Write([]byte("world"))
	w.Flush()
	if w.String() != "hello world" {
		t.Fatalf("累计内容错误: %q", w.String())
	}
}

// 回调应收到增量内容（含最终 flush），供 UI 显示。
func TestStreamWriterCallsBack(t *testing.T) {
	var mu sync.Mutex
	var chunks []string
	w := newStreamWriter(func(chunk string) {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
	}, 0)
	w.Write([]byte("line1\n"))
	w.Write([]byte("line2\n"))
	w.Flush()
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") {
		t.Fatalf("回调内容不完整: %v", chunks)
	}
}

// 节流：高频小写入不应每次都回调（避免刷爆 UI 事件循环）。
func TestStreamWriterThrottles(t *testing.T) {
	calls := 0
	w := newStreamWriter(func(string) { calls++ }, 50*time.Millisecond)
	for i := 0; i < 200; i++ {
		w.Write([]byte("x"))
	}
	// 节流窗口内不应有 200 次回调
	if calls > 5 {
		t.Fatalf("节流失效，回调 %d 次", calls)
	}
	// Flush 必须把尾部内容送出去
	w.Flush()
	if calls == 0 {
		t.Fatal("Flush 后应至少有一次回调")
	}
}

// 节流窗口内的小写入在 Flush 时不能丢内容。
func TestStreamWriterFlushDeliversTail(t *testing.T) {
	var last string
	w := newStreamWriter(func(chunk string) { last += chunk }, time.Hour)
	w.Write([]byte("important-output"))
	w.Flush()
	if !strings.Contains(last, "important-output") {
		t.Fatalf("Flush 未送出尾部内容: %q", last)
	}
}

// 并发写入安全（cmd.Wait 与读取 goroutine 可能并发）。
func TestStreamWriterConcurrentSafe(t *testing.T) {
	w := newStreamWriter(func(string) {}, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				w.Write([]byte("ab"))
			}
		}()
	}
	wg.Wait()
	w.Flush()
	if len(w.String()) != 8*50*2 {
		t.Fatalf("并发写入丢失内容，长度=%d", len(w.String()))
	}
}

// 回调为 nil 时不应 panic（无 UI 场景，如 Server 模式）。
func TestStreamWriterNilCallbackSafe(t *testing.T) {
	w := newStreamWriter(nil, 0)
	w.Write([]byte("data"))
	w.Flush()
	if w.String() != "data" {
		t.Fatalf("nil 回调下累计错误: %q", w.String())
	}
}

// SetBashStream 全局回调的设置与读取。
func TestSetBashStream(t *testing.T) {
	SetBashStream(nil)
	if bashStreamFn() != nil {
		t.Fatal("清空后应为 nil")
	}
	called := false
	SetBashStream(func(string) { called = true })
	if bashStreamFn() == nil {
		t.Fatal("设置后应非 nil")
	}
	SetBashStream(nil)
	_ = called
}
