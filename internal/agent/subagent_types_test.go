package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mizar/internal/plugins"
)

// ============================================================================
// GAP-S1 第二层：用户自定义子代理类型（~/.mizar/agents/*.md）
// ============================================================================

func TestParseAgentTypeMD(t *testing.T) {
	src := `---
name: tester
description: 跑测试并归因失败用例
tools: [bash, read, grep]
model: glm-4-flash
---
你是测试型子代理。只跑测试与收集失败证据，不修改被测代码。`
	at, err := parseAgentTypeMD(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if at.Name != "tester" || at.Description != "跑测试并归因失败用例" || at.Model != "glm-4-flash" {
		t.Fatalf("frontmatter 解析错误: %+v", at)
	}
	if len(at.Tools) != 3 || at.Tools[0] != "bash" || at.Tools[2] != "grep" {
		t.Fatalf("tools 解析错误: %v", at.Tools)
	}
	if !strings.Contains(at.System, "测试型子代理") || strings.Contains(at.System, "name:") {
		t.Fatalf("正文提取错误: %q", at.System)
	}

	// 逗号写法 + 引号
	at2, err := parseAgentTypeMD("---\nname: a\ntools: \"bash, read\"\n---\n正文")
	if err != nil || len(at2.Tools) != 2 || at2.Tools[1] != "read" {
		t.Fatalf("逗号 tools 解析错误: %+v err=%v", at2, err)
	}
	// 缺 name
	if _, err := parseAgentTypeMD("---\ndescription: x\n---\nbody"); err == nil {
		t.Fatal("缺 name 应报错")
	}
	// 缺 frontmatter
	if _, err := parseAgentTypeMD("just text"); err == nil {
		t.Fatal("缺 frontmatter 应报错")
	}
}

func TestLoadAgentTypeFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tester.md"), []byte("---\nname: tester\ntools: [bash]\n---\n跑测试"), 0o644)
	os.WriteFile(filepath.Join(dir, "explorer.md"), []byte("---\nname: explorer\ndescription: 只读探查\n---\n只读"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)

	types, errs := LoadAgentTypeFiles(dir)
	if len(errs) != 1 { // broken.md
		t.Fatalf("want 1 parse error, got %v", errs)
	}
	if len(types) != 2 || types[0].Name != "explorer" || types[1].Name != "tester" {
		t.Fatalf("类型加载错误: %+v (errs=%v)", types, errs)
	}
	// 目录不存在 = 无类型、无错误
	if types, errs := LoadAgentTypeFiles(filepath.Join(dir, "missing")); types != nil || errs != nil {
		t.Fatalf("目录不存在应返回 nil: %+v %+v", types, errs)
	}
}

// 自定义类型工具白名单：子 agent 只拿到白名单内的共享工具。
func TestSubagent_CustomTypeToolWhitelist(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	pm := plugins.NewManager("", nil)
	pm.RegisterBuiltin(plugins.Tool{Name: "ping", Description: "t", Run: func(a string) (string, error) { return "pong", nil }})
	pm.RegisterBuiltin(plugins.Tool{Name: "evil_write", Description: "t", Run: func(a string) (string, error) { return "", nil }})
	pm.RegisterBuiltin(plugins.Tool{Name: "delegate_task", Run: func(string) (string, error) { return "", nil }})
	sm := NewSubagentManager(SubagentConfig{ParentLLM: llm, ParentSys: "sys", Plugins: pm})
	sm.SetAgentTypes([]AgentType{{Name: "prober", Description: "只许 ping", Tools: []string{"ping"}}})

	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "探查", Role: "prober"})
	waitState(sm, h.ID, 2*time.Second)
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.gotTool) == 0 {
		t.Fatal("子 agent 没发起 LLM 调用")
	}
	for _, tl := range llm.gotTool {
		for _, tool := range tl {
			if tool.Name == "evil_write" {
				t.Fatal("白名单外工具漏进了子 agent 工具清单")
			}
		}
	}
	// delegate_task 是嵌套委派专用通道（深度闸门另行控制），不受白名单约束
	found := false
	for _, tool := range llm.gotTool[0] {
		if tool.Name == "ping" {
			found = true
		}
	}
	if !found {
		t.Fatal("白名单内工具 ping 缺失")
	}
}

