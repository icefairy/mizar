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

// 未闭合 XML：模型只写开标签 <tool name="x">{"..."}，没有 </tool>。
//
// 实测于 Windows 会话（idx134/136）：模型把 XML 开标签与 JSON 混写，
// JSON 完整但缺闭合标签，尾部甚至串出 </system>。旧模式1 要求 `</tool>`
// 且禁用 `<`（`[^<]+`），两个条件都不满足 → 解析 0 个 → XML 被当纯文本
// → 循环视为「任务完成」→ 一条回复就结束。
func TestExtractActionXMLUnclosedTag(t *testing.T) {
	text := `<tool name="bash">{"command":"echo hi"}` // 故意不闭合
	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 1 || reqs[0].Tool != "bash" {
		t.Fatalf("未闭合的 XML 工具调用应能解析, got %+v", reqs)
	}
	if reqs[0].Args != `{"command":"echo hi"}` {
		t.Fatalf("args 应为完整 JSON, got %q", reqs[0].Args)
	}
}

// args 内含 `<` 的 bash 命令（<# 注释、<<'EOF' heredoc、重定向）。
// 旧模式1 的 `[^<]+` 遇到任何 < 就断，导致整条命令无法解析。
func TestExtractActionXMLArgsContainAngleBrackets(t *testing.T) {
	text := `<tool name="bash">{"command":"python3 - <<'EOF'\n# <# 注释\nprint(1)\nEOF"}</tool>`
	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 1 || reqs[0].Tool != "bash" {
		t.Fatalf("args 含 < 的命令应能解析, got %+v", reqs)
	}
	if !strings.Contains(reqs[0].Args, "<<'EOF'") {
		t.Fatalf("args 应保留 heredoc 内容, got %q", truncate(reqs[0].Args, 80))
	}
}

// 尾部串出其它标签（</system>）时，仍应取到那一个完整 JSON 调用。
func TestExtractActionXMLTrailingGarbage(t *testing.T) {
	text := `<tool name="read">{"path":"x"}</system></system>`
	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 1 || reqs[0].Tool != "read" {
		t.Fatalf("尾部有杂标签时应仍能解析, got %+v", reqs)
	}
	if strings.Contains(reqs[0].Args, "</system>") {
		t.Fatalf("args 不应吞入尾部杂标签: %q", reqs[0].Args)
	}
}

// 一行里多个未闭合调用：必须全部提取（JSON 边界即调用边界）。
func TestExtractActionXMLMultipleUnclosed(t *testing.T) {
	text := `<tool name="a">{"p":1}` + "\n" + `<tool name="b">{"q":2}`
	reqs := parseCallJSONsOrFail(t, text)
	if len(reqs) != 2 {
		t.Fatalf("应提取 2 个未闭合调用, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Tool != "a" || reqs[1].Tool != "b" {
		t.Fatalf("工具名/顺序不对: %+v", reqs)
	}
	if reqs[0].Args != `{"p":1}` || reqs[1].Args != `{"q":2}` {
		t.Fatalf("args 应各自独立, got %q / %q", reqs[0].Args, reqs[1].Args)
	}
}
