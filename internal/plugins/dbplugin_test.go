package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/engine"
)

// hostWithDB 返回带数据库宿主函数的 HostFuncs（供 db.ts 插件测试）。
func hostWithDB(t *testing.T) *engine.HostFuncs {
	t.Helper()
	return &engine.HostFuncs{
		Log:         func(string) {},
		DBQuery:     engine.DBQueryFn,
		DBExecBatch: engine.DBExecBatchFn,
		DBClose:     engine.DBCloseFn,
		FSRead: func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return string(b), err
		},
		FSWrite: func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
	}
}

// 内置 db.ts 插件：连接（sqlite 内存）→ 建表 → 增删改查。
func TestDBPluginCRUD(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "assets", "extensions", "db.ts"))
	if err != nil {
		t.Skipf("db.ts 不存在: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.ts"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir, hostWithDB(t))
	if _, failed := m.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	// 应注册这些工具
	for _, name := range []string{"db_conn", "db_tables", "db_query", "db_exec", "db_insert", "db_update", "db_delete", "db_count"} {
		if _, ok := m.Get(name); !ok {
			t.Fatalf("工具 %s 未注册", name)
		}
	}
	dsn := filepath.Join(t.TempDir(), "test.db")
	inline := `"driver":"sqlite3","dsn":"` + dsn + `"`

	// 建表
	if _, err := m.Call("db_exec", `{`+inline+`,"sql":"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)"}`); err != nil {
		t.Fatalf("db_exec create: %v", err)
	}
	// 插入
	out, err := m.Call("db_insert", `{`+inline+`,"table":"users","values":{"name":"张三","age":30}}`)
	if err != nil {
		t.Fatalf("db_insert: %v", err)
	}
	if !strings.Contains(out, "rowsAffected") {
		t.Fatalf("db_insert 返回异常: %s", out)
	}
	// 查询
	out, _ = m.Call("db_query", `{`+inline+`,"sql":"SELECT name,age FROM users"}`)
	if !strings.Contains(out, "张三") || !strings.Contains(out, "30") {
		t.Fatalf("db_query 未返回插入的行: %s", out)
	}
	// 计数
	out, _ = m.Call("db_count", `{`+inline+`,"table":"users"}`)
	if !strings.Contains(out, `"cnt":1`) && !strings.Contains(out, `"cnt": 1`) {
		t.Fatalf("db_count 应为 1: %s", out)
	}
	// 更新
	if _, err := m.Call("db_update", `{`+inline+`,"table":"users","values":{"age":31},"where":{"name":"张三"}}`); err != nil {
		t.Fatalf("db_update: %v", err)
	}
	out, _ = m.Call("db_query", `{`+inline+`,"sql":"SELECT age FROM users"}`)
	if !strings.Contains(out, "31") {
		t.Fatalf("更新未生效: %s", out)
	}
	// 删除（where 必填，空 where 应报错）
	if _, err := m.Call("db_delete", `{`+inline+`,"table":"users","where":{}}`); err == nil {
		t.Fatal("空 where 的删除应被拒绝")
	}
	if _, err := m.Call("db_delete", `{`+inline+`,"table":"users","where":{"name":"张三"}}`); err != nil {
		t.Fatalf("db_delete: %v", err)
	}
	out, _ = m.Call("db_count", `{`+inline+`,"table":"users"}`)
	if !strings.Contains(out, `"cnt":0`) && !strings.Contains(out, `"cnt": 0`) {
		t.Fatalf("删除后计数应为 0: %s", out)
	}
}
