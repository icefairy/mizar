package agent

import (
	"strings"
	"testing"
)

// 系统提示与纠错提示都**不得引导模型使用 XML**。
//
// 背景（实测）：纠错提示曾说「或用 XML：<tool name="bash">...</tool>」，
// 把模型从 JSON 引到了 XML —— 而 XML 是兼容性最差的形态（模型从不闭合标签：
// 实测 4 次 XML 输出全部失败：`[^<]+` 遇 < 断掉、缺 </tool>、串出 </system>）。
// 教 XML 等于主动引导模型走最差路径，必须杜绝。
func TestPromptsDoNotTeachXML(t *testing.T) {
	a, _, _ := newTestAgent()
	if p := a.SystemPrompt(); strings.Contains(p, "<tool name=") {
		t.Fatal("系统提示不应包含 XML 工具标签")
	}
}

// 解析失败时注入的纠错提示不得出现 XML 教法。
func TestParseErrorsDoNotSuggestXML(t *testing.T) {
	bad := []string{
		`{"action":"tool","tool":"bash","args":"{"command":"ls"}"}`, // args 内层引号未转义
		`{"tool":"bash"}`, // 缺 action
		`完全无法识别的文本`,
	}
	for _, in := range bad {
		_, err := parseCallJSONs(in)
		if err == nil {
			continue
		}
		msg := err.Error()
		for _, banned := range []string{"或用 XML", "请使用 XML", "<tool name=", "<reply>"} {
			if strings.Contains(msg, banned) {
				t.Fatalf("纠错提示不得引导 XML（含 %q）：\n%s", banned, msg)
			}
		}
	}
}

// 纠错提示应给出 JSON 正确写法（正向引导没丢）。
func TestParseErrorsStillGuideJSON(t *testing.T) {
	_, err := parseCallJSONs(`{"action":"tool","tool":"bash","args":"{"command":"ls"}"}`)
	if err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
	if !strings.Contains(err.Error(), `"action"`) {
		t.Fatalf("纠错提示应给出 JSON 示例: %s", err.Error())
	}
}
