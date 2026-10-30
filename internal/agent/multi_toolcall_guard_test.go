package agent

import (
	"strings"
	"testing"

	"mizar/internal/plugins"
)

// 回归：同一轮内的多个调用参数完全相同时，Guard 会在第一个调用上命中阈值并返回
// controlNextStep。旧实现直接 break 丢弃剩余调用——模型的并行写操作凭空消失。
// 正确行为：guard 只做「提醒/终止」的观察者，不能吞掉同一批里已解析出的调用。
func TestRunDoesNotDropSiblingCallsWhenGuardReminds(t *testing.T) {
	pm := testManager(t)
	var executed []string
	for _, name := range []string{"t_a", "t_b", "t_c"} {
		n := name
		pm.RegisterBuiltin(plugins.Tool{
			Name:        n,
			Description: "test tool",
			Run: func(args string) (string, error) {
				executed = append(executed, n)
				return "ok:" + n, nil
			},
		})
	}

	// 阈值 [2,3,4]：第一轮三个调用的参数完全相同（v=1）→ 第 2 个调用即命中温和提醒。
	a := New(&scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, m := range msgs {
			if m.Kind == KindToolResult {
				return `{"action":"reply","text":"done"}`, nil
			}
		}
		return `{"action":"tool","calls":[` +
			`{"tool":"t_a","args":"{\"v\":1}"},` +
			`{"tool":"t_b","args":"{\"v\":1}"},` +
			`{"tool":"t_c","args":"{\"v\":1}"}` +
			`],"tool":"t_a","args":"{\"v\":1}"}`, nil
	}}, pm)
	a.Guard = NewRepeatGuard(&RepeatGuardConfig{Thresholds: []int{2, 3, 4}})

	if _, err := a.Run("task"); err != nil {
		t.Fatal(err)
	}
	if len(executed) != 3 {
		t.Fatalf("guard 提醒不应吞掉同批调用：期望 3 个都执行，实际 %v", executed)
	}
}

// 回归：终止由 guard 触发时，错误文本必须点名真正重复的工具与次数，
// 不能把首个调用的工具名硬编码进消息（多调用时首调用未必是重复的那个）。
func TestGuardTerminationMessageNamesOffendingTool(t *testing.T) {
	pm := testManager(t)
	pm.RegisterBuiltin(plugins.Tool{
		Name: "t_x", Description: "test",
		Run: func(args string) (string, error) { return "ok", nil },
	})
	// 单个调用连续重复，超出最高阈值 → 终止
	a := New(&scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"tool","tool":"t_x","args":"{\"v\":1}"}`, nil
	}}, pm)
	a.Guard = NewRepeatGuard(&RepeatGuardConfig{Thresholds: []int{2}})
	a.MaxSteps = 10

	_, err := a.Run("task")
	if err == nil {
		t.Fatal("超出阈值应终止")
	}
	if !strings.Contains(err.Error(), "t_x") {
		t.Fatalf("终止信息应点名工具 t_x: %v", err)
	}
}
