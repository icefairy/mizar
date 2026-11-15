package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkSkill(t *testing.T, dir, name string, fm map[string]string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	os.MkdirAll(sub, 0o755)
	var sb strings.Builder
	sb.WriteString("---\nname: " + name + "\n")
	for k, v := range fm {
		sb.WriteString(k + ": " + v + "\n")
	}
	sb.WriteString("---\nbody\n")
	os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte(sb.String()), 0o644)
}

func loadTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	mkSkill(t, dir, "project-mizar", map[string]string{
		"description": "Mizar 项目的约定与命令", "triggers": "[mizar, 开阳, go build]",
	})
	mkSkill(t, dir, "project-feihe", map[string]string{
		"description": "飞鹤输入法项目", "triggers": "[feihe, 输入法]",
	})
	mkSkill(t, dir, "docker-ops", map[string]string{"description": "容器运维"})
	mkSkill(t, dir, "always-handoff", map[string]string{"description": "交接", "always": "true"})
	m := NewManager(dir)
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("加载失败: %v", failed)
	}
	return m
}

// TestRelevantByTaskText 任务提到项目名 → 只注入该项目技能（+常驻）。
func TestRelevantByTaskText(t *testing.T) {
	m := loadTestManager(t)
	picked, hidden := m.Relevant("帮我修一下 mizar 的编译错误", RelevanceOptions{MaxSkills: 8})
	names := map[string]bool{}
	for _, s := range picked {
		names[s.Name] = true
	}

	if !names["project-mizar"] {
		t.Errorf("应命中 project-mizar，got %v", names)
	}
	if names["project-feihe"] {
		t.Error("不该注入无关项目 feihe")
	}
	if !names["always-handoff"] {
		t.Error("always:true 的技能应常驻")
	}
	if hidden == 0 {
		t.Error("应有未列出的技能（体现按需检索的必要）")
	}
}

// TestRelevantTriggerWord 触发词命中。
func TestRelevantTriggerWord(t *testing.T) {
	m := loadTestManager(t)
	picked, _ := m.Relevant("输入法候选词的排序逻辑", RelevanceOptions{})
	found := false
	for _, s := range picked {
		if s.Name == "project-feihe" {
			found = true
		}
	}
	if !found {
		t.Error("触发词「输入法」应命中 project-feihe")
	}
}

// TestRelevantNoTask 无任务文本时退化为前 N（可预期）。
func TestRelevantNoTask(t *testing.T) {
	m := loadTestManager(t)
	picked, hidden := m.Relevant("", RelevanceOptions{MaxSkills: 2})
	if len(picked) > 2 {
		t.Errorf("应最多 2 个，got %d", len(picked))
	}
	if hidden <= 0 {
		t.Error("应有剩余未列出")
	}
}

// TestRenderRelevantHasHint 渲染结果要提示「还有 N 个可用 skill_search」。
func TestRenderRelevantHasHint(t *testing.T) {
	m := loadTestManager(t)
	out := m.RenderRelevant("mizar 编译", RelevanceOptions{MaxSkills: 2})
	if !strings.Contains(out, "skill_search") {
		t.Errorf("应提示用 skill_search 检索:\n%s", out)
	}
	if !strings.Contains(out, "另有") {
		t.Errorf("应说明还有多少个未列出:\n%s", out)
	}
}

// TestSearch Skill_search 关键词检索。
func TestSearch(t *testing.T) {
	m := loadTestManager(t)
	hits := m.Search("输入法", 10)
	if len(hits) == 0 || hits[0].Name != "project-feihe" {
		t.Errorf("应命中 project-feihe，got %v", hits)
	}
	if len(m.Search("完全不存在的东西zzz", 10)) != 0 {
		t.Error("无关查询应无结果")
	}
}

// TestScoreThresholds 打分规则可解释性。
func TestScoreThresholds(t *testing.T) {
	s := &Skill{Name: "project-mizar", Description: "mizar 项目", Triggers: []string{"开阳"}, Category: "devops"}
	// 技能名 "project-mizar" 不出现在 "mizar 编译" 里（子串不匹配），
	// 所以这条只能靠描述词重叠得分；用完整名测强信号。
	if n := s.Score("project-mizar 编译", map[string]bool{"mizar": true}); n < 6 {
		t.Errorf("技能全名命中应 ≥6，got %d", n)
	}
	if n := s.Score("开阳的东西", nil); n < 4 {
		t.Errorf("触发词命中应 ≥4，got %d", n)
	}
	if n := s.Score("完全无关", nil); n != 0 {
		t.Errorf("无关应 0，got %d", n)
	}
}

// TestChineseRecallNoSpaces 回归：中文任务无空格也必须能召回（曾 score=0 全漏）。
func TestChineseRecallNoSpaces(t *testing.T) {
	m := loadTestManager(t)
	cases := []string{
		"帮我把开阳的构建脚本修一下",   // 触发词「开阳」
		"修一下 mizar 的构建脚本", // 名字 mizar
		"项目构建命令报错了",       // 只有描述能对上
	}
	for _, task := range cases {
		hits := m.Search(task, 3)
		found := false
		for _, h := range hits {
			if h.Name == "project-mizar" {
				found = true
			}
		}
		if !found {
			t.Errorf("任务 %q 应召回 project-mizar，got %v", task, namesOf(hits))
		}
	}
	// 无关任务不该召回
	if hits := m.Search("今天天气怎么样", 3); len(hits) != 0 {
		t.Errorf("无关任务不该有结果: %v", namesOf(hits))
	}
}

func namesOf(ss []Skill) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

// TestTokenizeCJK 分词：中文出 unigram+bigram，英文出整词。
func TestTokenizeCJK(t *testing.T) {
	toks := tokenize("构建 Go build")
	if !toks["构建"] || !toks["构"] || !toks["建"] {
		t.Errorf("中文应出 unigram+bigram: %v", toks)
	}
	if !toks["build"] {
		t.Errorf("英文应出整词: %v", toks)
	}
	if toks["b"] {
		t.Errorf("英文单字母不该入索引: %v", toks)
	}
}
