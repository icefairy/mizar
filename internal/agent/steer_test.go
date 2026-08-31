package agent

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// TestSteerInjectedBeforeNextLLM 循环中发送 steer，下一轮 LLM 调用应看到纠正消息。
func TestSteerInjectedBeforeNextLLM(t *testing.T) {
	pm := testManager(t)
	var mu sync.Mutex
	var seen []string
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		var last string
		for _, m := range msgs {
			last = m.Content
		}
		seen = append(seen, last)
		// 第一轮返回工具调用（模拟 agent 开始干活），第二轮返回 reply
		if len(seen) == 1 {
			return `{"action":"tool","tool":"ping","args":""}`, nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}}

	a := New(llm, pm)
	// 第一步 LLM 调用耗时 60ms，给 goroutine 留出注入窗口
	orig := llm.fn
	llm.fn = func(msgs []Message) (string, error) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n == 0 {
			time.Sleep(60 * time.Millisecond)
		}
		return orig(msgs)
	}
	// 用 goroutine 在第一步 LLM 调用期间注入 steer
	go func() {
		time.Sleep(20 * time.Millisecond)
		a.Steer("停，方向错了，改成做别的事")
	}()

	reply, err := a.Run("task")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q", reply)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("llm calls = %d, want >= 2", len(seen))
	}
	// 第二轮 LLM 输入的最后一条应包含 steer 内容
	if !strings.Contains(seen[1], "停，方向错了") {
		t.Errorf("第二轮未见纠正消息; last=%q", seen[1])
	}
}

// TestSteerLatestWins 多条 steer 只保留最新一条。
func TestSteerLatestWins(t *testing.T) {
	pm := testManager(t)
	a := New(&scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"done"}`, nil
	}}, pm)
	a.Steer("first")
	a.Steer("second")
	a.Steer("third")
	sm := a.drainSteer()
	if sm == nil || sm.content != "third" {
		t.Fatalf("drain = %+v, want third", sm)
	}
	if a.drainSteer() != nil {
		t.Fatal("drain should be empty after one drain")
	}
}

// TestAbortStopsLoop 调用 Abort 后循环停止，返回 ErrAborted。
func TestAbortStopsLoop(t *testing.T) {
	pm := testManager(t)
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		time.Sleep(15 * time.Millisecond) // 每步耗时，给 Abort 留窗口
		return `{"action":"tool","tool":"ping","args":""}`, nil
	}}
	a := New(llm, pm)
	a.MaxSteps = 100
	go func() {
		time.Sleep(40 * time.Millisecond)
		a.Abort()
	}()
	_, err := a.Run("task")
	if err != ErrAborted {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
}
// TestAbortSkipsToolExecutionAfterLLM 模拟 ESC 在 LLM 请求进行中按下：
// LLM 已返回工具调用，但回复到达时用户已按 ESC。循环必须在执行工具【前】退出，
// 不再执行任何工具（修复点：解析回复后、工具执行前的 abort 检查点）。
func TestAbortSkipsToolExecutionAfterLLM(t *testing.T) {
	pm := testManager(t)
	// 用 Go 侧内置工具替换插件 ping，以便统计真实调用次数
	var toolCalls int
	pm.RegisterBuiltin(plugins.Tool{
		Name: "ping",
		Run: func(args string) (string, error) {
			toolCalls++
			return "pong", nil
		},
	})

	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		// 模拟慢 LLM 请求：给外部 goroutine 留出按 ESC 的窗口
		time.Sleep(15 * time.Millisecond)
		return `{"action":"tool","tool":"ping","args":"{\"x\":1}"}`, nil
	}}
	a := New(llm, pm)
	a.MaxSteps = 10
	// 外部 goroutine：LLM 调用正在进行时请求中止（等价于 TUI 的 ESC → agent.Abort()）
	go func() {
		time.Sleep(2 * time.Millisecond)
		a.Abort()
	}()
	_, err := a.Run("task")
	if err != ErrAborted {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if toolCalls != 0 {
		t.Fatalf("tool executed %d time(s) after abort, want 0", toolCalls)
	}
}
