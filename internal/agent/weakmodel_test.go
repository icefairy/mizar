package agent

import (
	"strings"
	"testing"
	"time"
)

// TestTunerParseEscalation 连续解析失败触发升级提示。
func TestTunerParseEscalation(t *testing.T) {
	tuner := DefaultTuner()
	tuner.Reset()

	// 前 2 次失败不升级（阈值 3）
	for i := 0; i < 2; i++ {
		escalated, _ := tuner.ParseFailed(i)
		if escalated {
			t.Fatalf("step %d: 不应升级", i)
		}
	}
	// 第 3 次升级（failures=3 >= maxParseFailures=3）
	escalated, msg := tuner.ParseFailed(2)
	if !escalated {
		t.Fatal("第 4 次失败应升级")
	}
	if !strings.Contains(msg, "已连续 3 次") {
		t.Fatalf("升级消息应包含次数: %q", msg)
	}

	// 成功后重置
	tuner.ParseSucceeded()
	escalated, _ = tuner.ParseFailed(0)
	if escalated {
		t.Fatal("重置后首次失败不应升级")
	}
}

// TestTunerDeadLoop 相同工具调用检测死循环。
func TestTunerDeadLoop(t *testing.T) {
	tuner := DefaultTuner()
	tuner.Reset()

	for i := 0; i < 2; i++ {
		if tuner.RecordToolCall("bash", "ls") {
			t.Fatalf("第 %d 次调用不应判死循环", i+1)
		}
	}
	// 第 3 次相同调用 → 窗口满，检测到
	if !tuner.RecordToolCall("bash", "ls") {
		t.Fatal("第 3 次相同调用应检测到死循环")
	}

	// 不同参数不算死循环
	tuner.Reset()
	tuner.RecordToolCall("read", "a")
	tuner.RecordToolCall("read", "b")
	tuner.RecordToolCall("bash", "ls")
	if tuner.RecordToolCall("bash", "ls") {
		t.Fatal("不同参数不应判死循环")
	}
}

// TestTunerLLMRetry LLM 失败重试上限。
func TestTunerLLMRetry(t *testing.T) {
	tuner := DefaultTuner()
	tuner.Reset()

	for i := 1; i <= 2; i++ {
		shouldRetry, count := tuner.LLMFailed()
		if !shouldRetry || count != i {
			t.Fatalf("第 %d 次失败应重试 (got retry=%v count=%d)", i, shouldRetry, count)
		}
	}
	// 第 3 次失败超限
	shouldRetry, _ := tuner.LLMFailed()
	if shouldRetry {
		t.Fatal("超过 maxLLMRetries 不应再重试")
	}

	// 成功后重置
	tuner.LLMSucceeded()
	shouldRetry, count := tuner.LLMFailed()
	if !shouldRetry || count != 1 {
		t.Fatal("重置后应可重试")
	}
}

// TestTunerInAgentLoop Tuner 接入 Agent 循环：连续解析失败后提示升级。
func TestTunerInAgentLoop(t *testing.T) {
	pm := testManager(t)
	tuner := DefaultTuner()
	var prompts []string

	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		// 记录所有用户消息（升级提示也应出现）
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == RoleUser {
				prompts = append(prompts, msgs[i].Content)
				break
			}
		}
		return `不规范的回复，不是 JSON`, nil
	}}
	a := New(llm, pm)
	a.Tuner = tuner
	a.MaxSteps = 8

	_, _ = a.Run("task")
	// 应出现升级提示（含"示例"）
	found := false
	for _, p := range prompts {
		if strings.Contains(p, "示例") || strings.Contains(p, "连续") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("未出现升级提示, prompts=%v", prompts)
	}
}

// TestTunerRetryDelay 验证可配置退避延迟。
func TestTunerRetryDelay(t *testing.T) {
	// 默认 1s 起跳
	t.Run("default", func(t *testing.T) {
		tuner := DefaultTuner()
		if d := tuner.RetryDelay(1); d != 1000*time.Millisecond {
			t.Fatalf("expected 1s, got %v", d)
		}
		if d := tuner.RetryDelay(2); d != 2000*time.Millisecond {
			t.Fatalf("expected 2s, got %v", d)
		}
	})
	// 自定义 baseDelayMs
	t.Run("custom", func(t *testing.T) {
		tuner := DefaultTuner().WithRetryConfig(3, 500)
		if d := tuner.RetryDelay(1); d != 500*time.Millisecond {
			t.Fatalf("expected 500ms, got %v", d)
		}
		if d := tuner.RetryDelay(2); d != 1000*time.Millisecond {
			t.Fatalf("expected 1s, got %v", d)
		}
	})
}
