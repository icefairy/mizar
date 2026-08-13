// AbortReason 归一化与分类（参考 openclaude abortReasons.ts）
//
// mizar 在 Agent 循环中有多个退出路径：用户主动 abort、超时、max steps、
// LLM 错误、工具错误等。以前只返回 error 字符串，诊断困难。
//
// 现在统一为 AbortReason 枚举 + NormalizeAbortReason 归一化函数：
//   - 接受 string / error / unknown，输出统一枚举
//   - 支持别名映射（如 "user-cancel" → AbortUserAbort）
//   - 每种原因有对应的用户友好文案（可通过 AbortReasonMessage 获取）
package utils

import (
	"errors"

	"mizar/internal/agent"
)

// AbortReason 所有可能的中止/退出原因。
type AbortReason string

const (
	AbortUserAbort   AbortReason = "user-abort"   // 用户主动中止
	AbortTimeout     AbortReason = "timeout"      // 操作超时
	AbortMaxSteps    AbortReason = "max-steps"    // 循环步数耗尽
	AbortBackground  AbortReason = "background"   // 查询被后台化
	AbortParentEnded AbortReason = "parent-ended" // 父级任务结束
	AbortToolError   AbortReason = "tool-error"   // 工具执行失败
	AbortLLMError    AbortReason = "llm-error"    // LLM 调用失败
	AbortUnknown     AbortReason = "unknown"      // 未知原因
)

// KnownAbortReasons 所有已知原因的集合（用于校验）。
var KnownAbortReasons = map[AbortReason]struct{}{
	AbortUserAbort:   {},
	AbortTimeout:     {},
	AbortMaxSteps:    {},
	AbortBackground:  {},
	AbortParentEnded: {},
	AbortToolError:   {},
	AbortLLMError:    {},
	AbortUnknown:     {},
}

// aliasMap 做别名归一化。
var aliasMap = map[string]AbortReason{
	"user-cancel":         AbortUserAbort,
	"hard-max":            AbortMaxSteps,
	"tool-timeout":        AbortTimeout,
	"side-task-cancelled": AbortParentEnded,
}

// NormalizeAbortReason 将任意值归一化为 AbortReason。
// 接受 string、error（通过类型名推断）或 AbortReason。
func NormalizeAbortReason(reason any) AbortReason {
	switch v := reason.(type) {
	case AbortReason:
		if _, ok := KnownAbortReasons[v]; ok {
			return v
		}
		return AbortUnknown
	case string:
		// 先查别名表
		if alias, ok := aliasMap[v]; ok {
			return alias
		}
		// 再查已知集合
		if _, ok := KnownAbortReasons[AbortReason(v)]; ok {
			return AbortReason(v)
		}
		return AbortUnknown
	case error:
		if v == nil {
			return AbortUnknown
		}
		errStr := v.Error()
		// 特殊错误归一化
		if errors.Is(v, agent.ErrAborted) {
			return AbortUserAbort
		}
		if errors.Is(v, agent.ErrTimeout) {
			return AbortTimeout
		}
		if errors.Is(v, agent.ErrMaxSteps) {
			return AbortMaxSteps
		}
		// 回退到字符串处理
		return NormalizeAbortReason(errStr)
	default:
		return AbortUnknown
	}
}

// AbortReasonMessage 返回每种原因对应的用户友好文案。
func AbortReasonMessage(reason AbortReason) string {
	switch reason {
	case AbortUserAbort:
		return "操作被用户主动中止。"
	case AbortTimeout:
		return "操作因超时而被中止。"
	case AbortMaxSteps:
		return "循环步数已耗尽（超过最大步数限制）。"
	case AbortBackground:
		return "操作因查询被后台化而中止。"
	case AbortParentEnded:
		return "操作因父级任务结束而中止。"
	case AbortToolError:
		return "工具执行失败，循环已中止。"
	case AbortLLMError:
		return "LLM 调用失败，循环已中止。"
	case AbortUnknown:
		return "操作因未知原因中止。"
	default:
		return "操作已中止。"
	}
}

// IsExpectedAbort 判断该原因是否为"预期内"的终止（不视为异常）。
func IsExpectedAbort(reason AbortReason) bool {
	switch reason {
	case AbortUserAbort, AbortTimeout, AbortMaxSteps, AbortBackground, AbortParentEnded:
		return true
	default:
		return false
	}
}
