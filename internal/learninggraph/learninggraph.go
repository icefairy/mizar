// Package learninggraph 实现「学习闭环」的可视化图谱（H-7，对齐 hermes learning_graph）。
//
// Hermes 的做法：把「学到的东西」建成图——技能为节点（带使用次数/来源/创建者），
// related_skills 为技能间的显式边，记忆条目（MEMORY.md / USER.md）为记忆节点，
// 记忆↔技能之间用词法重叠启发式连边；再配密度统计（孤立节点占比等）让你看见
// 「到底学到了什么、哪些是孤岛」。
//
// Mizar 的对应物：
//   - 技能：internal/skills 加载的 SKILL.md（新增 related_skills / created_by 解析）
//   - 记忆：internal/memory 的四分类事实条目
//   - 使用次数：internal/skills.Stats（技能调用计数）
//
// 输出是纯数据结构（JSON 友好），由 /learn graph 命令或工具渲染；不依赖任何 UI。
package learninggraph

import (
	"sort"
	"strings"
	"unicode"

	"mizar/internal/memory"
	"mizar/internal/skills"
)

// Node 图节点。
type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"` // "skill" | "memory"
	Category  string `json:"category,omitempty"`
	UseCount  int    `json:"use_count"`
	CreatedBy string `json:"created_by,omitempty"`
	Source    string `json:"source,omitempty"` // 记忆：user/feedback/project/reference
}

// Edge 一条无向边。
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason"` // "related_skills" | "lexical"
}

// Stats 图谱密度统计。
type Stats struct {
	Nodes        int            `json:"nodes"`
	Skills       int            `json:"skills"`
	MemoryNodes  int            `json:"memory_nodes"`
	RelatedEdges int            `json:"related_edges"`
	LexicalEdges int            `json:"lexical_edges"`
	IsolatedPct  float64        `json:"isolated_pct"` // 孤立（无边）节点占比
	AgentCreated int            `json:"agent_created"`
	Used         int            `json:"used"`
	Categories   map[string]int `json:"categories,omitempty"`
}

// Graph 完整图谱。
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	Stats Stats  `json:"stats"`
}

// Build 构建学习图谱。
//
// skillList 为已加载技能；useCount 为技能名→使用次数（可为 nil）；
// memEntries 为记忆（分类→条目文本，来自 memory.Store.List("")）。
//
// 过滤规则（对齐 hermes 的 _has_learning_signal）：只收录有「学习信号」的技能
// ——被使用过（useCount>0）或由 agent/learn 创建。这样图谱反映「学到的」而非
// 「预置的」。记忆条目全部收录。
func Build(skillList []skills.Skill, useCount map[string]int, memEntries map[string][]string) Graph {
	nodes := make([]Node, 0, len(skillList)+8)
	byName := map[string]skills.Skill{}
	edgeSet := map[[2]string]Edge{}

	// 技能节点
	for _, s := range skillList {
		uses := useCount[s.Name]
		learned := uses > 0 || s.CreatedBy == "agent" || s.CreatedBy == "learn"
		if !learned {
			continue // 预置技能不进学习图谱
		}
		byName[s.Name] = s
		nodes = append(nodes, Node{
			ID: "skill:" + s.Name, Label: s.Name, Kind: "skill",
			Category: s.Category, UseCount: uses, CreatedBy: s.CreatedBy,
		})
	}
	// 关联补全：被「已收录技能」通过 related_skills 引用的技能也拉进图——
	// 否则 related_skills 声明的边永远画不出来（被引用方往往自身未使用）。
	// 这是 hermes「both endpoints exist」的实用变体：一端已收录即拉入另一端。
	allSkills := map[string]skills.Skill{}
	for _, s := range skillList {
		allSkills[s.Name] = s
	}
	for _, s := range byName {
		for _, target := range s.Related {
			if _, ok := byName[target]; ok {
				continue
			}
			if t, ok := allSkills[target]; ok {
				byName[target] = t
				nodes = append(nodes, Node{
					ID: "skill:" + t.Name, Label: t.Name, Kind: "skill",
					Category: t.Category, UseCount: useCount[t.Name], CreatedBy: t.CreatedBy,
				})
			}
		}
	}

	// related_skills 边（两端都在图中才算）
	for _, s := range byName {
		for _, target := range s.Related {
			if target == s.Name {
				continue
			}
			if _, ok := byName[target]; !ok {
				continue
			}
			a, b := s.Name, target
			if a > b {
				a, b = b, a
			}
			edgeSet[[2]string{a, b}] = Edge{Source: "skill:" + a, Target: "skill:" + b, Reason: "related_skills"}
		}
	}

	// 记忆节点 + 词法重叠边
	catOrder := memory.Categories()
	for _, cat := range catOrder {
		for i, entry := range memEntries[cat] {
			id := "memory:" + cat + ":" + itoa(i)
			label := entry
			if r := []rune(label); len(r) > 40 {
				label = string(r[:40]) + "…"
			}
			nodes = append(nodes, Node{
				ID: id, Label: label, Kind: "memory", Category: "memory", Source: cat,
			})
			// 每条记忆 → 最多 3 个词法重叠技能
			for _, name := range topOverlapSkills(entry, byName, 3) {
				a, b := id, "skill:"+name
				if a > b {
					a, b = b, a
				}
				edgeSet[[2]string{a, b}] = Edge{Source: a, Target: b, Reason: "lexical"}
			}
		}
	}

	// 稳定排序输出
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Kind != nodes[j].Kind {
			return nodes[i].Kind < nodes[j].Kind
		}
		return nodes[i].ID < nodes[j].ID
	})
	edges := make([]Edge, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})

	return Graph{Nodes: nodes, Edges: edges, Stats: computeStats(nodes, edges)}
}

