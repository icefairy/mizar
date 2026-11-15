package agent

import (
	"strings"
	"testing"
)

func TestMemoryPromptInjected(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	a.MemoryPrompt = "\n## 结构化记忆（MEMORY.md）\n### user\n- 偏好结论先行\n"
	p := a.SystemPrompt()
	if !strings.Contains(p, "结构化记忆") || !strings.Contains(p, "偏好结论先行") {
		t.Errorf("memory 注入段未出现在系统提示:\n%s", p)
	}
}

func TestSetMemoryPromptInvalidatesCache(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	a.MemoryPrompt = "\n## 结构化记忆（MEMORY.md）\n### user\n- 旧事实\n"
	p1 := a.SystemPrompt()
	if !strings.Contains(p1, "旧事实") {
		t.Fatal("初始事实未注入")
	}
	a.SetMemoryPrompt("\n## 结构化记忆（MEMORY.md）\n### user\n- 新事实\n")
	p2 := a.SystemPrompt()
	if strings.Contains(p2, "旧事实") {
		t.Error("缓存未失效，仍含旧事实")
	}
	if !strings.Contains(p2, "新事实") {
		t.Error("新事实未注入")
	}
}

func TestMemoryPromptCloned(t *testing.T) {
	a := New(&scriptLLM{}, testManager(t))
	a.MemoryPrompt = "MEM-XYZ"
	b := a.Clone()
	if b.MemoryPrompt != "MEM-XYZ" {
		t.Errorf("Clone 应复制 MemoryPrompt，got %q", b.MemoryPrompt)
	}
}
