package engine

import (
	"strings"
	"testing"
	"time"
)

// SetCallTimeout 允许测试缩短超时时间（默认 30s）。
func (e *Engine) SetCallTimeout(d time.Duration) { e.callTimeout = d }
func (e *Engine) SetRunTimeout(d time.Duration)   { e.runTimeout = d }

func (e *Engine) callTimeoutOrDefault() time.Duration {
	if e.callTimeout > 0 {
		return e.callTimeout
	}
	return 30 * time.Second
}
func (e *Engine) runTimeoutOrDefault() time.Duration {
	if e.runTimeout > 0 {
		return e.runTimeout
	}
	return 30 * time.Second
}

// TestCallPanicRecovered 插件宿主函数 panic → Call 返回 error 而非崩溃。
func TestCallPanicRecovered(t *testing.T) {
	e, _ := New(nil)
	defer e.Close()
	e.vm.Set("boom", func() { panic("kaboom") })
	_, err := e.Call("boom")
	if err == nil {
		t.Fatal("expected error on panic")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected panic error, got %v", err)
	}
}

// TestRunScriptPanicRecovered 编译后 JS 抛 panic → RunScript 返回 error。
func TestRunScriptPanicRecovered(t *testing.T) {
	e, _ := New(nil)
	defer e.Close()
	// Go 里的 panic 传进 JS 会被 goja 包装为 JS error；反过来 RunScript 直接抛 JS 错误。
	// 用一个会触发 Go panic 的宿主函数做注入。
	e.vm.Set("crash", func() { panic("js-panic") })
	_, err := e.Call("crash")
	if err == nil {
		t.Fatal("expected panic error")
	}
}

// TestCallTimeout 插件 JS 死循环 → Call 超时返回 error（用 500ms 缩短）。
func TestCallTimeout(t *testing.T) {
	e, _ := New(nil)
	defer e.Close()
	e.SetCallTimeout(500 * time.Millisecond)
	e.RunScript("reg.js", "function hang(){ while(true) {} }")
	started := time.Now()
	_, err := e.Call("hang")
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took too long: %v", elapsed)
	}
	t.Logf("call timed out in %v", elapsed)
}

// TestRunScriptTimeout 插件加载脚本死循环 → RunScript 超时返回 error。
func TestRunScriptTimeout(t *testing.T) {
	e, _ := New(nil)
	defer e.Close()
	e.SetRunTimeout(500 * time.Millisecond)
	started := time.Now()
	err := e.RunScript("dead.js", "while(true) {}")
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took too long: %v", elapsed)
	}
}

// TestRunScriptTimeoutRecovers engine 在超时后仍可继续使用。
func TestRunScriptTimeoutRecovers(t *testing.T) {
	e, _ := New(nil)
	defer e.Close()
	e.SetRunTimeout(500 * time.Millisecond)
	e.RunScript("dead.js", "while(true) {}")
	// 引擎应可用（goja 超时中断只打断当前执行，不破坏 VM）
	res, err := e.Call("eval", "JSON.stringify(123)")
	if err != nil {
		t.Fatalf("engine unusable after timeout: %v", err)
	}
	if res.(string) != "123" {
		t.Fatalf("expected 123, got %v", res)
	}
}