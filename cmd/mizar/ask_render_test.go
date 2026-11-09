package main

import (
	"strings"
	"testing"

	"mizar/internal/builtins"
)

// 回归：ask_user_question 的题目与选项必须真的出现在聊天区渲染文本里。
//
// 旧实现把这段拼在 m.textView.SetText(sb.String()) 之后，追加的内容永远不会
// 被画出来 —— 表现就是用户看不到题目/选项，无法作答，只能干等到超时。
func TestRenderAskPromptShowsQuestionAndOptions(t *testing.T) {
	p := &askPending{
		qs: []builtins.Question{{
			ID:       "erupt_target",
			Question: "erupt 升级目标选哪个？",
			Options: []builtins.Option{
				{Label: "最新 2.4.0", Description: "JDK17"},
				{Label: "维持 1.12.x"},
			},
		}},
	}
	out := renderAskPrompt(p)
	// 去掉 tview 颜色标签后检查真实可见文本
	visible := stripColorTags(out)
	for _, want := range []string{
		"需要你的选择",
		"erupt 升级目标选哪个？",
		"最新 2.4.0",
		"维持 1.12.x",
		"1)",
		"2)",
	} {
		if !strings.Contains(visible, want) {
			t.Fatalf("渲染缺失 %q:\n%s", want, visible)
		}
	}
}

// 多问题时只渲染当前一问，并带 (第 N/M 问) 进度。
func TestRenderAskPromptMultiShowsCurrentOnly(t *testing.T) {
	p := &askPending{
		qs: []builtins.Question{
			{ID: "a", Question: "第一个问题？"},
			{ID: "b", Question: "第二个问题？"},
		},
		idx: 1,
	}
	visible := stripColorTags(renderAskPrompt(p))
	if !strings.Contains(visible, "第 2/2 问") {
		t.Fatalf("缺少进度提示:\n%s", visible)
	}
	if !strings.Contains(visible, "第二个问题？") {
		t.Fatalf("应渲染当前问题:\n%s", visible)
	}
	if strings.Contains(visible, "第一个问题？") {
		t.Fatalf("已答过的问题不应重复渲染:\n%s", visible)
	}
}

// stripColorTags 去掉 tview 的 [color]text[::-] 标签。
func stripColorTags(s string) string {
	var sb strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			depth++
			continue
		}
		if s[i] == ']' && depth > 0 {
			depth--
			continue
		}
		if depth == 0 {
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}
