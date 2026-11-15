// Package reflect 实现「反思引擎」（H-7 下半，对齐 hermes 的 insights / background review 思路）。
//
// 与已有能力的分工：
//   - `internal/agent/background_review.go`：每轮对话后**被动**判断「刚才这段有没有
//     值得沉淀的技能/记忆」，产出 skill 或 memory 条目；
//   - 本包：**主动**反思——把「最近的技能使用、学习图谱密度、会话活动」汇总成
//     可行动的洞察（哪些技能成了孤岛、哪些工具被反复用说明缺工具、哪些记忆没被
//     用上），既提供纯启发式版本（无 LLM 也能用），也提供 LLM 蒸馏版本。
//
// 为什么要有纯启发式版本：内网/离线/无网关时反思不能失效，且启发式结论可复核
// （不含幻觉），适合直接写进 MEMORY.md 的 project 分类。
package reflect

import (
	"fmt"
	"sort"
	"strings"

	"mizar/internal/learninggraph"
)

// Insight 一条反思结论。
type Insight struct {
	Kind       string `json:"kind"` // unused_skill | orphan_skill | heavy_tool | memory_gap | focus
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Actionable string `json:"actionable,omitempty"` // 建议动作（可直接照着做）
}

// Activity 反思输入：近况快照。
type Activity struct {
	Graph       learninggraph.Graph // 学习图谱（技能+记忆+边）
	ToolCounts  map[string]int      // 最近工具调用次数
	SkillCounts map[string]int      // 技能使用次数（同 graph 内 use_count 来源）
	RecentTasks []string            // 最近的任务描述（可空）
	TurnCount   int                 // 最近统计轮数（可空）
}

