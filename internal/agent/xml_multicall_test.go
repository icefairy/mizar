package agent

import (
	"strings"
	"testing"
)

// XML 协议下发多个 <tool> 标签：必须全部提取且保序，不能只取首个。
// 旧实现用 FindStringSubmatch（只取首个），后续标签被静默丢弃。
func TestExtractActionXMLMultipleTools(t *testing.T) {
	text := `<tool name="read">{"path":"a"}</tool>` + "\n" +
		`<tool name="write">{"path":"b"}</tool>` + "\n" +
		`<tool name="bash">{"command":"ls"}</tool>`

	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 3 {
		t.Fatalf("应提取 3 个调用, got %d: %+v", len(reqs), reqs)
	}
	want := []string{"read", "write", "bash"}
	for i, w := range want {
		if reqs[i].Tool != w {
			t.Fatalf("第 %d 个应为 %q, got %q（顺序不能变）", i, w, reqs[i].Tool)
		}
		if reqs[i].Action != "tool" {
			t.Fatalf("第 %d 个 action 应为 tool, got %q", i, reqs[i].Action)
		}
	}
	if reqs[0].Args != `{"path":"a"}` {
		t.Fatalf("args 应原样保留, got %q", reqs[0].Args)
	}
}

// 单个 <tool> 标签：既有行为不能变。
func TestExtractActionXMLSingleUnchanged(t *testing.T) {
	reqs := parseCallJSONsOrFail(t, `先说明一下。`+"\n"+`<tool name="read">{"path":"x"}</tool>`)
	if len(reqs) != 1 || reqs[0].Tool != "read" {
		t.Fatalf("单个 XML 工具调用应正常解析, got %+v", reqs)
	}
}

// arg_key/arg_value 形态：非贪婪分隔符不能互相吞并。
func TestExtractActionXMLArgKeyValueMultiple(t *testing.T) {
	text := `<tool name="a"><arg_key>p</arg_key><arg_value>1</arg_value></tool>` + "\n" +
		`<tool name="b"><arg_key>q</arg_key><arg_value>2</arg_value></tool>`
	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 2 {
		t.Fatalf("arg_key/arg_value 形态应提取 2 个, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Tool != "a" || reqs[1].Tool != "b" {
		t.Fatalf("工具名/顺序不对: %+v", reqs)
	}
	// 贪婪匹配的典型症状：第一个调用吞掉第二个标签
	if strings.Contains(reqs[0].Args, "</tool>") {
		t.Fatalf("第一个调用的 args 不应包含后续标签（贪婪匹配 bug）: %q", reqs[0].Args)
	}
}

// <reply> 形态：不能因多工具改造而失效。
func TestExtractActionXMLReplyUnchanged(t *testing.T) {
	reqs := parseCallJSONsOrFail(t, `<reply>最终答案</reply>`)
	if len(reqs) != 1 || reqs[0].Action != "reply" || reqs[0].Text != "最终答案" {
		t.Fatalf("reply 应正常解析, got %+v", reqs)
	}
}

// 带引号的 "tool 变体（模型偶尔写成 "tool name=...）。
func TestExtractActionXMLQuotedToolVariant(t *testing.T) {
	reqs := parseCallJSONsOrFail(t, `"tool name="read">{"path":"x"}</tool>`)
	if len(reqs) != 1 || reqs[0].Tool != "read" {
		t.Fatalf("带引号的变体应被识别, got %+v", reqs)
	}
}

// extractAssistantText：subagent 兜底取文本的依据。
func TestExtractAssistantText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"reply 正常", `{"action":"reply","text":"结果正文"}`, "结果正文"},
		{"不带 action 的 reason", `{"reason":"我先读一下文件"}`, "我先读一下文件"},
		{"工具调用轮无文字", `{"action":"tool","tool":"read","args":"{}"}`, ""},
		{"多调用轮无文字", `{"action":"tool","calls":[{"tool":"a","args":"{}"}]}`, ""},
		{"纯文本直说", "我直接说结果", "我直接说结果"},
		{"畸形工具 JSON", `{"action":"tool","tool":"bash","args":"{"command":"ls"}"}`, ""},
		{"空", "", ""},
	}
	for _, c := range cases {
		if got := extractAssistantText(c.in); got != c.want {
			t.Fatalf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// parseCallJSONsOrFail 断言解析成功（测试辅助）。
func parseCallJSONsOrFail(t *testing.T, text string) []callRequest {
	t.Helper()
	reqs, err := parseCallJSONs(text)
	if err != nil {
		t.Fatalf("解析失败: %v (text=%q)", err, text)
	}
	return reqs
}
