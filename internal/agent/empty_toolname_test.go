package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// 【复现 2026-10-30 线上 11133】空工具名不得进入历史。
//
// 模型有时丢 tool 字段（args 里也没有 bash 的 "command": 可猜）。
// 旧行为：继续往下执行 → ToolCallRef{Name:""} 写进历史 → 编码后发出
// tool_calls[].function.name == ""，上游一律拒绝（workbuddy 11133）。
//
// 新行为：不执行、不写 tool_calls，注入纠正提示让模型重发。
func TestRunEmptyToolNameDoesNotWriteToolCalls(t *testing.T) {
	var sawEmptyRef bool
	var correctionSent bool
	dir, _ := os.MkdirTemp("", "mizar-emptyname-*")
	os.WriteFile(filepath.Join(dir, "t.ts"), []byte(`export function tool_echo(args: string): string { return "ECHO:" + args; }`), 0o644)
	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatal(failed)
	}
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		for _, msg := range msgs {
			for _, tc := range msg.ToolCalls {
				if tc.Name == "" {
					sawEmptyRef = true
				}
			}
			if msg.Role == RoleUser && strings.Contains(msg.Content, "缺少工具名") {
				correctionSent = true
			}
		}
		if !correctionSent {
			// 空工具名 + args 不含 command → 无法猜出工具
			return `{"action":"tool","tool":"","args":"whatever"}` + "\n", nil
		}
		return `{"action":"reply","text":"done"}`, nil
	}}
	a := New(llm, m)
	got, err := a.Run("x")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "done" {
		t.Fatalf("want done got %q", got)
	}
	if !correctionSent {
		t.Fatal("未注入「缺少工具名」纠正提示，模型无法自纠")
	}
	if sawEmptyRef {
		t.Fatal("历史中出现了空工具名的 ToolCallRef（会被编码成 tool_calls[].function.name==\"\"，上游 11133）")
	}
}
