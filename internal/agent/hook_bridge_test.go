package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
	"mizar/internal/plugins"
)

// TestHookBridgeEndToEnd 验证完整链路：
// scriptLLM 返回 reply → RunEnd hook 触发 → 插件 hook_on 回调执行 →
// db_query 写入 sqlite → 可查询。
func TestHookBridgeEndToEnd(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mizar-hook-e2e-*")
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "mem.db")
	plugin := `
const DB = ` + "`" + dbPath + "`" + `;
hook_on("RunEnd", (ctx: string): string => {
  const c = JSON.parse(ctx);
  db_query("sqlite3", DB, "CREATE TABLE IF NOT EXISTS m (id INTEGER PRIMARY KEY AUTOINCREMENT, task TEXT)");
  db_query("sqlite3", DB, "INSERT INTO m (task) VALUES ('" + (c.Task||"").replace(/'/g,"''") + "')");
  return "saved";
});
export function tool_mem(): string {
  return db_query("sqlite3", DB, "SELECT id, task FROM m");
}
`
	os.WriteFile(filepath.Join(dir, "mem.ts"), []byte(plugin), 0o644)

	m := plugins.NewManager(dir, &engine.HostFuncs{Log: func(string) {}, DBQuery: engine.DBQueryFn})
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("plugin load failed: %v", failed)
	}

	// 桥接 Hooks → Manager（模拟 main.go 的接线）
	llm := &scriptLLM{fn: func(msgs []Message) (string, error) {
		return `{"action":"reply","text":"完成"}` , nil
	}}
	a := New(llm, m)
	a.Hooks = NewHooks()
	lastFired := 0
	bridgeHook := func(evt string) HookFunc {
		return func(ctx *HookContext) error {
			n, errs := m.FireHook(evt, plugins.MarshalCtx(ctx))
			lastFired = n
			for _, e := range errs {
				t.Logf("hook %s err: %v", evt, e)
			}
			return nil
		}
	}
	a.Hooks.OnRunStart(bridgeHook("RunStart")).OnRunEnd(bridgeHook("RunEnd"))

	got, err := a.Run("记录一下这个任务")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "完成" {
		t.Fatalf("want 完成 got %q", got)
	}
	t.Logf("RunEnd hook 注册数: %d, Fire 成功数: %d", m.HookCount("RunEnd"), lastFired)

	// 验证 RunEnd hook 写入了 DB
	res, err := m.Call("mem", "")
	if err != nil {
		t.Fatalf("call mem: %v", err)
	}
	if !strings.Contains(res, "记录一下这个任务") {
		t.Fatalf("DB 里应有任务记录, got: %s", res)
	}
}
