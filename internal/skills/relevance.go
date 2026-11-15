// 技能相关性检索（2026-11-15）。
//
// 问题：RenderIndex 把**全部**技能（名+描述+路径）塞进系统提示。技能少时无所谓，
// 但项目技能会随项目数量线性增长——十个项目就是十条索引，把本该给代码的上下文
// 占掉了，而且真正相关的那个被淹在无关条目里。
//
// 解法（对齐 hermes 的 project > local 优先级 + 按相关性注入思路）：
//   - `always: true` 的技能常驻（少数必须总在视野里的）
//   - 其余按**当前任务文本**打分：任务里出现技能名/触发词/分类即加分
//   - 只注入 top-N（默认 8）+ 一句「还有 N 个技能未列出，可用 skill_search 检索」
//   - 模型需要其它技能时用 `skill_search` 工具按关键词找回（按需加载，不占常驻上下文）
//
// 这样即使有一百个项目技能，系统提示里也永远只有当前最相关的那几条。
package skills

import (
	"fmt"
	"sort"
	"strings"
)

// RelevanceOptions 注入策略参数。
type RelevanceOptions struct {
	// MaxSkills 最多注入多少条（<=0 用默认 8）。
	MaxSkills int
	// ExcludeAlways 显式排除 always:true 的技能（默认包含）。
	// 用「反向开关」而不是 IncludeAlways：bool 零值是 false，若用正向开关，
	// 调用方不传就变成「排除常驻技能」——与直觉相反。
	ExcludeAlways bool
}

const defaultMaxInjectedSkills = 8

// Score 计算技能与任务文本的相关度。0 = 不相关。
//
// 打分规则（简单可解释，不引第三方检索库）：
//   - 技能名出现在任务里：+6（最强信号）
//   - 触发词出现在任务里：+4 each
//   - 分类名出现在任务里：+3
//   - 描述里的词与任务词重叠：+1 each（最多 +3）
func (s *Skill) Score(taskLower string, taskTokens map[string]bool) int {
	if taskLower == "" {
		return 0
	}
	score := 0
	// ① 技能名直接出现（最强信号）。中文任务里名字也可能是中文，用分词判断更稳。
	name := strings.ToLower(s.Name)
	if name != "" {
		if strings.Contains(taskLower, name) {
			score += 6
		} else if nameTokens := tokenize(name); overlapCount(nameTokens, taskTokens) >= len(nameTokens)*2/3 && len(nameTokens) > 0 {
			// 名字的 token 大部分都在任务里（处理「名字是 project-xxx、任务写 xxx」之类）
			score += 3
		}
	}
	// ② 触发词（次强）。逐个用 token 重叠判断，中文触发词也能命中。
	for _, t := range s.Triggers {
		tl := strings.ToLower(strings.TrimSpace(t))
		if tl == "" {
			continue
		}
		if strings.Contains(taskLower, tl) {
			score += 4
			continue
		}
		tt := tokenize(tl)
		if len(tt) > 0 && overlapCount(tt, taskTokens)*2 >= len(tt) {
			// 触发词有一半以上 token 命中
			score += 2
		}
	}
	// ③ 分类
	if s.Category != "" && strings.Contains(taskLower, strings.ToLower(s.Category)) {
		score += 3
	}
	// ④ 描述重叠（弱信号，**归一化**：按描述 token 数折算，避免长描述靠堆词刷分）
	if len(s.DescTokens) > 0 {
		ov := overlapCount(s.DescTokens, taskTokens)
		if ov > 0 {
			// 重叠率 → 0~3 分（bigram+unigram 会把同一短语重复计数，0.5 已算高度相关）
			ratio := float64(ov) / float64(len(s.DescTokens))
			bonus := int(ratio*6 + 0.5)
			if bonus > 3 {
				bonus = 3
			}
			score += bonus
		}
	}
	return score
}

