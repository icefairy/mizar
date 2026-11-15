package learninggraph

import (
	"strings"
	"testing"

	"mizar/internal/memory"
	"mizar/internal/skills"
)

func sampleSkills() []skills.Skill {
	return []skills.Skill{
		{Name: "docker-ops", Description: "容器运维 排障", Category: "devops", CreatedBy: "agent", Related: []string{"k8s-debug"}},
		{Name: "k8s-debug", Description: "Kubernetes 集群调试", Category: "devops"},
		{Name: "handoff", Description: "生成交接文档"}, // 预置、未使用 → 不进图
		{Name: "sql-tuning", Description: "数据库慢查询优化", CreatedBy: "learn"},
	}
}

func TestBuildFiltersPrebuiltUnused(t *testing.T) {
	g := Build(sampleSkills(), nil, nil)
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	if ids["skill:handoff"] {
		t.Error("预置且未使用的技能不应进学习图谱")
	}
	for _, want := range []string{"skill:docker-ops", "skill:k8s-debug", "skill:sql-tuning"} {
		if !ids[want] {
			t.Errorf("应包含 %s", want)
		}
	}
}

func TestBuildUsedSkillIncluded(t *testing.T) {
	// handoff 被使用过 → 应进图
	g := Build(sampleSkills(), map[string]int{"handoff": 3}, nil)
	found := false
	for _, n := range g.Nodes {
		if n.ID == "skill:handoff" {
			found = true
			if n.UseCount != 3 {
				t.Errorf("use_count=%d", n.UseCount)
			}
		}
	}
	if !found {
		t.Error("使用过的技能应进图")
	}
}

func TestRelatedEdges(t *testing.T) {
	g := Build(sampleSkills(), nil, nil)
	var rel int
	for _, e := range g.Edges {
		if e.Reason == "related_skills" {
			rel++
			if !(e.Source == "skill:docker-ops" && e.Target == "skill:k8s-debug") &&
				!(e.Source == "skill:k8s-debug" && e.Target == "skill:docker-ops") {
				t.Errorf("边端点错误: %+v", e)
			}
		}
	}
	if rel != 1 {
		t.Errorf("应有 1 条 related 边，got %d", rel)
	}
}

func TestRelatedEdgeRequiresBothEndpoints(t *testing.T) {
	sks := []skills.Skill{
		{Name: "a", CreatedBy: "agent", Related: []string{"nonexistent"}},
	}
	g := Build(sks, nil, nil)
	for _, e := range g.Edges {
		if e.Reason == "related_skills" {
			t.Error("指向不存在技能的边应被忽略")
		}
	}
}

func TestMemoryNodesAndLexicalEdges(t *testing.T) {
	mem := map[string][]string{
		memory.CategoryProject: {"docker 容器重启策略要用 unless-stopped"},
		memory.CategoryUser:    {"偏好结论先行"},
	}
	g := Build(sampleSkills(), nil, mem)
	memNodes := 0
	for _, n := range g.Nodes {
		if n.Kind == "memory" {
			memNodes++
		}
	}
	if memNodes != 2 {
		t.Errorf("应有 2 个记忆节点，got %d", memNodes)
	}
	// docker 记忆应与 docker-ops 技能连边（词法重叠）
	var found bool
	for _, e := range g.Edges {
		if e.Reason == "lexical" && strings.Contains(e.Target, "docker-ops") && strings.Contains(e.Source, "memory:") {
			found = true
		}
		if e.Reason == "lexical" && strings.Contains(e.Source, "docker-ops") && strings.Contains(e.Target, "memory:") {
			found = true
		}
	}
	if !found {
		t.Errorf("docker 记忆应与 docker-ops 有词法边: %+v", g.Edges)
	}
}

func TestStats(t *testing.T) {
	g := Build(sampleSkills(), map[string]int{"docker-ops": 2}, nil)
	st := g.Stats
	// docker-ops(agent) + sql-tuning(learn) + k8s-debug（被 docker-ops 的 related 拉入）
	if st.Skills != 3 {
		t.Errorf("技能节点应 3 个（含被 related 拉入的 k8s-debug），got %d", st.Skills)
	}
	if st.Used != 1 {
		t.Errorf("used=1，got %d", st.Used)
	}
	if st.AgentCreated != 2 {
		t.Errorf("agent_created=2，got %d", st.AgentCreated)
	}
	if st.Categories["devops"] != 2 {
		t.Errorf("devops 分类应 2（docker-ops + k8s-debug），got %v", st.Categories)
	}
}

func TestIsolatedPct(t *testing.T) {
	sks := []skills.Skill{{Name: "solo", CreatedBy: "agent"}}
	g := Build(sks, nil, nil)
	if g.Stats.IsolatedPct != 100 {
		t.Errorf("单节点无边应 100%% 孤立，got %v", g.Stats.IsolatedPct)
	}
}

func TestTokenizeCJKAndASCII(t *testing.T) {
	toks := tokenize("docker 容器运维")
	if !toks["docker"] {
		t.Error("应含 docker")
	}
	if !toks["容器"] || !toks["器运"] {
		t.Errorf("应含 CJK 2-gram: %v", toks)
	}
	if toks["ab"] {
		t.Error("短 ASCII 不应入 token")
	}
}

func TestBuildEmpty(t *testing.T) {
	g := Build(nil, nil, nil)
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Error("空输入应产出空图")
	}
}
