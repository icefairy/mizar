package plugins

import (
	"testing"
)

func TestDescribePluginFunc(t *testing.T) {
	src := `// 记忆插件：长期记忆读写
// 通过 hmem 服务持久化

// mem_write: 写入一条记忆，content 为 JSON 字符串（含 content 字段）
export function tool_mem_write(args: string): string {
  return "ok";
}

/** 搜索记忆
 * 按语义相似度检索
 */
export function tool_mem_search(args: string): string {
  return "[]";
}

export function tool_no_doc(args: string): string {
  return "x";
}
`
	cases := []struct {
		fn, want string
	}{
		{"tool_mem_write", "mem_write: 写入一条记忆，content 为 JSON 字符串（含 content 字段）"},
		{"tool_mem_search", "搜索记忆 按语义相似度检索"},
	}
	for _, c := range cases {
		got := describePluginFunc(src, c.fn)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.fn, got, c.want)
		}
	}
	// 无注释兜底
	got := describePluginFunc(src, "tool_no_doc")
	if got == "" {
		t.Fatal("fallback should not be empty")
	}
	// 空源码兜底
	if describePluginFunc("", "tool_x") == "" {
		t.Fatal("empty src fallback should not be empty")
	}
}

func TestCollectToolsDescription(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "mem.ts", `// mem_write: 写入一条记忆
export function tool_mem_write(args: string): string { return "ok"; }
`)
	m := NewManager(dir, host())
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0]
	if tool.Name != "mem_write" {
		t.Fatalf("expected mem_write, got %s", tool.Name)
	}
	if tool.Description != "mem_write: 写入一条记忆" {
		t.Fatalf("description not extracted: %q", tool.Description)
	}
}

func TestDescribePluginFuncBlankLine(t *testing.T) {
	// 注释与函数之间隔 1 个空行（常见风格）应能收集，且不跨块合并头部说明。
	src := `// 插件头部说明：与工具注释之间有空行，不应被吞并
// 这里是头部信息

// 工具说明一行
// 工具说明二行

export function tool_mem_write(args: string): string {
  return "ok";
}
`
	got := describePluginFunc(src, "tool_mem_write")
	want := "工具说明一行 工具说明二行"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
