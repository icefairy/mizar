package main

// ask.go —— ask_user_question 的文本选项交互（经典 readline / TUI 共用）。
//
// 背景：模型调用 ask_user_question 时，若环境无交互界面会退化成「请自行决策」，
// 用户无法表达选择。本文件把问题渲染成带编号的文本选项，用户直接回一个数字
// （或原文）即可作答——不需要任何 GUI，也不需要 TUI。
//
// 用法：
//
//	【提问】用哪种方案？
//	  1) 方案 A — 更简单
//	  2) 方案 B — 更灵活
//	  直接回车 = 1) 方案 A
//	> 2
//
// 多选：回复 "1,3" 或 "1 3"；自定义输入：直接回文字（非纯数字/编号时不当作选项）。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mizar/internal/builtins"
)

// askReader 逐行读取用户输入（liner 与 TUI 各自实现）。
// 返回 io.EOF 表示用户放弃作答（Ctrl+D / 环境无输入）。
type askReader func(prompt string) (string, error)

// renderQuestions 把 questions JSON 渲染成人类可读的文本选项块。
// 解析失败时返回原始字符串（至少让用户看到模型问了什么）。
// 渲染细节复用 builtins.RenderPlainQuestions，保证 TUI 与经典模式提示一致。
func renderQuestions(questionsJSON string) string {
	var qs []builtins.Question
	if err := json.Unmarshal([]byte(questionsJSON), &qs); err != nil || len(qs) == 0 {
		return questionsJSON
	}
	return strings.TrimRight(builtins.RenderPlainQuestions(qs), "\n")
}

// answerFor 把用户的单行输入解析为该问题的答案。
// 纯数字/编号（"2"、"(2)"、"2)"）或逗号/空格分隔的编号 → 选项 label；
// 其余情况按原文处理（自由输入）。
func answerFor(q builtins.Question, line string) builtins.Answer {
	line = strings.TrimSpace(line)
	ans := builtins.Answer{ID: q.ID}
	if len(q.Options) == 0 {
		ans.Value = line
		return ans
	}
	if line == "" { // 直接回车 → 第一项
		ans.Value = q.Options[0].Label
		return ans
	}
	if q.MultiSelect {
		if labels, ok := pickOptions(q.Options, splitIndices(line)); ok {
			ans.Values = labels
			ans.Value = strings.Join(labels, ", ")
			return ans
		}
		ans.Value = line
		return ans
	}
	if idxs := splitIndices(line); len(idxs) == 1 {
		if labels, ok := pickOptions(q.Options, idxs); ok {
			ans.Value = labels[0]
			return ans
		}
	}
	ans.Value = line
	return ans
}

// splitIndices 把 "1,3" / "1 3" / "(2)" / "2)" 解析为 0-based 选项下标。
// 任一段不是合法数字时返回 nil（视为自由输入，不猜）。
func splitIndices(line string) []int {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '\t' || r == ';' || r == '；'
	})
	if len(fields) == 0 {
		return nil
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "()（）[]【】.")
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 {
			return nil
		}
		out = append(out, n-1)
	}
	return out
}

// pickOptions 把 0-based 下标映射为 label；越界返回 false。
func pickOptions(opts []builtins.Option, idxs []int) ([]string, bool) {
	out := make([]string, 0, len(idxs))
	for _, i := range idxs {
		if i < 0 || i >= len(opts) {
			return nil, false
		}
		out = append(out, opts[i].Label)
	}
	return out, true
}

// isTerminal 报告 f 是否为字符设备（TTY）。非 TTY（管道/重定向/CI）下不宜交互提问。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// readLineFromStdin 读取标准输入的一行（用于 -task 单次执行模式的作答）。
// 回显由终端行规则负责；EOF 时返回 io.EOF。
// 注意：bufio.Reader 必须复用（一次问答可能读多行，每次新建会因预读缓冲丢行）。
func readLineFromStdin(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stdout, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", io.EOF
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// askUserText 是经典模式（readline / 单次任务）的 AskUser 实现：
// 把问题打印到终端，逐问读取一行作答，返回 answers JSON。
// write 为 nil 时不打印（供测试静默使用）。
func askUserText(questionsJSON string, write io.Writer, read askReader) (string, error) {
	var qs []builtins.Question
	if err := json.Unmarshal([]byte(questionsJSON), &qs); err != nil || len(qs) == 0 {
		return "", fmt.Errorf("解析问题失败: %w", err)
	}
	if write == nil {
		write = io.Discard
	}
	fmt.Fprintln(write, renderQuestions(questionsJSON))
	answers := make([]builtins.Answer, 0, len(qs))
	for _, q := range qs {
		line, err := read("> ")
		if err != nil {
			// 用户放弃（EOF/Ctrl-D）：返回空答案列表，让模型看到「用户未回答」而非报错中断任务
			return "[]", nil
		}
		answers = append(answers, answerFor(q, line))
	}
	b, err := json.Marshal(answers)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