// Relevant 按任务文本挑选要注入的技能。
//
// 返回 (注入列表, 未列出的数量)。task 为空时退化为「返回前 N 条」（保持旧行为，
// 因为会话刚启动时还不知道要做什么）。
func (m *Manager) Relevant(task string, opt RelevanceOptions) ([]Skill, int) {
	all := m.All()
	if len(all) == 0 {
		return nil, 0
	}
	maxN := opt.MaxSkills
	if maxN <= 0 {
		maxN = defaultMaxInjectedSkills
	}
	taskLower := strings.ToLower(strings.TrimSpace(task))
	// 中文无空格，必须用 CJK 分词（unigram+bigram），否则整句只有一个 token，
	// 任何描述词都命中不了 → 该召回的技能全漏（实测过）。
	tokens := tokenize(taskLower)

	// always-on 优先占位。
	// 注意：RelevanceOptions 零值时 IncludeAlways=false（bool 零值），但「默认包含」
	// 才是符合直觉的语义——所以调用方不传时按包含处理，显式要排除请用
	// RelevanceOptions{ExcludeAlways: true}。
	var picked []Skill
	var rest []Skill
	for _, s := range all {
		if s.AlwaysOn && !opt.ExcludeAlways {
			picked = append(picked, s)
			continue
		}
		rest = append(rest, s)
	}

	if taskLower == "" {
		// 无任务文本：按名字顺序取前 N（与旧行为一致，可预期）
		remaining := maxN - len(picked)
		if remaining < 0 {
			remaining = 0
		}
		for i := 0; i < len(rest) && i < remaining; i++ {
			picked = append(picked, rest[i])
		}
		return picked, len(rest) - min(len(rest), remaining)
	}

	// 有任务文本：按相关度排序，高于阈值才注入
	type scored struct {
		s Skill
		n int
	}
	var cands []scored
	for _, s := range rest {
		n := s.Score(taskLower, tokens)
		if n > 0 {
			cands = append(cands, scored{s: s, n: n})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].n != cands[j].n {
			return cands[i].n > cands[j].n
		}
		return cands[i].s.Name < cands[j].s.Name
	})
	remaining := maxN - len(picked)
	if remaining < 0 {
		remaining = 0
	}
	for i := 0; i < len(cands) && i < remaining; i++ {
		picked = append(picked, cands[i].s)
	}
	hidden := len(rest) - min(len(cands), remaining)
	return picked, hidden
}

// Search 按关键词检索技能（供 skill_search 工具用）。
// 返回按相关度排序的结果，limit<=0 时默认 10。
func (m *Manager) Search(query string, limit int) []Skill {
	if limit <= 0 {
		limit = 10
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	tokens := tokenize(q)
	type scored struct {
		s Skill
		n int
	}
	var cands []scored
	for _, s := range m.All() {
		n := s.Score(q, tokens)
		if n == 0 && q != "" && strings.Contains(s.SearchText, q) {
			n = 2 // 子串兜底
		}
		if n > 0 {
			cands = append(cands, scored{s: s, n: n})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].n != cands[j].n {
			return cands[i].n > cands[j].n
		}
		return cands[i].s.Name < cands[j].s.Name
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]Skill, len(cands))
	for i, c := range cands {
		out[i] = c.s
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RenderRelevant 渲染按相关性挑选后的技能索引段。
//
// 与 RenderIndex 的区别：只列 top-N 相关技能 + 一行「还有 N 个可用 skill_search 检索」。
// task 为空时行为等同 RenderIndex 的前 N 条（会话刚起步拿不到任务文本）。
func (m *Manager) RenderRelevant(task string, opt RelevanceOptions) string {
	picked, hidden := m.Relevant(task, opt)
	if len(picked) == 0 && hidden == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 可用技能\n")
	sb.WriteString("下面是与当前任务最相关的技能。任务匹配其描述时，用 read 工具读取路径对应的文件加载完整指令；")
	sb.WriteString("需要其它技能时用 `skill_search {\"query\": \"关键词\"}` 检索（全部技能：`/skills`）。\n")
	for _, sk := range picked {
		sb.WriteString("- ")
		if sk.Category != "" {
			sb.WriteString("[" + sk.Category + "] ")
		}
		sb.WriteString(sk.RenderIndex())
		sb.WriteString("\n")
	}
	if hidden > 0 {
		sb.WriteString(fmt.Sprintf("- …另有 %d 个技能未列出（用 `skill_search` 按关键词检索，或 `skill` 工具按名加载）\n", hidden))
	}
	return sb.String()
}
