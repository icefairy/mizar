package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// cancelCtxLLM 实现 CancellableStreamToolCallLLM 的 mock：
// 记录收到的 ctx，并阻塞监听 ctx.Done()，模拟「正在流式读取中的在途请求」。
type cancelCtxLLM struct {
	mu        sync.Mutex
	gotCtx    context.Context
	canceled  chan struct{} // ctx 被 cancel 时关闭
	callCount int
}

// Chat 实现 LLM 接口（非流式路径不会用到，流式优先走下方方法）。
func (c *cancelCtxLLM) Chat(msgs []Message) (string, error) {
	c.mu.Lock()
	c.callCount++
	c.mu.Unlock()
	return `{"action":"reply","text":"ok"}`, nil
}

// ChatWithToolsStreamCtx 实现 CancellableStreamToolCallLLM：阻塞等待 ctx.Done。
func (c *cancelCtxLLM) ChatWithToolsStreamCtx(ctx context.Context, msgs []Message, tools []plugins.Tool, onToken func(StreamDelta)) (string, error) {
	c.mu.Lock()
	c.callCount++
	c.gotCtx = ctx
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		close(c.canceled)
		return "", ctx.Err()
	case <-time.After(10 * time.Second):
		return `{"action":"reply","text":"slow stream finished"}`, nil
	}
}

// TestAbortCancelsRunContext 验证 Abort() 取消当前 Run 的上下文：
// ESC 取消时，使用 ChatWithToolsStreamCtx 的在途 LLM 请求能立即中断，
// Run 返回 ErrAborted（而不是阻塞到流自然结束）。
func TestAbortCancelsRunContext(t *testing.T) {
	pm := testManager(t)
	llm := &cancelCtxLLM{canceled: make(chan struct{})}
	a := New(llm, pm)
	a.OnLLMStream = func(step int, d StreamDelta) {} // 走流式路径

	done := make(chan error, 1)
	go func() {
		_, err := a.Run("task")
		done <- err
	}()
	// 等 LLM 调用开始，然后 ESC 取消
	time.Sleep(50 * time.Millisecond)
	a.Abort()

	select {
	case err := <-done:
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Abort within 5s (cancel chain broken)")
	}
	// 确认在途请求确实收到 ctx 取消
	select {
	case <-llm.canceled:
	case <-time.After(time.Second):
		t.Fatal("LLM request context was not cancelled by Abort()")
	}
	// Abort 后 Run 的取消上下文应被清理（defer setRunCancel(nil, nil)）
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.runCtx != nil {
		t.Fatal("runCtx not cleared after Run returned")
	}
}

// TestAbortDuringLLMErrorReturnsAborted 验证 ctx 取消产生的 llmErr 在 Aborted 状态下
// 直接返回 ErrAborted，不走 Tuner 重试（否则 abort 后可能因重试继续空转）。
func TestAbortDuringLLMErrorReturnsAborted(t *testing.T) {
	pm := testManager(t)
	llm := &cancelCtxLLM{canceled: make(chan struct{})}
	a := New(llm, pm)
	a.OnLLMStream = func(step int, d StreamDelta) {}
	// 启用 Tuner：如果 abort 检查不生效，llmErr 会走重试路径导致额外 LLM 调用
	a.Tuner = &WeakModelTuner{}

	go func() {
		time.Sleep(50 * time.Millisecond)
		a.Abort()
	}()
	_, err := a.Run("task")
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	llm.mu.Lock()
	cnt := llm.callCount
	llm.mu.Unlock()
	if cnt > 1 {
		t.Fatalf("LLM called %d times after abort, want 1", cnt)
	}
}

// TestAbortWithoutRunNilSafe 无活跃 Run 时 Abort() 不 panic（runCancel 为 nil）。
func TestAbortWithoutRunNilSafe(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"ok"}`, nil
	}}, pm)
	a.Abort() // 不应 panic
}
