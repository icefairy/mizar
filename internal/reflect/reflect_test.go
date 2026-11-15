package reflect

import (
	"strings"
	"testing"

	"mizar/internal/learninggraph"
)

func g(nodes []learninggraph.Node, edges []learninggraph.Edge) learninggraph.Graph {
	stats := learninggraph.Stats{}
	for _, n := range nodes {
		if n.Kind == "skill" {
			stats.Skills++
			if n.UseCount > 0 {
				stats.Used++
			}
		} else {
			stats.MemoryNodes++
		}
	}
	return learninggraph.Graph{Nodes: nodes, Edges: edges, Stats: stats}
}

func TestOrphanSkillDetected(t *testing.T) {
	gr := g([]learninggraph.Node{
		{ID: "skill:a", Label: "a", Kind: "skill"},
		{ID: "skill:b", Label: "b", Kind: "skill"},
	}, []learninggraph.Edge{{Source: "skill:a", Target: "skill:b", Reason: "related_skills"}})
	ins := Heuristic(Activity{Graph: gr})
	// a、b 都有边 → 无孤岛
	for _, i := range ins {
		if i.Kind == "orphan_skill" {
			t.Errorf("a/b 都连边，不应报孤岛: %s", i.Detail)
		}
	}
	// 加一个孤立技能
	gr2 := g([]learninggraph.Node{
		{ID: "skill:solo", Label: "solo", Kind: "skill"},
	}, nil)
	ins2 := Heuristic(Activity{Graph: gr2})
	found := false
	for _, i := range ins2 {
		if i.Kind == "orphan_skill" {
			found = true
			if !strings.Contains(i.Detail, "solo") {
				t.Errorf("应点名 solo: %s", i.Detail)
			}
		}
	}
	if !found {
		t.Error("孤立技能应被检出")
	}
}

func TestHeavyToolDetected(t *testing.T) {
	ins := Heuristic(Activity{
		Graph:      g(nil, nil),
		ToolCounts: map[string]int{"bash": 12, "grep": 3},
	})
	found := false
	for _, i := range ins {
		if i.Kind == "heavy_tool" {
			found = true
			if !strings.Contains(i.Detail, "bash×12") {
				t.Errorf("应含 bash×12: %s", i.Detail)
			}
			if strings.Contains(i.Detail, "grep") {
				t.Errorf("低于阈值(5)的 grep 不应出现: %s", i.Detail)
			}
		}
	}
	if !found {
		t.Error("高频工具应被检出")
	}
}

func TestMemoryGapDetected(t *testing.T) {
	gr := g([]learninggraph.Node{
		{ID: "memory:project:0", Label: "某记忆", Kind: "memory"},
		{ID: "skill:x", Label: "x", Kind: "skill"},
	}, nil) // 无 lexical 边
	ins := Heuristic(Activity{Graph: gr})
	found := false
	for _, i := range ins {
		if i.Kind == "memory_gap" {
			found = true
		}
	}
	if !found {
		t.Error("记忆与技能无关联应被检出")
	}
}

func TestFocusLowUsage(t *testing.T) {
	gr := g([]learninggraph.Node{
		{ID: "skill:a", Label: "a", Kind: "skill", UseCount: 0},
		{ID: "skill:b", Label: "b", Kind: "skill", UseCount: 0},
		{ID: "skill:c", Label: "c", Kind: "skill", UseCount: 0},
	}, nil)
	ins := Heuristic(Activity{Graph: gr})
	found := false
	for _, i := range ins {
		if i.Kind == "focus" {
			found = true
		}
	}
	if !found {
		t.Error("大量未使用技能应被检出")
	}
}

func TestRenderEmpty(t *testing.T) {
	out := Render(nil)
	if !strings.Contains(out, "未发现明显可改进项") {
		t.Errorf("空结论文案: %s", out)
	}
}

func TestRenderInsights(t *testing.T) {
	ins := []Insight{{Kind: "orphan_skill", Title: "T", Detail: "D", Actionable: "A"}}
	out := Render(ins)
	for _, want := range []string{"反思（1 条）", "T", "D", "→ A"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q:\n%s", want, out)
		}
	}
}

func TestRenderGraph(t *testing.T) {
	gr := g([]learninggraph.Node{
		{ID: "skill:a", Label: "a", Kind: "skill", UseCount: 2, Category: "devops"},
		{ID: "skill:b", Label: "b", Kind: "skill"},
	}, []learninggraph.Edge{{Source: "skill:a", Target: "skill:b", Reason: "related_skills"}})
	out := RenderGraph(gr)
	for _, want := range []string{"学习图谱", "■ 技能", "a [devops] 用2次", "↔"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q:\n%s", want, out)
		}
	}
}

func TestRenderGraphEmpty(t *testing.T) {
	if out := RenderGraph(learninggraph.Graph{}); !strings.Contains(out, "学习图谱为空") {
		t.Errorf("空图文案: %s", out)
	}
}

func TestDistillPrompt(t *testing.T) {
	gr := g([]learninggraph.Node{{ID: "skill:a", Label: "a", Kind: "skill"}}, nil)
	p := DistillPrompt(Activity{
		Graph:       gr,
		RecentTasks: []string{"重构 X"},
	}, []Insight{{Kind: "orphan_skill", Title: "已有结论"}})
	for _, want := range []string{"技能库的维护者", "重构 X", "已有结论", "不要重复"} {
		if !strings.Contains(p, want) {
			t.Errorf("缺少 %q:\n%s", want, p)
		}
	}
}
