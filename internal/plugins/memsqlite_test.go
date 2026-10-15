package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
)

// hostWithMemDB 带数据库宿主函数，供 memory.ts（自托管 SQLite）测试。
func hostWithMemDB(t *testing.T) *engine.HostFuncs {
	t.Helper()
	return &engine.HostFuncs{
		Log:     func(string) {},
		Env:     func(k string) string { return os.Getenv(k) },
		DBQuery: engine.DBQueryFn,
		FSRead: func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return string(b), err
		},
		FSWrite: func(p, c string) error {
			os.MkdirAll(filepath.Dir(p), 0o755)
			return os.WriteFile(p, []byte(c), 0o644)
		},
	}
}

// 自托管 SQLite 记忆插件：写入 → 检索排序（中文分词）→ 列表/删除。
func TestMemoryPluginSQLite(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "assets", "extensions", "memory.ts"))
	if err != nil {
		t.Skipf("memory.ts 不存在: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.ts"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	// 用隔离 HOME，避免污染真实 ~/.mizar
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := NewManager(dir, hostWithMemDB(t))
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}

	// 写三条记忆
	for _, c := range []string{
		"用户偏好结论先行，不要寒暄",
		"数据库连接使用 postgres，端口 5432",
		"前端项目用 Vue3 和 Tailwind",
	} {
		if _, err := m.Call("mem_write", `{"content":`+jsonStr(c)+`}`); err != nil {
			t.Fatalf("mem_write: %v", err)
		}
	}

	// 检索「数据库」——应命中 postgres 那条，且排第一
	out, err := m.Call("mem_search", `{"query":"数据库连接","limit":3}`)
	if err != nil {
		t.Fatalf("mem_search: %v", err)
	}
	if !strings.Contains(out, "postgres") {
		t.Fatalf("检索未命中相关记忆: %s", out)
	}
	// 第一条结果的正文（结果行 "1. [...] #N ..." 之后紧跟的缩进行）应含 postgres
	idx := strings.Index(out, "1. [")
	if idx < 0 {
		t.Fatalf("无结果行: %s", out)
	}
	tail := out[idx:]
	// 只看第一条结果的块（到下一个空行/下一条结果为止）
	if end := strings.Index(tail, "\n\n"); end >= 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "postgres") {
		t.Fatalf("最相关的记忆未排首位: %s", out)
	}

	// 检索「前端」——应命中 Vue3
	out, _ = m.Call("mem_search", `{"query":"前端框架"}`)
	if !strings.Contains(out, "Vue3") {
		t.Fatalf("检索前端未命中 Vue3: %s", out)
	}

	// 默认 limit=3
	out, _ = m.Call("mem_list", `{}`)
	if !strings.Contains(out, "3 条记忆") {
		t.Fatalf("mem_list 应显示 3 条: %s", out)
	}

	// 统计
	out, _ = m.Call("mem_stats", `{}`)
	if !strings.Contains(out, "总数: 3") {
		t.Fatalf("mem_stats 总数应为 3: %s", out)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
