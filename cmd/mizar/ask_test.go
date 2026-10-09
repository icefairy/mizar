package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"mizar/internal/builtins"
)

const twoOptions = `[{"id":"keep_data","question":"除了删除可执行文件，是否保留数据目录？",
"header":"清理范围",
"options":[{"label":"保留数据目录","description":"只删二进制，数据不动"},
{"label":"一并删除数据目录","description":"彻底清理"}]}]`

func TestRenderQuestionsShowsNumberedOptions(t *testing.T) {
	out := renderQuestions(twoOptions)
	for _, want := range []string{"保留数据目录", "一并删除数据目录", "1)", "2)", "直接回车"} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染结果缺少 %q:\n%s", want, out)
		}
	}
}

func TestRenderQuestionsFallsBackToRaw(t *testing.T) {
	if got := renderQuestions("不是 JSON"); got != "不是 JSON" {
		t.Fatalf("非法 JSON 应原样返回，得到 %q", got)
	}
}

func TestAnswerForPickByNumber(t *testing.T) {
	qs := parseQs(t, twoOptions)
	if got := answerFor(qs[0], "2").Value; got != "一并删除数据目录" {
		t.Fatalf("选 2 应得第二项 label，得到 %q", got)
	}
	// 括号/右括号形式也应识别
	if got := answerFor(qs[0], "(1)").Value; got != "保留数据目录" {
		t.Fatalf("选 (1) 应得第一项 label，得到 %q", got)
	}
}

func TestAnswerForEmptyLinePicksFirst(t *testing.T) {
	qs := parseQs(t, twoOptions)
	if got := answerFor(qs[0], "  ").Value; got != "保留数据目录" {
		t.Fatalf("直接回车应取第一项，得到 %q", got)
	}
}

func TestAnswerForFreeText(t *testing.T) {
	qs := parseQs(t, twoOptions)
	// 非数字输入按自由文本；越界编号同样退化为自由文本（不静默取错项）
	for _, in := range []string{"我自己定", "9"} {
		if got := answerFor(qs[0], in).Value; got != in {
			t.Fatalf("自由输入 %q 应原样保留，得到 %q", in, got)
		}
	}
}

func TestAnswerForMultiSelect(t *testing.T) {
	qs := parseQs(t, `[{"id":"scope","question":"删哪些？","multi_select":true,
"options":[{"label":"二进制"},{"label":"配置"},{"label":"日志"}]}]`)
	got := answerFor(qs[0], "1, 3")
	if len(got.Values) != 2 || got.Values[0] != "二进制" || got.Values[1] != "日志" {
		t.Fatalf("多选 1,3 应为 [二进制 日志]，得到 %+v", got)
	}
}

func TestAnswerForNoOptions(t *testing.T) {
	qs := parseQs(t, `[{"id":"name","question":"叫什么？"}]`)
	if got := answerFor(qs[0], "mizar").Value; got != "mizar" {
		t.Fatalf("无选项时应原样返回输入，得到 %q", got)
	}
}

func TestAskUserTextCollectsAnswers(t *testing.T) {
	inputs := []string{"2"}
	i := 0
	read := func(string) (string, error) { s := inputs[i]; i++; return s, nil }
	out, err := askUserText(twoOptions, io.Discard, read)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var ans []builtins.Answer
	if err := json.Unmarshal([]byte(out), &ans); err != nil {
		t.Fatalf("答案不是合法 JSON: %v (%s)", err, out)
	}
	if len(ans) != 1 || ans[0].ID != "keep_data" || ans[0].Value != "一并删除数据目录" {
		t.Fatalf("答案不符: %+v", ans)
	}
}

func TestAskUserTextEOFReturnsEmpty(t *testing.T) {
	read := func(string) (string, error) { return "", io.EOF }
	out, err := askUserText(twoOptions, io.Discard, read)
	if err != nil {
		t.Fatalf("EOF 不应报错（任务不该中断）: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("EOF 应返回空答案列表，得到 %q", out)
	}
}

func TestAskUserTextMultipleQuestions(t *testing.T) {
	two := `[{"id":"a","question":"A?","options":[{"label":"a1"},{"label":"a2"}]},
{"id":"b","question":"B?","options":[{"label":"b1"},{"label":"b2"}]}]`
	inputs := []string{"1", "b2"} // 第二问用编号（无前缀）
	i := 0
	read := func(string) (string, error) { s := inputs[i]; i++; return s, nil }
	out, err := askUserText(two, io.Discard, read)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var ans []builtins.Answer
	if err := json.Unmarshal([]byte(out), &ans); err != nil {
		t.Fatalf("答案不是合法 JSON: %v", err)
	}
	if len(ans) != 2 || ans[0].Value != "a1" || ans[1].Value != "b2" {
		t.Fatalf("两问答案不符: %+v", ans)
	}
}

func TestAskUserTextReadError(t *testing.T) {
	read := func(string) (string, error) { return "", errors.New("boom") }
	if _, err := askUserText(twoOptions, io.Discard, read); err != nil {
		t.Fatalf("读取失败应降级为空答案而非报错: %v", err)
	}
}

func parseQs(t *testing.T, js string) []builtins.Question {
	t.Helper()
	var qs []builtins.Question
	if err := json.Unmarshal([]byte(js), &qs); err != nil {
		t.Fatalf("测试数据非法: %v", err)
	}
	return qs
}
