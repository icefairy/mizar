package agent

import "fmt"

// completenessMaxNudges 单次 Run 内允许的最大「完成度续跑」次数。
//
// 为什么需要上限：模型可能反复无视提醒，每次续跑都要完整走一轮 LLM
// （输入 = 全部历史，最贵的那一种）。上限存在的目的是「给它台阶，但不给它无限预算」。
// 达到上限后放行模型回复——此时宁可由用户看到「它说做完了但其实没做」，
// 也不要静默把 token 烧穿。
const completenessMaxNudges = 2

// CompletenessGate 完成度自检闸门。
//
// 解决的问题：mizar 的 callRequest 是 {action:"tool"|"reply"} 的互斥枚举，
// reply 在循环里语义写死为「任务结束」。模型只要说一句话就结束本轮——
// 于是「干了一半、说句『好，接下来…』」= 任务静默停止，用户必须手打「继续」。
//
// 闸门策略（刻意保守）：只在**存在客观未完成项**时拦截收尾，而这些未完成项
// 只来自待办清单（todo 工具）。没有待办就不干预——纯问答、单步任务不该多烧一轮 LLM，
// 也不该被文本启发式误判（「我将…」这种措辞在正常回答里太常见）。
//
// Pending 由调用方注入，避免 agent → builtins 的循环依赖。
type CompletenessGate struct {
	// Pending 返回当前所有**未完成**的待办文本（nil = 不做完成度检查）。
	Pending func() []string

	nudges int
}

// NewCompletenessGate 创建闸门。pending 可为 nil（等价于关闭检查）。
func NewCompletenessGate(pending func() []string) *CompletenessGate {
	return &CompletenessGate{Pending: pending}
}

// Reset 重置续跑计数（每次 Run 开始时调用）。
func (g *CompletenessGate) Reset() {
	g.nudges = 0
}

// ShouldContinue 判断「模型声称收尾」是否应被拦截。
// 返回 (提醒文本, true) 表示拦截并续跑；返回 ("", false) 表示放行。
func (g *CompletenessGate) ShouldContinue() (string, bool) {
	if g == nil || g.Pending == nil {
		return "", false
	}
	if g.nudges >= completenessMaxNudges {
		return "", false
	}
	pending := g.Pending()
	if len(pending) == 0 {
		return "", false
	}
	g.nudges++
	return g.NudgeText(pending), true
}

// NudgeText 生成注入给模型的续跑提醒（点明还有哪些未完成项）。
func (g *CompletenessGate) NudgeText(pending []string) string {
	s := "【完成度自检】你的待办清单里还有未完成项，任务尚未结束，不要现在收尾：\n"
	for _, p := range pending {
		s += fmt.Sprintf("- %s\n", p)
	}
	s += "请继续执行下一步工具调用；确实全部完成时，先调用 todo 把对应项标记为完成，" +
		"再用 respond 交付结果。"
	return s
}
