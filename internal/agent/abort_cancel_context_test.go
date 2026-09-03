package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// cancellableScriptLLM 实现 LLMWithContext 接口：
// 在 ChatWithToolsCtx 中监听 ctx.Done()，模拟非流式路径也可被取消。
type cancellableScriptLLM struct {
	canceled chan struct{}
	callCnt  int
	mu       sync.Mutex
}

func (c *cancellableScriptLLM) Chat(messages []Message) (string, error) {
	return `{"action":"reply","text":"ok"}`, nil
}

func (c *cancellableScriptLLM) ChatWithTools(messages []Message, tools []plugins.Tool) (string, error) {
	return `{"action":"reply","text":"ok"}`, nil
}

// ChatWithToolsCtx 实现 LLMWithContext：在 ctx.Done 时立即返回
func (c *cancellableScriptLLM) ChatWithToolsCtx(ctx context.Context, messages []Message, tools []plugins.Tool) (string, error) {
	c.mu.Lock()
	c.callCnt++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		close(c.canceled)
		return "", ctx.Err()
	case <-time.After(10 * time.Second):
		return `{"action":"reply","text":"ok"}`, nil
	}
}

// TestAbortCancelsNonStreamLLMWithContext 验证 LLMWithContext 接口在 ESC 取消时能立即中断：
// 当 LLM 实现 LLMWithContext 而非 CancellableStreamToolCallLLM 时，
// 非流式路径也能通过 ctx 中断在途请求。
func TestAbortCancelsNonStreamLLMWithContext(t *testing.T) {
	pm := testManager(t)
	llm := &cancellableScriptLLM{canceled: make(chan struct{})}
	a := New(llm, pm)
	// 不设置 OnLLMStream → 走非流式路径
	// 但 LLM 实现了 LLMWithContext → 会调用 ChatWithToolsCtx 并接受 ctx

	done := make(chan error, 1)
	go func() {
		_, err := a.Run("task")
		done <- err
	}()
	// 等 LLM 调用开始
	time.Sleep(50 * time.Millisecond)
	a.Abort()

	select {
	case err := <-done:
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Abort within 5s (LLMWithContext cancel chain broken)")
	}
	// 确认在途请求确实收到 ctx 取消
	select {
	case <-llm.canceled:
	case <-time.After(time.Second):
		t.Fatal("LLMWithContext request context was not cancelled by Abort()")
	}
}

// TestAbortSkipsToolForNonStreamLLMWithContext 验证非流式路径 + LLMWithContext 时，
// 解析 LLM 回复后的 abort 检查点也能生效。
func TestAbortSkipsToolForNonStreamLLMWithContext(t *testing.T) {
	pm := testManager(t)
	var toolCalls int
	pm.RegisterBuiltin(plugins.Tool{
		Name: "ping",
		Run: func(args string) (string, error) {
			toolCalls++
			return "pong", nil
		},
	})

	// 第一轮 LLM 调用：返回工具调用
	// Abort 在 LLM 返回后、工具执行前触发
	innerLLM := &cancellableScriptLLM{canceled: make(chan struct{})}
	wrapper := &abortAfterFirstLLM{
		inner: innerLLM,
		abortCh: make(chan struct{}),
	}
	a := New(wrapper, pm)
	a.MaxSteps = 10

	go func() {
		time.Sleep(80 * time.Millisecond) // 等第一轮 LLM 返回
		a.Abort()
		close(wrapper.abortCh)
	}()

	_, err := a.Run("task")
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if toolCalls != 0 {
		t.Fatalf("tool executed %d time(s) after abort, want 0", toolCalls)
	}
}

// abortAfterFirstLLM 包装 LLMWithContext，第一轮返回工具调用，之后由调用方处理
type abortAfterFirstLLM struct {
	inner   *cancellableScriptLLM
	abortCh chan struct{}
	calls   int
	mu      sync.Mutex
}

func (w *abortAfterFirstLLM) Chat(messages []Message) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls == 1 {
		time.Sleep(50 * time.Millisecond)
		return `{"action":"tool","tool":"ping","args":"{\"x\":1}"}`, nil
	}
	return `{"action":"reply","text":"done"}`, nil
}

func (w *abortAfterFirstLLM) ChatWithToolsCtx(ctx context.Context, messages []Message, tools []plugins.Tool) (string, error) {
	// 转发给 inner，inner 支持 ctx 取消
	return w.inner.ChatWithToolsCtx(ctx, messages, tools)
}
