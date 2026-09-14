package main

// 回归测试：startTask 在同步路径上不得出现「解锁未持有的锁」。
//
// 背景：fed0420 在流式回调闭包结束后多写了一次 m.streamMu.Unlock()，而该锁
// 已在循环内释放。TUI 里每次发消息都会触发
//
//	fatal error: sync: unlock of unlocked mutex
//
// fatal error 不属于普通 panic，recover 无法拦截，进程直接退出。
import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// stubLLM 立即返回一条回复，让后台任务 goroutine 快速干净退出。
type stubLLM struct{}

func (stubLLM) Chat(msgs []agent.Message) (string, error) {
	return `{"action":"reply","text":"ok"}`, nil
}

// scriptedTUI 构造一个 agent 完整、可安全后台运行的 tuiModel。
func scriptedTUI(t *testing.T) *tuiModel {
	t.Helper()
	dir := t.TempDir()
	pm := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := pm.LoadAll(); len(failed) > 0 {
		t.Fatalf("plugin load failed: %v", failed)
	}
	m := newTUIForQueueTest()
	m.agent = agent.New(stubLLM{}, pm)
	m.addChatLine(chatLine{role: "user", content: "上一条"}) // 让持久化分支可走到
	return m
}

// TestStartTaskNoUnlockOfUnlockedMutex 直接调用 startTask，确认同步路径不死锁、
// 不因解锁未持有的 mutex 而崩溃。
func TestStartTaskNoUnlockOfUnlockedMutex(t *testing.T) {
	m := scriptedTUI(t)

	done := make(chan interface{}, 1)
	go func() {
		defer func() { done <- recover() }()
		m.startTask("hi")
	}()

	select {
	case r := <-done:
		if r == nil {
			return // 正常返回
		}
		if s := fmt.Sprint(r); strings.Contains(s, "unlock of unlocked mutex") {
			t.Fatalf("startTask 解锁未持有的 mutex（进程会 fatal 退出）: %v", r)
		}
		t.Fatalf("startTask panic: %v", r)
	case <-time.After(5 * time.Second):
		t.Fatal("startTask 超时未返回（死锁）")
	}
}
