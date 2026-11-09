package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/plugins"
)

// Question 单个问题（对齐 dsh tool-ask-user schema）。
type Question struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

// Option 单选/多选选项。
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Answer 单个问题的回答（id 对应 Question.ID）。
type Answer struct {
	ID     string   `json:"id"`
	Value  string   `json:"value"`
	Values []string `json:"values,omitempty"` // multi_select
}

// toolAskUser 返回 ask_user_question 工具。
//
// 行为：
//   - TUI 模式（Agent.AskUser 非 nil）：阻塞等待用户在输入框提交答案（由 TUI 实现 channel 轮询）
//   - Server/CLI 模式（AskUser == nil）：返回降级提示 "无交互界面，请基于已有信息自主决策"
func toolAskUser() plugins.Tool {
	return plugins.Tool{
		Name:        "ask_user_question",
		Description: "Ask the user a concise question when you need confirmation, a choice, or missing information before proceeding. Send one or more questions with stable ids. When options are provided the user answers by picking a number (interactive terminals) or by replying with the number in a follow-up message (non-interactive environments). Args: {questions: array(of question objects: id string, question string, header? string, options? array of label/description, multi_select? bool)}",
		Run: func(args string) (string, error) {
			if agentAskUser == nil {
				// 无交互界面（Server/管道/CI）：不能阻塞等待，但可变相达成——
				// 让模型把选项写进正文，用户下一条消息回编号。
				if qs, err := parseQuestions(args); err == nil && len(qs) > 0 {
					return "⚠️ 当前环境无交互界面，无法阻塞等待回答。请立刻把下面的选项作为普通文本写进你的回复正文，" +
						"请用户在下一条消息中回复编号（不要重试本工具）：\n\n" + RenderPlainQuestions(qs), nil
				}
				return "⚠️ 当前环境无交互界面（非 TUI 模式），无法向用户提问。请基于已有信息自主决策，或切换到交互式模式重试。", nil
			}
			qs, err := parseQuestions(args)
			if err != nil {
				return "", err
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

// parseQuestions 宽容解析模型传入的问题参数，返回值一定是非空 []Question。
//
// 为什么不只认裸数组：Tool.Description 若没写 Args 段，ArgsSchema() 会兜底成
// {args: string}，各家模型于是按自己的习惯包装参数，实测出现过
// {"questions": [...]}、{"args": "[{...}]"}、{"args": {"questions": "[...]"}}
// 等形态。这里统一剥壳归一化，避免一次形状不匹配就整轮失败（模型通常原样重试）。
func parseQuestions(args string) ([]Question, error) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &v); err != nil {
		return nil, fmt.Errorf("ask_user_question: 参数不是合法 JSON（应为问题数组 [{\"id\":..,\"question\":..}]）：%v", err)
	}
	// 最多剥 3 层：args 键 / questions 键 / 字符串化的 JSON
	for depth := 0; depth < 3; depth++ {
		if s, ok := v.(string); ok {
			var inner any
			if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &inner); err != nil {
				break
			}
			v = inner
			continue
		}
		if m, ok := v.(map[string]any); ok {
			var next any
			found := false
			for _, key := range []string{"questions", "args", "items"} {
				if x, ok := m[key]; ok {
					next, found = x, true
					break
				}
			}
			if !found {
				break
			}
			v = next
			continue
		}
		break
	}

	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("ask_user_question: 需要非空的问题数组，例如 [{\"id\":\"q1\",\"question\":\"选哪个？\",\"options\":[{\"label\":\"A\"}]}]")
	}
	var qs []Question
	for _, item := range arr {
		b, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("ask_user_question: 问题项无法解析：%v", err)
		}
		var q Question
		if err := json.Unmarshal(b, &q); err != nil {
			return nil, fmt.Errorf("ask_user_question: 问题项格式错误（需要 id/question 字段）：%v", err)
		}
		// 兼容模型用 title/text 代替 question 的写法
		if q.Question == "" {
			var alt struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			}
			_ = json.Unmarshal(b, &alt)
			if alt.Title != "" {
				q.Question = alt.Title
			} else if alt.Text != "" {
				q.Question = alt.Text
			}
		}
		// 缺 id 时按序号补，避免整轮失败（id 只用于回填答案）
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", len(qs)+1)
		}
		if q.Question == "" {
			return nil, fmt.Errorf("ask_user_question: 第 %d 个问题缺少 question 文本", len(qs)+1)
		}
		qs = append(qs, q)
	}
	return qs, nil
}

// RenderPlainQuestions 把问题渲染成纯文本选项清单（无交互环境的降级回退：
// 让模型把选项写进正文，用户下一条消息回复编号）。
func RenderPlainQuestions(qs []Question) string {
	var sb strings.Builder
	for i, q := range qs {
		if len(qs) > 1 {
			fmt.Fprintf(&sb, "【问题 %d/%d】%s\n", i+1, len(qs), q.Question)
		} else {
			fmt.Fprintf(&sb, "【提问】%s\n", q.Question)
		}
		for j, o := range q.Options {
			fmt.Fprintf(&sb, "  %d) %s", j+1, o.Label)
			if o.Description != "" {
				fmt.Fprintf(&sb, " — %s", o.Description)
			}
			sb.WriteString("\n")
		}
		if len(q.Options) == 0 {
			sb.WriteString("  （无预设选项，请直接回复文字）\n")
			continue
		}
		hint := "直接回车"
		if q.MultiSelect {
			hint = "多选：回复 1,3 或 1 3；直接回车"
		}
		fmt.Fprintf(&sb, "  %s = 1) %s\n", hint, q.Options[0].Label)
	}
	return sb.String()
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
