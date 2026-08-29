package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/plugins"
)

// Question 单个问题（对齐 dsh tool-ask-user schema）。
type Question struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Header     string     `json:"header,omitempty"`
	Options    []Option   `json:"options,omitempty"`
	MultiSelect bool      `json:"multi_select,omitempty"`
}

// Option 单选/多选选项。
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer 单个问题的回答（id 对应 Question.ID）。
type Answer struct {
	ID        string   `json:"id"`
	Value     string   `json:"value"`
	Values    []string `json:"values,omitempty"` // multi_select
}

// toolAskUser 返回 ask_user_question 工具。
//
// 行为：
//   - TUI 模式（Agent.AskUser 非 nil）：阻塞等待用户在输入框提交答案（由 TUI 实现 channel 轮询）
//   - Server/CLI 模式（AskUser == nil）：返回降级提示 "无交互界面，请基于已有信息自主决策"
func toolAskUser() plugins.Tool {
	return plugins.Tool{
		Name:        "ask_user_question",
		Description: "Ask the user a concise question when you need confirmation, a choice, or missing information before proceeding. Send one or more questions with stable ids.",
		Run: func(args string) (string, error) {
			if agentAskUser == nil {
				return "⚠️ 当前环境无交互界面（非 TUI 模式），无法向用户提问。请基于已有信息自主决策，或切换到交互式模式重试。", nil
			}
			var qs []Question
			if err := json.Unmarshal([]byte(args), &qs); err != nil || len(qs) == 0 {
				return "", fmt.Errorf("ask_user_question: args must be a non-empty JSON array of questions with id/question fields")
			}
			for _, q := range qs {
				if q.ID == "" || q.Question == "" {
					return "", fmt.Errorf("ask_user_question: each question needs non-empty id and question")
				}
			}
			qb, _ := json.Marshal(qs)
			ab, err := agentAskUser(string(qb))
			if err != nil {
				return "", fmt.Errorf("ask_user_question: %w", err)
			}
			var answers []Answer
			if err := json.Unmarshal([]byte(ab), &answers); err != nil {
				return "", fmt.Errorf("ask_user_question: invalid answer format: %w", err)
			}
			// 构建人类可读汇总
			var sb strings.Builder
			sb.WriteString("✅ 已收到用户回答：\n")
			for _, a := range answers {
				q := findQ(qs, a.ID)
				label := a.Value
				if q != nil && len(q.Options) > 0 {
					for _, o := range q.Options {
						if o.Label == label {
							label = o.Label
							if o.Description != "" {
								label += " (" + o.Description + ")"
							}
							break
						}
					}
				}
				if q.MultiSelect && len(a.Values) > 0 {
					label = strings.Join(a.Values, ", ")
				}
				fmt.Fprintf(&sb, "  • %s: %s\n", qIDShort(a.ID), label)
			}
			return sb.String(), nil
		},
	}
}

func findQ(qs []Question, id string) *Question {
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i]
		}
	}
	return nil
}

func qIDShort(id string) string {
	if len(id) > 20 {
		return id[:20] + "…"
	}
	return id
}
