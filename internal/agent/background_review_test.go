package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type countingAux struct {
	calls int
	reply string
	err   error
}

func (c *countingAux) Chat(msgs []Message) (string, error) {
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	return c.reply, nil
}

// TestBgReviewAdvanceWithoutStart 回归：不调 Start() 也应触发（2026-11-15 审计修复）。
func TestBgReviewAdvanceWithoutStart(t *testing.T) {
	aux := &countingAux{reply: `{"action":"none"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{MinTurnsBetweenReviews: 1, AuxLLM: aux})
	for i := 0; i < 3; i++ {
		br.Advance("resp", nil)
	}
	waitFor(t, 2*time.Second, func() bool { return aux.calls > 0 })
	if aux.calls == 0 {
		t.Fatal("未调 Start 时也必须触发 review（此前完全失效）")
	}
}

// TestBgReviewDisabled 显式关闭时不触发。
func TestBgReviewDisabled(t *testing.T) {
	off := false
	aux := &countingAux{reply: `{"action":"none"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{Enabled: &off, MinTurnsBetweenReviews: 1, AuxLLM: aux})
	for i := 0; i < 5; i++ {
		br.Advance("resp", nil)
	}
	time.Sleep(200 * time.Millisecond)
	if aux.calls != 0 {
		t.Errorf("Enabled=false 时不应触发，实际 %d 次", aux.calls)
	}
}

// TestBgReviewThrottle 未到轮次阈值不触发。
func TestBgReviewThrottle(t *testing.T) {
	aux := &countingAux{reply: `{"action":"none"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{MinTurnsBetweenReviews: 3, AuxLLM: aux})
	br.Advance("r", nil)
	br.Advance("r", nil)
	time.Sleep(150 * time.Millisecond)
	if aux.calls != 0 {
		t.Errorf("2 轮 < 阈值 3，不应触发，实际 %d", aux.calls)
	}
	br.Advance("r", nil) // 第 3 轮
	waitFor(t, 2*time.Second, func() bool { return aux.calls > 0 })
	if aux.calls == 0 {
		t.Error("第 3 轮应触发")
	}
}

// TestBgReviewNoAuxLLM 无辅助模型时静默不触发。
func TestBgReviewNoAuxLLM(t *testing.T) {
	br := NewBackgroundReview(BackgroundReviewConfig{MinTurnsBetweenReviews: 1})
	br.Advance("resp", nil) // 不应 panic
}

// TestBgReviewOnMemoryCallback 记忆写入必须走 OnMemory 回调（不碰裸文件）。
func TestBgReviewOnMemoryCallback(t *testing.T) {
	aux := &countingAux{reply: `{"action":"memory","entry":"用户偏好结论先行"}`}
	var got string
	br := NewBackgroundReview(BackgroundReviewConfig{
		MinTurnsBetweenReviews: 1,
		AuxLLM:                 aux,
		OnMemory: func(entry string) error {
			got = entry
			return nil
		},
	})
	br.Advance("resp", nil)
	waitFor(t, 2*time.Second, func() bool { return got != "" })
	if got != "用户偏好结论先行" {
		t.Errorf("OnMemory 未收到条目，got %q", got)
	}
}

// TestBgReviewOnMemoryError 回调报错不影响主流程。
func TestBgReviewOnMemoryError(t *testing.T) {
	aux := &countingAux{reply: `{"action":"memory","entry":"x"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{
		MinTurnsBetweenReviews: 1,
		AuxLLM:                 aux,
		OnMemory:               func(string) error { return errors.New("boom") },
	})
	br.Advance("resp", nil) // 不 panic
	waitFor(t, time.Second, func() bool { return aux.calls > 0 })
}

// TestBgReviewSkippedWhenAuxFails 辅助模型失败时静默。
func TestBgReviewSkippedWhenAuxFails(t *testing.T) {
	aux := &countingAux{err: errors.New("net down")}
	br := NewBackgroundReview(BackgroundReviewConfig{MinTurnsBetweenReviews: 1, AuxLLM: aux})
	br.Advance("resp", nil)
	waitFor(t, time.Second, func() bool { return aux.calls > 0 }) // 调用发生但不 panic
}

// TestBgReviewNewSkillWrite 技能写盘。
func TestBgReviewNewSkillWrite(t *testing.T) {
	dir := t.TempDir()
	aux := &countingAux{reply: `{"action":"skill","skill_name":"my-skill","skill_body":"---\nname: my-skill\n---\nbody"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{
		MinTurnsBetweenReviews: 1, AuxLLM: aux, SkillsDir: dir,
	})
	br.Advance("resp", nil)
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.ReadFile(dir + "/my-skill/SKILL.md")
		return err == nil
	})
	b, err := os.ReadFile(dir + "/my-skill/SKILL.md")
	if err != nil {
		t.Fatalf("技能未写入: %v", err)
	}
	if !strings.Contains(string(b), "my-skill") {
		t.Errorf("技能内容异常: %s", b)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- 项目技能沉淀（2026-11-15）----

func TestProjectSkillName(t *testing.T) {
	cases := []struct{ dir, want string }{
		{"/data/codes/mizar", "project-mizar"},
		{"/data/codes/My_Project", "project-my-project"},
		{"/data/codes/hmem-go", "project-hmem-go"},
		{"/", ""},
		{"", ""},
	}
	for _, c := range cases {
		br := NewBackgroundReview(BackgroundReviewConfig{WorkDir: c.dir})
		if got := br.projectSkillName(); got != c.want {
			t.Errorf("WorkDir=%q → %q want %q", c.dir, got, c.want)
		}
	}
}

func TestSanitizeSkillSlug(t *testing.T) {
	cases := map[string]string{
		"Mizar":        "mizar",
		"my project":   "my-project",
		"a__b":         "a-b",
		"--x--":        "x",
		"中文项目":         "unnamed",
		"project_v2.1": "project-v2-1",
	}
	for in, want := range cases {
		if got := sanitizeSkillSlug(in); got != want {
			t.Errorf("sanitizeSkillSlug(%q)=%q want %q", in, got, want)
		}
	}
}

// TestReviewPromptIncludesProject 提示词必须带上项目技能名与已有内容（read-before-write）。
func TestReviewPromptIncludesProject(t *testing.T) {
	dir := t.TempDir()
	// 预置一个已有项目技能
	os.MkdirAll(filepath.Join(dir, "project-mizar"), 0o755)
	os.WriteFile(filepath.Join(dir, "project-mizar", "SKILL.md"),
		[]byte("---\nname: project-mizar\n---\n## 构建\nmake build\n"), 0o644)

	br := NewBackgroundReview(BackgroundReviewConfig{
		SkillsDir: dir,
		WorkDir:   "/data/codes/mizar",
	})
	p := br.buildReviewPrompt("回复内容", nil)
	if !strings.Contains(p, "project-mizar") {
		t.Errorf("提示词应含项目技能名:\n%s", p)
	}
	if !strings.Contains(p, "make build") {
		t.Errorf("提示词应含已有技能内容（供增量更新）:\n%s", p)
	}
	if !strings.Contains(p, "/data/codes/mizar") {
		t.Errorf("提示词应含工作目录:\n%s", p)
	}
}

// TestReviewPromptNoProject 无 WorkDir 时不带项目段（保持旧行为）。
func TestReviewPromptNoProject(t *testing.T) {
	br := NewBackgroundReview(BackgroundReviewConfig{SkillsDir: t.TempDir()})
	p := br.buildReviewPrompt("回复", nil)
	if strings.Contains(p, "本项目对应的技能名") {
		t.Errorf("无 WorkDir 不应有项目段:\n%s", p)
	}
}

// TestUpdateSkillOverwrites  update_skill 应覆盖（模型已做合并）。
func TestUpdateSkillOverwrites(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "project-x"), 0o755)
	path := filepath.Join(dir, "project-x", "SKILL.md")
	os.WriteFile(path, []byte("旧内容"), 0o644)

	br := NewBackgroundReview(BackgroundReviewConfig{SkillsDir: dir})
	br.writeSkill("project-x", "旧内容 + 新规则", true)
	b, _ := os.ReadFile(path)
	if string(b) != "旧内容 + 新规则" {
		t.Errorf("update_skill 应覆盖，got %q", b)
	}
}

// TestNewSkillDoesNotOverwrite new_skill 遇到已存在文件应跳过（不冲掉已有内容）。
func TestNewSkillDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "existing"), 0o755)
	path := filepath.Join(dir, "existing", "SKILL.md")
	os.WriteFile(path, []byte("用户手写的"), 0o644)

	br := NewBackgroundReview(BackgroundReviewConfig{SkillsDir: dir})
	br.writeSkill("existing", "模型乱写", false)
	b, _ := os.ReadFile(path)
	if string(b) != "用户手写的" {
		t.Errorf("new_skill 不应覆盖已有文件，got %q", b)
	}
	// 新建的仍可写
	br.writeSkill("brand-new", "新技能", false)
	if _, err := os.Stat(filepath.Join(dir, "brand-new", "SKILL.md")); err != nil {
		t.Error("新技能应被创建")
	}
}

// TestProjectSkillEndToEnd 全链路：update_skill 动作写入项目技能。
func TestProjectSkillEndToEnd(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "project-demo"), 0o755)
	skillPath := filepath.Join(dir, "project-demo", "SKILL.md")
	os.WriteFile(skillPath, []byte("## 旧\n已有知识"), 0o644)

	aux := &countingAux{reply: `{"action":"update_skill","skill_name":"project-demo","skill_body":"## 旧\n已有知识\n\n## 新\n补充的规则"}`}
	br := NewBackgroundReview(BackgroundReviewConfig{
		MinTurnsBetweenReviews: 1,
		AuxLLM:                 aux,
		SkillsDir:              dir,
		WorkDir:                "/data/codes/demo",
	})
	br.Advance("resp", nil)
	waitFor(t, 2*time.Second, func() bool {
		b, err := os.ReadFile(skillPath)
		return err == nil && strings.Contains(string(b), "补充的规则")
	})
	b, _ := os.ReadFile(skillPath)
	if !strings.Contains(string(b), "已有知识") {
		t.Errorf("旧内容应保留（模型做合并）:\n%s", b)
	}
}

// TestProjectSkillTriggers 项目技能自动带触发词（保证「聊到 xx 项目」能召回）。
func TestProjectSkillTriggers(t *testing.T) {
	br := NewBackgroundReview(BackgroundReviewConfig{WorkDir: "/data/codes/mizar"})
	trs := br.projectSkillTriggers()
	if len(trs) == 0 || trs[0] != "mizar" {
		t.Errorf("首个触发词应为项目目录名，got %v", trs)
	}
	// 通用父目录（codes）应被跳过
	for _, tr := range trs {
		if strings.EqualFold(tr, "codes") {
			t.Errorf("通用父目录不该进触发词: %v", trs)
		}
	}
	// 空 WorkDir → 空
	if got := NewBackgroundReview(BackgroundReviewConfig{}).projectSkillTriggers(); len(got) != 0 {
		t.Errorf("空 WorkDir 应无触发词，got %v", got)
	}
}

// TestPromptSuggestsTriggers 新建项目技能时提示词要建议 triggers。
func TestPromptSuggestsTriggers(t *testing.T) {
	br := NewBackgroundReview(BackgroundReviewConfig{
		SkillsDir: t.TempDir(),
		WorkDir:   "/data/codes/mizar",
	})
	p := br.buildReviewPrompt("回复", nil)
	if !strings.Contains(p, "triggers") {
		t.Errorf("新建技能时应建议写 triggers:\n%s", p)
	}
}