// Heuristic 纯启发式反思（不调 LLM，结论可复核）。
func Heuristic(a Activity) []Insight {
	var out []Insight

	// 1. 孤岛技能：进了图但没有任何边（既无 related_skills，也无记忆关联）
	linked := map[string]bool{}
	degree := map[string]int{}
	for _, e := range a.Graph.Edges {
		linked[e.Source] = true
		linked[e.Target] = true
		degree[e.Source]++
		degree[e.Target]++
	}
	var orphans []string
	for _, n := range a.Graph.Nodes {
		if n.Kind == "skill" && !linked[n.ID] {
			orphans = append(orphans, n.Label)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		out = append(out, Insight{
			Kind:  "orphan_skill",
			Title: fmt.Sprintf("%d 个技能是孤岛（无任何关联边）", len(orphans)),
			Detail: "这些技能没被 related_skills 关联、也没和记忆产生词法重叠，说明它们孤立存在、" +
				"难以在需要时被想起来：" + strings.Join(orphans, "、"),
			Actionable: "给它们补 `related_skills: [相关技能A, 相关技能B]` frontmatter，或写一条指向它们的记忆",
		})
	}

	// 2. 用了但没沉淀：高频工具说明要么缺专门工具，要么该写成技能
	if len(a.ToolCounts) > 0 {
		type kv struct {
			name string
			n    int
		}
		var tools []kv
		for k, v := range a.ToolCounts {
			if v >= 5 { // 阈值：至少用了 5 次才算习惯
				tools = append(tools, kv{k, v})
			}
		}
		sort.Slice(tools, func(i, j int) bool {
			if tools[i].n != tools[j].n {
				return tools[i].n > tools[j].n
			}
			return tools[i].name < tools[j].name
		})
		if len(tools) > 0 {
			var parts []string
			for _, t := range tools {
				parts = append(parts, fmt.Sprintf("%s×%d", t.name, t.n))
			}
			// 只对「非核心通用工具」提示，bash/read 这类天然高频
			out = append(out, Insight{
				Kind:   "heavy_tool",
				Title:  "高频工具模式值得沉淀",
				Detail: "最近这些工具被反复使用：" + strings.Join(parts, "、"),
				Actionable: "如果某个工具总是配合同一组参数/步骤出现（如固定的一串 bash），" +
					"考虑用 skill_manage 写成技能，或做成一次性工具/脚本",
			})
		}
	}

	// 3. 记忆与技能脱节：有记忆条目但完全没有 lexical 边
	memNodes, lexical := 0, 0
	for _, n := range a.Graph.Nodes {
		if n.Kind == "memory" {
			memNodes++
		}
	}
	for _, e := range a.Graph.Edges {
		if e.Reason == "lexical" {
			lexical++
		}
	}
	if memNodes > 0 && lexical == 0 {
		out = append(out, Insight{
			Kind:  "memory_gap",
			Title: "记忆条目与技能完全没有关联",
			Detail: fmt.Sprintf("有 %d 条结构化记忆，但与任何技能都没有词法重叠——"+
				"说明记忆记的是「人/项目约定」、技能记的是「操作手法」，二者各说各话", memNodes),
			Actionable: "在相关技能正文里引用对应记忆要点（如「参见 MEMORY 的 project:部署约定」），" +
				"或用更贴近技能名的措辞重写记忆条目",
		})
	}

	// 4. 使用分布：in-graph 技能里「用过 / 没用过」的比例
	if a.Graph.Stats.Skills > 0 {
		used, total := a.Graph.Stats.Used, a.Graph.Stats.Skills
		if used*2 < total { // 用过的不及一半
			out = append(out, Insight{
				Kind:   "focus",
				Title:  fmt.Sprintf("学习图谱里 %d/%d 个技能从未被使用", total-used, total),
				Detail: "被创建/学习但从未调用的技能，等于没有；它们占着索引位但没产出价值",
				Actionable: "用 `/skills` 逐个看，无用则 skill_manage delete；" +
					"或者反过来，把它们真正用起来（技能价值在使用中体现）",
			})
		}
	}

	return out
}

// Render 把反思结论渲染成人读文本（TUI /learn reflect 用）。
func Render(insights []Insight) string {
	if len(insights) == 0 {
		return "🧠 反思完成：未发现明显可改进项（技能图谱连通、工具使用均衡）。"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧠 反思（%d 条）：\n", len(insights))
	for i, in := range insights {
		fmt.Fprintf(&sb, "\n%d. [%s] %s\n", i+1, in.Kind, in.Title)
		fmt.Fprintf(&sb, "   %s\n", in.Detail)
		if in.Actionable != "" {
			fmt.Fprintf(&sb, "   → %s\n", in.Actionable)
		}
	}
	return strings.TrimSpace(sb.String())
}

// RenderGraph 把图谱渲染成缩进文本（TUI /learn graph 用；纯文本树，不依赖 UI）。
func RenderGraph(g learninggraph.Graph) string {
	if len(g.Nodes) == 0 {
		return "（学习图谱为空：还没有被使用/被创建的技能）"
	}
	var sb strings.Builder
	st := g.Stats
	fmt.Fprintf(&sb, "学习图谱：%d 节点（技能 %d / 记忆 %d）· %d 边（related %d / lexical %d）· 孤立 %.1f%%\n",
		st.Nodes, st.Skills, st.MemoryNodes, st.RelatedEdges+st.LexicalEdges, st.RelatedEdges, st.LexicalEdges, st.IsolatedPct)

	adj := map[string][]string{}
	for _, e := range g.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target+"("+e.Reason+")")
		adj[e.Target] = append(adj[e.Target], e.Source+"("+e.Reason+")")
	}
	fmt.Fprintf(&sb, "\n■ 技能（%d）\n", st.Skills)
	for _, n := range g.Nodes {
		if n.Kind != "skill" {
			continue
		}
		cat := n.Category
		if cat == "" {
			cat = "未分类"
		}
		fmt.Fprintf(&sb, "  • %s [%s] 用%d次", n.Label, cat, n.UseCount)
		if links := adj[n.ID]; len(links) > 0 {
			fmt.Fprintf(&sb, "  ↔ %s", strings.Join(links, ", "))
		} else {
			sb.WriteString("  (孤岛)")
		}
		sb.WriteString("\n")
	}
	if st.MemoryNodes > 0 {
		fmt.Fprintf(&sb, "\n■ 记忆（%d）\n", st.MemoryNodes)
		for _, n := range g.Nodes {
			if n.Kind != "memory" {
				continue
			}
			fmt.Fprintf(&sb, "  • [%s] %s", n.Source, n.Label)
			if links := adj[n.ID]; len(links) > 0 {
				fmt.Fprintf(&sb, "  ↔ %s", strings.Join(links, ", "))
			}
			sb.WriteString("\n")
		}
	}
	if len(st.Categories) > 0 {
		var cats []string
		for k, v := range st.Categories {
			cats = append(cats, fmt.Sprintf("%s:%d", k, v))
		}
		sort.Strings(cats)
		fmt.Fprintf(&sb, "\n分类分布：%s\n", strings.Join(cats, " "))
	}
	return strings.TrimSpace(sb.String())
}

// DistillPrompt 生成「LLM 蒸馏」的提示词（可选增强：有辅助模型时用）。
func DistillPrompt(a Activity, heuristic []Insight) string {
	var sb strings.Builder
	sb.WriteString("你是技能库的维护者。下面是本会话学习图谱的客观统计与启发式结论。\n")
	sb.WriteString("请给出 2-4 条**可执行**的改进建议（不要重复已有结论、不要泛泛而谈）。\n\n")
	sb.WriteString("## 统计\n")
	fmt.Fprintf(&sb, "- 技能 %d 个（用过 %d，agent 创建 %d），记忆 %d 条\n",
		a.Graph.Stats.Skills, a.Graph.Stats.Used, a.Graph.Stats.AgentCreated, a.Graph.Stats.MemoryNodes)
	fmt.Fprintf(&sb, "- 边：related %d / lexical %d，孤立 %.1f%%\n",
		a.Graph.Stats.RelatedEdges, a.Graph.Stats.LexicalEdges, a.Graph.Stats.IsolatedPct)
	if len(a.RecentTasks) > 0 {
		sb.WriteString("\n## 最近任务\n")
		for _, t := range a.RecentTasks {
			fmt.Fprintf(&sb, "- %s\n", t)
		}
	}
	if len(heuristic) > 0 {
		sb.WriteString("\n## 已发现的启发式结论（不要重复）\n")
		for _, in := range heuristic {
			fmt.Fprintf(&sb, "- [%s] %s\n", in.Kind, in.Title)
		}
	}
	sb.WriteString("\n输出格式：每行一条，`- 建议内容`，不要标题、不要解释格式。")
	return sb.String()
}
