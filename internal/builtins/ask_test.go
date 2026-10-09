package builtins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUserQuestionNoInteractiveFallsBackToText(t *testing.T) {
	SetAskUser(nil) // 模拟 Server/管道：无交互界面
	tool := toolAskUser()
	out, err := tool.Run(`[{"id":"keep_data","question":"是否保留数据目录？",
"options":[{"label":"保留","description":"只删二进制"},{"label":"一并删除"}]}]`)
	if err != nil {
		t.Fatalf("降级路径不应报错: %v", err)
	}
	// 关键：不能只说「请自行决策」，必须把选项给出来让用户下一条回复编号
	for _, want := range []string{"保留", "一并删除", "1)", "2)", "回复编号"} {
		if !strings.Contains(out, want) {
			t.Fatalf("降级提示缺少 %q:\n%s", want, out)
		}
	}
}

func TestAskUserQuestionNoInteractiveNoOptions(t *testing.T) {
	SetAskUser(nil)
	out, err := toolAskUser().Run(`[{"id":"a","question":"项目叫什么？"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "项目叫什么") {
		t.Fatalf("应保留原问题文本:\n%s", out)
	}
}

func TestAskUserQuestionWithCallback(t *testing.T) {
	SetAskUser(func(questionsJSON string) (string, error) {
		// 回调收到的问题 JSON 应可解析
		var qs []Question
		if err := json.Unmarshal([]byte(questionsJSON), &qs); err != nil {
			t.Fatalf("回调收到非法 JSON: %v", err)
		}
		if len(qs) != 1 || qs[0].ID != "pick" {
			t.Fatalf("问题传递有误: %+v", qs)
		}
		return `[{"id":"pick","value":"方案B"}]`, nil
	})
	defer SetAskUser(nil)
	out, err := toolAskUser().Run(`[{"id":"pick","question":"选哪个？","options":[{"label":"方案A"},{"label":"方案B"}]}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "方案B") {
		t.Fatalf("汇总应包含用户选择:\n%s", out)
	}
}

func TestAskUserQuestionRejectsBadArgs(t *testing.T) {
	SetAskUser(func(string) (string, error) { return `[]`, nil })
	defer SetAskUser(nil)
	if _, err := toolAskUser().Run(`[]`); err == nil {
		t.Fatal("空数组应报错")
	}
	if _, err := toolAskUser().Run(`[{"id":"","question":"x"}]`); err == nil {
		t.Fatal("缺 id 应报错")
	}
}

func TestRenderPlainQuestionsMulti(t *testing.T) {
	out := RenderPlainQuestions([]Question{
		{ID: "a", Question: "A?", Options: []Option{{Label: "a1"}}},
		{ID: "b", Question: "B?"},
	})
	if !strings.Contains(out, "问题 1/2") || !strings.Contains(out, "问题 2/2") {
		t.Fatalf("多问题应带序号:\n%s", out)
	}
	if !strings.Contains(out, "直接回复文字") {
		t.Fatalf("无选项问题应有提示:\n%s", out)
	}
}
