package engine

import (
	"sync"
	"testing"
)

// TestHookOnGoja 验证 hook_on 在 goja 中注册 JS 回调，Fire 触发回调。
func TestHookOnGoja(t *testing.T) {
	var mu sync.Mutex
	var got []string
	host := &HostFuncs{
		HookOn: func(event string, cb func(ctxJSON string) (string, error)) error {
			if event == "CompactionAfter" {
				// 模拟注册后的回调执行
				mu.Lock()
				got = append(got, "registered:"+event)
				mu.Unlock()
				res, err := cb(`{"summary":"x"}`)
				if err != nil {
					t.Fatalf("cb: %v", err)
				}
				mu.Lock()
				got = append(got, "result:"+res)
				mu.Unlock()
			}
			return nil
		},
	}
	e, err := New(host)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("hook.ts", `
export function tool_install(): string {
  hook_on("CompactionAfter", (ctx) => {
    return "cb-ran:" + ctx;
  });
  return "installed";
}`)
	if err := e.RunScript("hook.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := e.Call("tool_install")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res != "installed" {
		t.Fatalf("want installed got %v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 || got[0] != "registered:CompactionAfter" || got[1] != `result:cb-ran:{"summary":"x"}` {
		t.Fatalf("got=%v", got)
	}
}
