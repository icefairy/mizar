package plugins

import (
	"strings"
	"testing"
)

// TestRPCCollect 插件导出 rpc_<name> 应被收集并调用。
func TestRPCCollect(t *testing.T) {
	dir := t.TempDir()
	writeTS(t, dir, "rpc_plugin.ts", `
export function rpc_hello(params) {
  const p = JSON.parse(params || "{}");
  return JSON.stringify({greeting: "hi " + (p.name || "world")});
}
export function rpc_add(params) {
  const p = JSON.parse(params || "{}");
  return String((p.a || 0) + (p.b || 0));
}
`)
	m := NewManager(dir, host())
	loaded, failed := m.LoadAll()
	if len(loaded) != 1 {
		t.Fatalf("loaded=%v failed=%v", loaded, failed)
	}
	rpcs := m.RPCMethods()
	if len(rpcs) != 2 {
		t.Fatalf("rpc methods = %v, want 2", rpcs)
	}
	// 调用 rpc_hello
	out, err := m.CallRPC("hello", `{"name":"mizar"}`)
	if err != nil {
		t.Fatalf("CallRPC: %v", err)
	}
	if !strings.Contains(out, "hi mizar") {
		t.Fatalf("out=%q", out)
	}
	// 调用 rpc_add
	out, err = m.CallRPC("add", `{"a":3,"b":4}`)
	if err != nil {
		t.Fatalf("CallRPC add: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Fatalf("add out=%q", out)
	}
	// 未知方法
	if _, err := m.CallRPC("nope", "{}"); err == nil {
		t.Fatal("want error for unknown method")
	}
}
