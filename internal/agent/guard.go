// Package agent 实现循环卫生守卫（复刻 deepseek-harness 的 repeat-tool-reminder）。
//
// 与 WeakModelTuner 的死循环检测互补：
//   - Tuner.RecordToolCall：窗口内全同即终止（强保护）
//   - RepeatGuard：先渐进提醒（gentle → detailed），超过最高阈值才终止（宽容纠偏）
//
// dsh 设计要点（已复刻）：
//   1. 参数规范化：JSON deep key-sort 后比较——键序不同的等价参数视为同一次调用（原始字符串比较会漏检）
//   2. 阈值分级：默认 [3, 5, 8]，首个阈值温和提醒，后续阈值详细提醒（点名工具/次数/参数预览）
//   3. 超过最高阈值仍重复 → 终止任务（避免无限烧 token）
//   4. 参数预览截断（默认 500 字符）：提醒文本有界，检测键始终用完整规范化串
package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RepeatGuardConfig RepeatGuard 配置。
type RepeatGuardConfig struct {
	// Thresholds 触发提醒的连续重复次数（升序、去重、每个 >= 2）。
	// 首个阈值发温和提醒，后续发详细提醒。默认 [3, 5, 8]。
	Thresholds []int
	// MaxArgsPreviewChars 详细提醒中参数预览的最大字符数（默认 500）。
	MaxArgsPreviewChars int
}

// RepeatGuard 循环卫生守卫：同一工具 + 规范化参数连续重复时渐进提醒，超过最高阈值终止。
// 非并发安全使用单 Agent 内，loop 串行调用 Observe，加锁保护 steer 场景下的并发读。
type RepeatGuard struct {
	mu    sync.Mutex
	cfg   RepeatGuardConfig
	key   string // 当前链的 key（tool|canonicalArgs）
	count int    // 当前链长度
}

// NewRepeatGuard 创建守卫。cfg 为 nil 或字段缺省时用默认值。
func NewRepeatGuard(cfg *RepeatGuardConfig) *RepeatGuard {
	g := &RepeatGuard{}
	if cfg != nil {
		g.cfg = *cfg
	}
	if len(g.cfg.Thresholds) == 0 {
		g.cfg.Thresholds = []int{3, 5, 8}
	} else {
		g.cfg.Thresholds = normalizeThresholds(g.cfg.Thresholds)
	}
	if g.cfg.MaxArgsPreviewChars <= 0 {
		g.cfg.MaxArgsPreviewChars = 500
	}
	return g
}

// normalizeThresholds 校验并规范化阈值：去重、升序、丢弃 < 2 的值。
// 全部无效时回退默认 [3, 5, 8]（宽松策略，不 fail-loud——mizar 定位弱模型宽容）。
func normalizeThresholds(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if v >= 2 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	if len(out) == 0 {
		out = []int{3, 5, 8}
	}
	return out
}

// Reset 重置链状态（新任务开始时调用）。
func (g *RepeatGuard) Reset() {
	g.mu.Lock()
	g.key = ""
	g.count = 0
	g.mu.Unlock()
}

// Count 返回当前链长度（线程安全读取，供日志/错误文本使用）。
func (g *RepeatGuard) Count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count
}

// MaxObserved 返回本次会话中观测到的最高链长度（含已重置的旧值）。
func (g *RepeatGuard) MaxObserved() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count
}

// Observe 记录一次工具调用，返回 (提醒文本, 是否终止)。
// 链断裂（工具或参数变化）时重新计数。命中阈值时返回提醒；
// 超过最高阈值时返回终止（此时提醒文本也给出，供日志记录）。
func (g *RepeatGuard) Observe(tool, args string) (reminder string, terminate bool) {
	canonical := CanonicalizeArgs(args)
	key := tool + "|" + canonical

	g.mu.Lock()
	defer g.mu.Unlock()

	if key == g.key {
		g.count++
	} else {
		g.key = key
		g.count = 1
	}

	th := g.cfg.Thresholds
	max := th[len(th)-1]
	if g.count > max {
		// 超过最高阈值仍重复：终止（提醒文本供日志/终止消息用）
		return detailedReminder(tool, g.count, previewArgs(canonical, g.cfg.MaxArgsPreviewChars)), true
	}
	for _, t := range th {
		if g.count == t {
			if t == th[0] {
				return gentleReminder, false
			}
			return detailedReminder(tool, g.count, previewArgs(canonical, g.cfg.MaxArgsPreviewChars)), false
		}
	}
	return "", false
}

// gentleReminder 首个阈值的温和提醒（对齐 dsh GENTLE_REMINDER 语义）。
const gentleReminder = "⚠️ 检测到你在用完全相同的参数重复调用同一个工具。" +
	"请仔细分析上一次的结果再决定是否重试：若任务未完成，请换一种方法或修改参数，而不是原样重发。"

// detailedReminder 后续阈值的详细提醒：点名工具、连续次数、参数预览（对齐 dsh detailedReminder）。
func detailedReminder(tool string, count int, argsPreview string) string {
	return fmt.Sprintf("⚠️ 重复工具调用检测：\n- 工具: %s\n- 连续次数: %d\n- 参数: %s\n"+
		"这些重复调用没有取得进展。不要再以这些参数调用该工具。请检查最近一次结果，"+
		"换一个动作、换一组参数，或确认证据已足够后直接输出最终回答。", tool, count, argsPreview)
}

// previewArgs 头部截断参数预览，超出标注省略字符数（对齐 dsh previewArguments）。
func previewArgs(canonical string, cap int) string {
	if len(canonical) <= cap {
		return canonical
	}
	return canonical[:cap] + fmt.Sprintf("…(+%d 字符省略)", len(canonical)-cap)
}

// CanonicalizeArgs 参数规范化：JSON deep key-sort 后序列化（对齐 dsh canonicalize/sortJsonValue）。
// 非 JSON 或解析失败时返回原文（比较仍可用，只是失去键序无关性）。
func CanonicalizeArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return trimmed
	}
	b, err := json.Marshal(sortJSONValue(v))
	if err != nil {
		return trimmed
	}
	return string(b)
}

// sortJSONValue 深度键排序：对象按键名字典序递归排序，数组保序。
func sortJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortJSONValue(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = sortJSONValue(t[i])
		}
		return t
	default:
		return v
	}
}
