// 拷问模式（Grilling Mode）：动手之前先把需求问到没有歧义。
//
// 与计划模式（plan.go）的分工：
//   - 拷问模式解决「到底要做什么」——向用户追问目标、验收标准、边界与约束；
//   - 计划模式解决「怎么做」——探索方案并提交可审批的执行计划。
//
// 两者可以叠加：先 /grill 问清需求，再 /plan 出方案。
//
// 使用方式：
//   - /grill [guidance] — 进入拷问模式（可选附带自定义拷问指引）
//   - /grill off        — 退出拷问模式
//
// 与计划模式不同，拷问模式不需要 exit 工具：模型在收敛条件满足后自己停止提问，
// 输出「确认清单 + 风险与默认假设」即可；用户随时可以 /grill off 强制结束。
package agent

import (
	"strings"
	"sync"
	"sync/atomic"
)

// GrillMode 拷问模式状态。
type GrillMode struct {
	mu       sync.RWMutex
	active   bool
	guidance string        // 拷问阶段的系统提示词片段
	ver      atomic.Uint64 // 每次 Enter/Exit 自增，用于让 system prompt 缓存失效
}

// NewGrillMode 创建空的拷问模式控制器。
func NewGrillMode() *GrillMode {
	return &GrillMode{}
}

// Enter 进入拷问模式；guidance 为空时使用默认拷问指引。
func (g *GrillMode) Enter(guidance string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if guidance == "" {
		guidance = defaultGrillGuidance
	}
	g.guidance = guidance
	g.active = true
	g.ver.Add(1)
}

// Exit 退出拷问模式。
func (g *GrillMode) Exit() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = false
	g.ver.Add(1)
}

// Version 返回模式状态版本号（供 system prompt 缓存失效判断）。
func (g *GrillMode) Version() uint64 {
	if g == nil {
		return 0
	}
	return g.ver.Load()
}

// IsActive 当前是否处于拷问模式。
func (g *GrillMode) IsActive() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.active
}

// Guidance 返回当前拷问指引。
func (g *GrillMode) Guidance() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.guidance
}

// SystemSection 返回注入 system prompt 的文本块（空字符串表示未激活）。
func (g *GrillMode) SystemSection() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.active {
		return ""
	}
	return "\n\n## 拷问模式（Grilling Mode）\n" + strings.TrimSpace(g.guidance) + "\n"
}

// defaultGrillGuidance 是 /grill 不带参数时注入的预设拷问提示词。
//
// 设计取舍：
//   - 先复述再提问：让用户一眼看出理解偏差，比直接抛十个问题高效得多；
//   - 每题必带选项：用户点编号即可回答，避免「从零打字」把拷问变成负担；
//   - 只问能改变方案/工作量的问题，措辞类问题禁止问；
//   - 明确收敛条件与轮次上限，防止拷问无限循环变成骚扰；
//   - 拷问期间禁止改代码，只允许只读探查，保证「问准」而不是「边猜边改」。
const defaultGrillGuidance = `你正处于拷问模式。目标是**在动手之前把需求问到没有歧义**，而不是急着写代码或执行命令。

### 拷问准则
1. **先复述再提问**：先用 3~5 行复述你理解的「要做什么 / 成功标准 / 明确不做什么」，
   让用户一眼看出你哪里理解偏了，再开始提问。
2. **每轮最多 3 个问题**，每个问题给 2~4 个具体选项（用 ask_user_question，
   让用户点编号即可回答；不要让用户从零开始打字）。
3. **只问能改变结论的问题**，按优先级：
   目标与验收标准 > 边界与非目标 > 数据/环境/兼容性约束 > 失败与回滚方式 >
   不可逆操作与权限 > 时间与优先级。措辞、命名、注释风格一类不要问。
4. **每轮只挖一层**：拿到回答后，先指出回答里新暴露的空白或矛盾，再问下一层；
   不要把所有问题一次性堆给用户。
5. **用户说「你决定」时**：给出你的推荐方案 + 理由 + 影响面，让用户只做确认。
6. **拷问期间禁止改代码**：不修改文件、不执行破坏性或可逆性不明的命令、不初始化仓库。
   只读操作（读文件、搜索、查看现状）可以做，用来把问题问准。

### 收敛条件（满足其一即停止提问）
- 你能写出「完成定义」清单：每条都是可验证的验收项，且有明确判定方式；
- 用户明确表示信息够了 / 直接开始；
- 已完成 3 轮关键提问。

### 收敛后的输出
不再提问，改为输出三段：
① **确认清单**：要做什么 / 成功标准 / 非目标 / 硬约束
② **风险与默认假设**：仍不确定的点，写清「若默认假设不对请纠正」
③ **下一步建议**：继续 /plan 出方案，或直接开工
然后停下等用户指令，不要自行开工。`
