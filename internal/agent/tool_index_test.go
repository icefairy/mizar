package agent

import (
	"strings"
	"testing"

	"mizar/internal/plugins"
)

// fakeTools 构造一个含核心工具 + 若干延迟工具的注册表。
func toolIndexTestAgent(t *testing.T, mode string) *Agent {
	t.Helper()
	a := New(&scriptLLM{}, testManager(t))
	// 注册几个非核心工具（模拟插件/MCP 长尾）
	defs := []plugins.Tool{
		{Name: "db_query", Description: "执行查询 SQL（SELECT）", Run: func(string) (string, error) { return "", nil }},
		{Name: "kb_search_documents", Description: "语义搜索知识库", Run: func(string) (string, error) { return "", nil }},
		{Name: "anysearch", Description: "联网搜索网页", Run: func(string) (string, error) { return "", nil }},
	}
	for _, d := range defs {
		a.Plugins.RegisterBuiltin(d)
	}
	// 注册 tool_search 自身
	a.Plugins.RegisterBuiltin(ToolSearchTool(a))
	a.SetToolInjectMode(mode)
	return a
}

func TestFullModeInjectsAll(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectFull)
	names := toolNames(a.ActiveTools())
	for _, want := range []string{"db_query", "kb_search_documents", "anysearch"} {
		if !names[want] {
			t.Errorf("full 模式应注入 %s，实际工具: %v", want, names)
		}
	}
	if got := a.DeferredTools(); len(got) != 0 {
		t.Errorf("full 模式无延迟工具，got %d", len(got))
	}
	if a.ToolInjectMode() != ToolInjectFull {
		t.Errorf("mode=%s", a.ToolInjectMode())
	}
}

func TestIndexModeDefers(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	active := toolNames(a.ActiveTools())
	// 核心工具在（此测试注册表只有 tool_search 属核心集）
	if !active["tool_search"] {
		t.Errorf("核心工具 tool_search 应保留: %v", active)
	}
	// 非核心的核心集工具（若注册表里有）也必须保留
	for _, core := range []string{"bash", "read", "respond"} {
		if _, exists := a.Plugins.Get(core); exists && !active[core] {
			t.Errorf("核心工具 %s 应保留", core)
		}
	}
	// 长尾工具被推迟
	for _, n := range []string{"db_query", "anysearch"} {
		if active[n] {
			t.Errorf("%s 应被推迟，实际仍注入", n)
		}
	}
	if len(a.DeferredTools()) == 0 {
		t.Error("应存在延迟工具")
	}
	// 系统提示应含索引段
	p := a.SystemPrompt()
	if !strings.Contains(p, "按需加载的工具") || !strings.Contains(p, "db_query") || !strings.Contains(p, "tool_search") {
		t.Errorf("索引段缺失:\n%s", p)
	}
}

func TestToolSearchLoadsTool(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	if a.IsToolLoadedForSession("db_query") {
		t.Fatal("初始不应加载")
	}
	ts := ToolSearchTool(a)
	out, err := ts.Run(`{"names":["db_query"]}`)
	if err != nil {
		t.Fatalf("tool_search: %v", err)
	}
	if !strings.Contains(out, "db_query") || !strings.Contains(out, "已加载") {
		t.Errorf("tool_search 输出: %s", out)
	}
	if !a.IsToolLoadedForSession("db_query") {
		t.Error("加载后应标记为已加载")
	}
	// 加载后进入 ActiveTools
	if !toolNames(a.ActiveTools())["db_query"] {
		t.Error("加载后应注入 db_query")
	}
}

func TestToolSearchByKeyword(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	ts := ToolSearchTool(a)
	out, err := ts.Run(`{"query":"搜索"}`)
	if err != nil {
		t.Fatalf("keyword search: %v", err)
	}
	// kb_search_documents 与 anysearch 描述都含“搜索”
	if !strings.Contains(out, "kb_search_documents") && !strings.Contains(out, "anysearch") {
		t.Errorf("关键词搜索应命中: %s", out)
	}
}

func TestToolSearchMissingName(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	ts := ToolSearchTool(a)
	out, err := ts.Run(`{"names":["nope"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nope") {
		t.Errorf("应报告未找到: %s", out)
	}
}

func TestToolSearchRequiresArgs(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	ts := ToolSearchTool(a)
	if _, err := ts.Run(`{}`); err == nil {
		t.Error("缺 names/query 应报错")
	}
}

func TestSetToolInjectModeNormalizes(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectFull)
	a.SetToolInjectMode("bogus")
	if a.ToolInjectMode() != ToolInjectFull {
		t.Errorf("非法模式应回落 full，got %s", a.ToolInjectMode())
	}
	a.SetToolInjectMode("INDEX")
	if a.ToolInjectMode() != ToolInjectIndex {
		t.Errorf("大小写应归一，got %s", a.ToolInjectMode())
	}
}

func TestCloneInheritsModeNotLoaded(t *testing.T) {
	a := toolIndexTestAgent(t, ToolInjectIndex)
	a.LoadToolForSession("db_query")
	b := a.Clone()
	if b.ToolInjectMode() != ToolInjectIndex {
		t.Errorf("Clone 应继承注入模式，got %s", b.ToolInjectMode())
	}
	if b.IsToolLoadedForSession("db_query") {
		t.Error("Clone 的已加载集合应独立（不继承父会话加载状态）")
	}
}

func TestClampRunes(t *testing.T) {
	if got := clampRunes("中文字符", 2); got != "中文…" {
		t.Errorf("clampRunes=%q", got)
	}
	if got := clampRunes("ab", 5); got != "ab" {
		t.Errorf("短串不应截断: %q", got)
	}
}

func toolNames(tools []plugins.Tool) map[string]bool {
	m := map[string]bool{}
	for _, t := range tools {
		m[t.Name] = true
	}
	return m
}