// 自定义类型默认模型：req.Model 未指定时用类型 model，派生路径与 per-task 相同。
func TestSubagent_CustomTypeDefaultModel(t *testing.T) {
	llm := &deriverLLM{modelName: "parent"}
	sm := newTestManager(llm, nil)
	sm.SetAgentTypes([]AgentType{{Name: "cheap", Model: "cheap-model"}})

	h, _ := sm.Launch(SubagentLaunchRequest{Goal: "省着点跑", Role: "cheap"})
	waitState(sm, h.ID, 2*time.Second)
	llm.mu.Lock()
	derived := append([]string{}, llm.derived...)
	llm.mu.Unlock()
	if len(derived) != 1 || derived[0] != "cheap-model" {
		t.Fatalf("应按类型默认模型派生，实际 derived=%v", derived)
	}
	if got, _ := sm.Get(h.ID); got.Model != "cheap-model" {
		t.Fatalf("句柄应回显类型模型，实际 %q", got.Model)
	}

	// req.Model 显式指定时优先于类型默认
	llm2 := &deriverLLM{modelName: "parent"}
	sm2 := newTestManager(llm2, nil)
	sm2.SetAgentTypes([]AgentType{{Name: "cheap", Model: "cheap-model"}})
	h2, _ := sm2.Launch(SubagentLaunchRequest{Goal: "指定优先", Role: "cheap", Model: "explicit"})
	waitState(sm2, h2.ID, 2*time.Second)
	llm2.mu.Lock()
	derived2 := append([]string{}, llm2.derived...)
	llm2.mu.Unlock()
	if len(derived2) != 1 || derived2[0] != "explicit" {
		t.Fatalf("req.Model 应优先于类型默认，实际 derived=%v", derived2)
	}
}

// 未知 role 仍归 leaf；已知内置 role 不受影响；类型 System 注入子任务指令。
func TestSubagent_CustomTypeRoleResolution(t *testing.T) {
	llm := &scriptedLLM{script: []string{`{"action":"reply","text":"ok"}`}}
	sm := newTestManager(llm, nil)
	sm.SetAgentTypes([]AgentType{{Name: "tester", System: "你是测试型子代理，禁止修改代码。"}})

	// 未知 role → leaf
	h1, _ := sm.Launch(SubagentLaunchRequest{Goal: "x", Role: "nonexistent-type"})
	waitState(sm, h1.ID, 2*time.Second)
	if got, _ := sm.Get(h1.ID); got.Role != "leaf" {
		t.Fatalf("未知 role 应归 leaf，实际 %q", got.Role)
	}
	// 自定义类型 role 保留 + System 注入任务指令
	h2, _ := sm.Launch(SubagentLaunchRequest{Goal: "跑单测", Role: "tester"})
	waitState(sm, h2.ID, 2*time.Second)
	if got, _ := sm.Get(h2.ID); got.Role != "tester" {
		t.Fatalf("自定义类型 role 应保留，实际 %q", got.Role)
	}
	llm.mu.Lock()
	var taskText string
	for _, round := range llm.gotMsgs {
		for _, m := range round {
			if strings.Contains(m.Content, "跑单测") && strings.Contains(m.Content, "【子任务背景/上下文】") {
				taskText = m.Content
			}
		}
	}
	llm.mu.Unlock()
	if !strings.Contains(taskText, "测试型子代理，禁止修改代码") {
		t.Fatalf("类型 System 未注入子任务指令: %q", taskText)
	}
	// 内置 role 不受影响
	h3, _ := sm.Launch(SubagentLaunchRequest{Goal: "y", Role: "explore"})
	waitState(sm, h3.ID, 2*time.Second)
	if got, _ := sm.Get(h3.ID); got.Role != "explore" {
		t.Fatalf("内置 role 应保留，实际 %q", got.Role)
	}
}

// delegate_task 描述列出可用自定义类型。
func TestDelegateTool_DescriptionListsTypes(t *testing.T) {
	sm := NewSubagentManager(SubagentConfig{})
	base := DelegateTool(sm).Description
	if strings.Contains(base, "自定义子代理类型") {
		t.Fatal("无类型时描述不应包含自定义类型清单")
	}
	sm.SetAgentTypes([]AgentType{{Name: "tester", Description: "跑测试"}})
	desc := DelegateTool(sm).Description
	if !strings.Contains(desc, "tester") || !strings.Contains(desc, "跑测试") {
		t.Fatalf("描述未列出类型: %q", desc)
	}
}