// topOverlapSkills 返回与 text 词法重叠度最高的前 n 个技能名（按得分降序、名字升序）。
func topOverlapSkills(text string, candidates map[string]skills.Skill, n int) []string {
	textTokens := tokenize(text)
	if len(textTokens) == 0 {
		return nil
	}
	type scored struct {
		name  string
		score int
	}
	var all []scored
	for name, s := range candidates {
		score := 0
		lowered := strings.ToLower(name)
		if lowered != "" && strings.Contains(strings.ToLower(text), lowered) {
			score += 6 // 技能名直接出现，强信号（对齐 hermes）
		}
		for t := range tokenize(s.Name + " " + s.Description) {
			if textTokens[t] {
				score++
			}
		}
		// 阈值：至少 2 分才算有关联（单个 CJK bigram 偶然重叠噪声太大，
		// 例如记忆里出现「代码」就与所有讲代码的技能连边）。技能名直接出现（+6）恒过阈值。
		if score >= 2 {
			all = append(all, scored{name: name, score: score})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].name < all[j].name
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, len(all))
	for i, s := range all {
		out[i] = s.name
	}
	return out
}

// tokenize 中英混合粗分词：CJK 连续串取 2-gram 片段、ASCII 取 ≥3 字符的词。
// 与 sessionsearch 的 trigram、memory 的 bigram 思路一致，够用于「重叠度」启发式。
func tokenize(text string) map[string]bool {
	set := map[string]bool{}
	lower := strings.ToLower(text)
	var ascii strings.Builder
	var cjk []rune
	flushASCII := func() {
		if ascii.Len() >= 3 {
			set[ascii.String()] = true
		}
		ascii.Reset()
	}
	flushCJK := func() {
		// CJK 序列输出 2-gram（单个字太泛，3-gram 太严）
		for i := 0; i+1 < len(cjk); i++ {
			set[string(cjk[i:i+2])] = true
		}
		cjk = cjk[:0]
	}
	for _, r := range lower {
		switch {
		case isCJK(r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			ascii.WriteRune(r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return set
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func computeStats(nodes []Node, edges []Edge) Stats {
	st := Stats{Nodes: len(nodes), Categories: map[string]int{}}
	linked := map[string]bool{}
	for _, e := range edges {
		linked[e.Source] = true
		linked[e.Target] = true
		if e.Reason == "related_skills" {
			st.RelatedEdges++
		} else {
			st.LexicalEdges++
		}
	}
	for _, n := range nodes {
		if n.Kind == "skill" {
			st.Skills++
			if n.CreatedBy == "agent" || n.CreatedBy == "learn" {
				st.AgentCreated++
			}
			if n.UseCount > 0 {
				st.Used++
			}
			if n.Category != "" {
				st.Categories[n.Category]++
			} else {
				st.Categories["(未分类)"]++
			}
		} else {
			st.MemoryNodes++
		}
	}
	if len(nodes) > 0 {
		st.IsolatedPct = float64(len(nodes)-len(linked)) * 100 / float64(len(nodes))
	}
	return st
}

// itoa 小工具（避免为了一个整数引入 strconv 的噪音可读性权衡——这里就一行）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
