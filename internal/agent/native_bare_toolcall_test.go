package agent

import (
	"strings"
	"testing"
)

// 用户报告的复现：模型输出缺 action 字段的原生 bare 工具调用 JSON。
// 例如 {"args":"{\"command\":...}","tool":"bash"}（args 在前、tool 在后、无 action）。
// 此前这些会被当作 reply 原样显示给用户、工具不执行。

func TestParseCallJSON_NativeBareToolCall(t *testing.T) {
	in := `{"args":"{\"command\":\"ls\"}","tool":"bash"}`
	req, err := parseCallJSON(in)
	if err != nil {
		t.Fatalf("parseCallJSON bare native tool call err = %v", err)
	}
	if req.Action != "tool" || req.Tool != "bash" || req.Args != `{"command":"ls"}` {
		t.Fatalf("got action=%q tool=%q args=%q", req.Action, req.Tool, req.Args)
	}
}

func TestExtractActionJSON_NativeBareToolCall(t *testing.T) {
	in := `{"args":"{\"command\":\"ls\"}","tool":"bash"}`
	ext, ok := ExtractActionJSON(in)
	if !ok {
		t.Fatal("ExtractActionJSON should extract native bare tool call")
	}
	if ext != in {
		t.Fatalf("extracted = %q, want %q", ext, in)
	}
}

func TestIsToolCallText_NativeBareToolCall(t *testing.T) {
	if !IsToolCallText(`{"args":"{}","tool":"bash"}`) {
		t.Fatal("bare native tool call should be recognized as tool call text")
	}
	// 缺 args 不能算工具调用
	if IsToolCallText(`{"tool":"bash"}`) {
		t.Fatal("bare without args should NOT be tool call text")
	}
	// 标准协议仍识别
	if !IsToolCallText(`{"action":"tool","tool":"bash","args":"{}"}`) {
		t.Fatal("standard protocol still recognized")
	}
	// 合法 JSON 但既非控制协议也非 tool+args：不算
	if IsToolCallText(`{"a":1,"b":2}`) {
		t.Fatal("unrelated object should not be tool call")
	}
	// 人话不作数（非合法 JSON 整体，或只是提到 tool）——此处验证不会误伤常见人话
	if IsToolCallText(`我先用 bash 检查一下`) {
		t.Fatal("plain prose containing 'bash' should not be tool call")
	}
}

// 分段喂入裸工具 JSON（模拟 SSE 逐 token），且前有前导人话：JSON 不透传，intro 保留。
func TestStreamExtractor_NativeBareToolCall(t *testing.T) {
	// 与用户报告完全一致的裸 JSON 文本
	bare := `{"args":"{\"command\":\"ls\"}","tool":"bash"}`
	// 按字节分段（模拟流式），不强制在转义边界切开
	var mid int
	for i := 1; i < len(bare); i++ {
		if i == len(bare)/3 || i == 2*len(bare)/3 {
			mid = i
			break
		}
	}

	e := &StreamTextExtractor{}
	e.Feed("先")
	e.Feed("看一下\n")
	out := ""
	out += e.Feed(bare[:mid])
	out += e.Feed(bare[mid:])

	if strings.Contains(out, "tool") {
		t.Fatalf("bare tool JSON leaked into display: %q", out)
	}
	if !e.IsTool() {
		t.Fatal("bare tool call should set IsTool=true")
	}
	if got := e.ToolIntro(); got != "先看一下" {
		t.Fatalf("ToolIntro = %q, want 先看一下", got)
	}
}

// 无前导人话、一次性到达裸工具 JSON：全部吞掉，intro 为空。
func TestStreamExtractor_NativeBareToolCall_NoIntro(t *testing.T) {
	e := &StreamTextExtractor{}
	if got := e.Feed(`{"args":"{}","tool":"bash"}`); got != "" {
		t.Fatalf("delta = %q, want empty", got)
	}
	if !e.IsTool() {
		t.Fatal("should be tool")
	}
	if got := e.ToolIntro(); got != "" {
		t.Fatalf("ToolIntro = %q, want empty", got)
	}
}

