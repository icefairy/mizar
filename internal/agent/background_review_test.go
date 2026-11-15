package agent

import (
	"errors"
	"os"
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
