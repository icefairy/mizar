package agent

import (
	"strings"
	"testing"
)

// 【复现 2026-10-30 线上 11133】解析层必须拒绝空工具名。
//
// 现场（~/.mizar/sessions/--data-codes-mizar--/，10-28 与 10-29 各一例）：
// 模型偶发输出 {"action":"tool","args":"{\"path\":...}","tool":""}——
// 参数抄上一条 read 的，工具名留空。旧 parseOneCallJSON 只在 Action=="" 时
// 校验 Tool，action=tool 一出现就放行，空名一路写进历史并发出，上游 11133。
//
// 不变式：action=tool 的解析结果，Tool 必须非空；否则报错进纠错提示让模型自纠。
func TestParseRejectsEmptyToolName(t *testing.T) {
	cases := []struct{ name, in string }{
		{"tool 为空串", `{"action":"tool","args":"{\"path\":\"x\"}","tool":""}`},
		{"tool 为 null", `{"action":"tool","args":"{\"path\":\"x\"}","tool":null}`},
		{"缺 tool 字段", `{"action":"tool","args":"{\"path\":\"x\"}"}`},
		{"tool 全空白", `{"action":"tool","args":"{\"path\":\"x\"}","tool":"   "}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reqs, err := parseCallJSONs(c.in)
			if err == nil {
				t.Fatalf("应报错（空工具名不得放行）, got reqs=%+v", reqs)
			}
			if !strings.Contains(err.Error(), "工具名") {
				t.Fatalf("错误信息应指出工具名问题, got %q", err.Error())
			}
			for _, r := range reqs {
				if r.Action == "tool" && strings.TrimSpace(r.Tool) == "" {
					t.Fatalf("错误路径下不应返回空名调用: %+v", r)
				}
			}
		})
	}
}

// 合法调用不受影响（回归保护）。
func TestParseAcceptsValidToolName(t *testing.T) {
	reqs, err := parseCallJSONs(`{"action":"tool","tool":"read","args":"{\"path\":\"x\"}"}`)
	if err != nil {
		t.Fatalf("合法调用不应报错: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Tool != "read" {
		t.Fatalf("解析结果不对: %+v", reqs)
	}
}

// calls[] 里的 id 必须能读到（此前 callSpec 写 "id"、callRequest 读 "callId"，
// 原生 tool_calls 转文本协议后 id 永远丢失，每次都被合成新 call_N）。
func TestParseCallsKeepsNativeID(t *testing.T) {
	in := `{"action":"tool","calls":[{"tool":"bash","args":"{\"command\":\"ls\"}","id":"call_abc123"}],"tool":"bash","args":"{\"command\":\"ls\"}"}`
	reqs, err := parseCallJSONs(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(reqs) != 1 || len(reqs[0].Calls) != 1 {
		t.Fatalf("结构不对: %+v", reqs)
	}
	if got := reqs[0].Calls[0].CallID; got != "call_abc123" {
		t.Fatalf("calls[].id 丢失: CallID=%q（应为 call_abc123）", got)
	}
}

// 兼容两种写法：新 id 与旧 callId 都要能读。
func TestParseCallsAcceptsBothIDKeys(t *testing.T) {
	for _, key := range []string{"id", "callId"} {
		in := `{"action":"tool","calls":[{"tool":"bash","args":"{}","` + key + `":"call_x1"}]}`
		reqs, err := parseCallJSONs(in)
		if err != nil {
			t.Fatalf("%s: parse: %v", key, err)
		}
		if len(reqs) != 1 || len(reqs[0].Calls) != 1 {
			t.Fatalf("%s: 结构不对: %+v", key, reqs)
		}
		if got := reqs[0].Calls[0].CallID; got != "call_x1" {
			t.Fatalf("%s: CallID=%q（应为 call_x1）", key, got)
		}
	}
}

// 边界：空工具名但参数含 "command" → 可推断为 bash，解析层必须放行
// （留给循环层兜底），不能一刀切拒绝——否则会破坏既有弱模型救回能力。
func TestParseAllowsInferableEmptyToolName(t *testing.T) {
	in := `{"action":"tool","args":"{\"command\":\"echo hi\"}"}`
	reqs, err := parseCallJSONs(in)
	if err != nil {
		t.Fatalf("含 command 的空名调用应放行（循环层会推断 bash）: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Tool != "" {
		t.Fatalf("解析层应保持 Tool 为空，交由循环层推断: %+v", reqs)
	}
	if !inferableToolFromArgs(reqs[0].Args) {
		t.Fatal("inferableToolFromArgs 应判定为可推断")
	}
}