// 回归：标准 reply JSON 不受 bare 识别影响。
func TestStreamExtractor_NativeBare_NoEffectOnReply(t *testing.T) {
	e := &StreamTextExtractor{}
	var out string
	for _, tk := range []string{`{"action":"reply","text":"已`, `经查`, `清楚了"}`} {
		out += e.Feed(tk)
	}
	if e.IsTool() {
		t.Fatal("reply should not be tool")
	}
	if got := out; got != "已经查清楚了" {
		t.Fatalf("reply streamed text = %q, want 已经查清楚了", got)
	}
}

// 端到端回归（对应真实报告）：模型输出缺 action 的 bare 工具 JSON 作为回复，
// 不把它当 reply 直接显示，而是归一化后真正执行 calc 工具并继续。
func TestRun_NativeBareToolCall_ExecutesAndContinues(t *testing.T) {
	a, _, _ := newTestAgent(
		`{"args":"2+3","tool":"calc"}`,
		`{"action":"reply","text":"计算完成"}`,
	)
	got, err := a.Run("2加3")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "计算完成" {
		t.Fatalf("got %q, want 计算完成（bare 工具调用应被执行而非原样显示）", got)
	}
}

// 复现用户近期报告的形态：缺 action 的 bare bash 工具调用，args 内含一长串
// 多行 docker 命令与转义引号（去掉终端换行后的合法 JSON）。应被识别为工具调用。
func TestProbe_BareBashDockerCommand_Valid(t *testing.T) {
	in := `{"args":"{\"command\":\"docker run -d --name ppu-port --gpus all --device /dev/alixpu_ctl --device /dev/alixpu_ppu0 2>&1 | tail -2; docker ps --filter name=ppu-port --format '{{.Names}} {{.Status}}'\",\"timeout\":60}","tool":"bash"}`
	if !IsToolCallText(in) {
		t.Fatal("bare bash docker tool call (valid JSON) should be recognized as tool call text")
	}
	if _, ok := ExtractActionJSON(in); !ok {
		t.Fatal("ExtractActionJSON should extract bare bash docker tool call")
	}
	req, err := parseCallJSON(in)
	if err != nil {
		t.Fatalf("parseCallJSON: %v", err)
	}
	if req.Action != "tool" || req.Tool != "bash" {
		t.Fatalf("action=%q tool=%q, want tool/bash", req.Action, req.Tool)
	}
}

// 弱模型把 args 内长命令写成字面换行（严格 JSON 非法）：不应被当 reply 显示，
// 至少要识别为工具调用意图（进入纠错/重试路径而非原样展示）。
func TestProbe_BareWithLiteralNewlines_NotDisplayed(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"args":"{\"command\":\"docker run -d`)
	sb.WriteString("\n")
	sb.WriteString(` --device /dev/alixpu_ctl`)
	sb.WriteString("\n")
	sb.WriteString(`\"}","tool":"bash"}`)
	in := sb.String()
	if !IsToolCallText(in) {
		t.Fatal("bare tool call with literal newlines should still be recognized as tool call text (not reply)")
	}
	_ = in
}

// 宽容路径必须能把「args 内含字面换行的畸形 bare 调用」提取并归一化为合法工具调用，
// 使其真正执行而非原样显示。验证 ExtractActionJSON 重建 + parseCallJSON 归一化。
func TestProbe_BareWithLiteralNewlines_ExtractsAndExecutes(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"args":"{\"command\":\"docker run -d`)
	sb.WriteString("\n")
	sb.WriteString(` --device /dev/alixpu_ctl`)
	sb.WriteString("\n")
	sb.WriteString(`\"}","tool":"bash"}`)
	in := sb.String()

	extracted, ok := ExtractActionJSON(in)
	if !ok {
		t.Fatal("ExtractActionJSON should extract literal-newline bare tool call")
	}
	if !isValidJSON(extracted) {
		t.Fatalf("reconstructed JSON must be valid, got: %q", extracted)
	}
	req, err := parseCallJSON(in)
	if err != nil {
		t.Fatalf("parseCallJSON literal-newline: %v", err)
	}
	if req.Action != "tool" || req.Tool != "bash" {
		t.Fatalf("action=%q tool=%q, want tool/bash", req.Action, req.Tool)
	}
	if !strings.Contains(req.Args, "docker run -d") || !strings.Contains(req.Args, "/dev/alixpu_ctl") {
		t.Fatalf("args should preserve command text, got: %q", req.Args)
	}
}

