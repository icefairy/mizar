package builtins

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// 集成验证：真实执行 bash 命令时，增量回调应收到输出（而非只在结束后一次性返回）。
func TestBashToolStreamsRealCommand(t *testing.T) {
	var mu sync.Mutex
	var chunks []string
	SetBashStream(func(chunk string) {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
	})
	defer SetBashStream(nil)

	tool := toolBash()
	// 分两段输出，中间 sleep：验证第一段在命令结束前就被回调
	out, err := tool.Run(`{"command":"echo first; sleep 0.3; echo second","timeout":10}`)
	if err != nil {
		t.Fatalf("bash 执行失败: %v", err)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("返回值应含全部输出: %q", out)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") {
		t.Fatalf("增量回调应收到两段输出，实际: %q", joined)
	}
}

// 无回调（Server/CI）时行为不变：仍返回完整输出。
func TestBashToolNoStreamCallback(t *testing.T) {
	SetBashStream(nil)
	tool := toolBash()
	out, err := tool.Run(`{"command":"echo hello-no-stream","timeout":10}`)
	if err != nil {
		t.Fatalf("bash 执行失败: %v", err)
	}
	if !strings.Contains(out, "hello-no-stream") {
		t.Fatalf("无回调时仍应返回输出: %q", out)
	}
}

// 超时路径：kill 后仍应 Flush 已产生的输出（不能因为超时把前面的输出吞掉）。
func TestBashToolTimeoutStillFlushesOutput(t *testing.T) {
	var mu sync.Mutex
	var chunks []string
	SetBashStream(func(chunk string) {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
	})
	defer SetBashStream(nil)

	tool := toolBash()
	out, err := tool.Run(`{"command":"echo before-timeout; sleep 5","timeout":1}`)
	if err == nil {
		t.Fatal("应超时报错")
	}
	if !strings.Contains(out, "before-timeout") {
		t.Fatalf("超时后返回值仍应含超时前的输出: %q", out)
	}
	// 给 Flush 一点时间（defer 在 Run 返回前已执行，这里主要防 goroutine 竞争）
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(chunks, ""), "before-timeout") {
		t.Fatalf("超时前输出应已回调: %q", strings.Join(chunks, ""))
	}
}
