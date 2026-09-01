// Package agent 实现弱模型宽容策略：当模型反复出错时，自动降级提示、
// 限流、检测死循环，尽力给部分结果而非彻底崩溃。
package agent

import (
	"fmt"
	"sync/atomic"
	"time"
)

// WeakModelTuner 弱模型宽容策略。
//
// 弱模型常见问题：
// 1. JSON 解析失败反复出现 → 提示升级（简单→严格→示例）
// 2. 同一工具重复调用 → 检测死循环，强制打断
// 3. LLM 临时错误 → 自动重试
// 4. 步数耗尽 → 尽力给部分结果
type WeakModelTuner struct {
	// 错误容忍度
	maxParseFailures int // 连续解析失败阈值（默认 3）
	maxLLMRetries    int // LLM 失败重试次数（默认 2）
	maxStepLimit     int // 单任务步数上限（默认 50）

	// 死循环检测：记录最近 N 个工具调用，若连续重复则中断
	loopWindowSize int // 窗口大小（默认 3）

	// LLM 重试退避
	retryBaseDelayMs int // 初始退避毫秒（默认 1000）

	// 状态
	parseFailures        atomic.Int32 // 当前连续解析失败数
	consecutiveToolCalls atomic.Value // 最近 N 个工具调用
	llmRetryCount        atomic.Int32 // LLM 当前重试次数

	// 回调
	onParseEscalate func(step int, failures int) string // 返回升级后的提示文案
}

// DefaultTuner 创建默认宽容策略。
func DefaultTuner() *WeakModelTuner {
	return &WeakModelTuner{
		maxParseFailures: 3,
		maxLLMRetries:    2,
		maxStepLimit:     50,
		loopWindowSize:   3,
	}
}

// WithRetryConfig 用配置中的 retry 参数覆盖默认值（0 = 保持默认）。
func (t *WeakModelTuner) WithRetryConfig(maxRetries, baseDelayMs int) *WeakModelTuner {
	if maxRetries > 0 {
		t.maxLLMRetries = maxRetries
	}
	if baseDelayMs > 0 {
		t.retryBaseDelayMs = baseDelayMs
	}
	return t
}

// Reset 重置所有计数（新任务开始时调用）。
func (t *WeakModelTuner) Reset() {
	t.parseFailures.Store(0)
	t.llmRetryCount.Store(0)
	t.consecutiveToolCalls.Store(make([]string, 0, t.loopWindowSize))
}

// ParseFailed 记录一次解析失败，返回是否已超阈值。
// 返回 (shouldEscalate bool, escalateMsg string)
func (t *WeakModelTuner) ParseFailed(step int) (bool, string) {
	failures := t.parseFailures.Add(1)
	if failures >= int32(t.maxParseFailures) {
		msg := t.buildEscalationMsg(failures, step)
		return true, msg
	}
	return false, ""
}

// ParseSucceeded 重置解析失败计数。
func (t *WeakModelTuner) ParseSucceeded() {
	t.parseFailures.Store(0)
}

// RecordToolCall 记录一次工具调用，检测死循环。
// 返回是否检测到重复（dead loop）。
func (t *WeakModelTuner) RecordToolCall(tool string, args string) bool {
	key := fmt.Sprintf("%s|%s", tool, args)
	calls, _ := t.consecutiveToolCalls.Load().([]string)
	calls = append(calls, key)
	// 保持窗口大小
	if len(calls) > t.loopWindowSize {
		calls = calls[len(calls)-t.loopWindowSize:]
	}
	t.consecutiveToolCalls.Store(calls)

	if len(calls) < t.loopWindowSize {
		return false
	}

	// 检查窗口内是否全部相同
	first := calls[0]
	for _, c := range calls[1:] {
		if c != first {
			return false
		}
	}
	return true
}

// LLMFailed 记录一次 LLM 失败，返回重试次数。
// 返回 (shouldRetry bool, retryCount int)
func (t *WeakModelTuner) LLMFailed() (bool, int) {
	retry := t.llmRetryCount.Add(1)
	if int(retry) <= t.maxLLMRetries {
		return true, int(retry)
	}
	return false, int(retry)
}

// LLMSucceeded 重置 LLM 重试计数。
func (t *WeakModelTuner) LLMSucceeded() {
	t.llmRetryCount.Store(0)
}

// buildEscalationMsg 构建逐级升级的解析失败提示。
func (t *WeakModelTuner) buildEscalationMsg(failures int32, step int) string {
	if t.onParseEscalate != nil {
		return t.onParseEscalate(step, int(failures))
	}

	base := fmt.Sprintf(
		"⚠️  已连续 %d 次解析失败（step=%d）。请严格按以下格式输出，不要有其他内容：",
		failures, step,
	)
	switch {
	case failures <= 3:
		return base + `\n{"action":"tool","tool":"工具名","args":"参数"}\n任务完成时：{"action":"reply","text":"最终回答"}`
	case failures <= 5:
		return base + `\n\n示例：
如果任务是"查看文件 /tmp/x.go"，正确格式：
{"action":"tool","tool":"read","args":"{\"path\":\"/tmp/x.go\"}"}

不要输出代码块标记，不要输出解释文字，只输出 JSON 对象本身。`
	default:
		return base + `\n\n你已连续失败多次。请只输出这一行（不带任何标记、不加注释）：
{"action":"reply","text":"抱歉，我暂时无法完成任务。"}`
	}
}

// MaxStepsExceeded 判断是否应尽力给部分结果。
// 当接近 maxStepLimit 且尚未达到时，返回建议的剩余步数。
func (t *WeakModelTuner) RemainingSteps(current, maxSteps int) (int, bool) {
	// 限制实际步数不超过 maxStepLimit
	lim := t.maxStepLimit
	if maxSteps > lim {
		maxSteps = lim
	}
	remaining := maxSteps - current
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

// RetryDelay 返回 LLM 重试的退避等待时间。
// 指数退避：baseDelayMs * 2^(retryCount-1)
func (t *WeakModelTuner) RetryDelay(retryCount int) time.Duration {
	base := t.retryBaseDelayMs
	if base <= 0 {
		base = 1000 // 默认 1s
	}
	return time.Duration(base*(1<<uint(retryCount-1))) * time.Millisecond
}
