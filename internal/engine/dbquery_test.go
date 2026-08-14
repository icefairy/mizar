package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBQuerySQLite(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "test.db")

	// 建表 + 插入
	res, err := DBQueryFn("sqlite3", dbFile, "CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(res, "rowsAffected") {
		t.Fatalf("create want rowsAffected, got %s", res)
	}

	if _, err := DBQueryFn("sqlite3", dbFile, `INSERT INTO users (name, age) VALUES ('alice', 30), ('bob', 25)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 查询
	res, err = DBQueryFn("sqlite3", dbFile, "SELECT id, name, age FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res), &rows); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, res)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %s", len(rows), res)
	}
	if rows[0]["name"] != "alice" || rows[1]["name"] != "bob" {
		t.Fatalf("unexpected rows: %s", res)
	}
	// age 是 int64（sqlite 驱动返回）
	if rows[0]["age"] != float64(30) && rows[0]["age"] != int64(30) {
		t.Fatalf("age want 30, got %v (%T)", rows[0]["age"], rows[0]["age"])
	}
}

func TestDBQueryDriverWhitelist(t *testing.T) {
	// 不支持的驱动
	_, err := DBQueryFn("mongodb", "mongodb://x", "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "unsupported driver") {
		t.Fatalf("want unsupported driver error, got %v", err)
	}
	// 空 SQL
	_, err = DBQueryFn("sqlite3", ":memory:", "   ")
	if err == nil || !strings.Contains(err.Error(), "empty SQL") {
		t.Fatalf("want empty SQL error, got %v", err)
	}
	// DSN 超长
	longDSN := strings.Repeat("a", 5000)
	_, err = DBQueryFn("sqlite3", longDSN, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "dsn too long") {
		t.Fatalf("want dsn too long error, got %v", err)
	}
}

func TestDBQueryNormalizeDriver(t *testing.T) {
	cases := map[string]string{
		"sqlite":   "sqlite",
		"sqlite3":  "sqlite",
		"SQLITE":   "sqlite",
		"mysql":    "mysql",
		"mariadb":  "mysql",
		"postgres": "postgres",
		"pg":       "postgres",
	}
	for in, want := range cases {
		got, err := normalizeDriver(in)
		if err != nil || got != want {
			t.Fatalf("normalizeDriver(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestDBQueryExecNonQuery(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "t2.db")
	DBQueryFn("sqlite3", dbFile, "CREATE TABLE t (id INTEGER)")
	res, err := DBQueryFn("sqlite3", dbFile, "INSERT INTO t VALUES (1)")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["rowsAffected"] != float64(1) {
		t.Fatalf("rowsAffected want 1, got %v", m["rowsAffected"])
	}
}

// TestDBQueryExecMultiStatement 验证分号多语句 + rowsAffected 聚合。
func TestDBQueryExecMultiStatement(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "t3.db")
	_, err := DBQueryFn("sqlite3", dbFile,
		"CREATE TABLE t (id INTEGER); INSERT INTO t VALUES (1); INSERT INTO t VALUES (2);")
	if err != nil {
		t.Fatalf("multi exec: %v", err)
	}
	res, err := DBQueryFn("sqlite3", dbFile, "SELECT COUNT(*) AS n FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 || rows[0]["n"] != float64(2) {
		t.Fatalf("want 2 rows, got %v", rows)
	}
}

// TestDBConnPoolReuse 验证连接池复用（:memory: 库跨调用数据可见 = 同连接）。
func TestDBConnPoolReuse(t *testing.T) {
	// :memory: 库若每次重开连接，第二次查询将看不到第一次插入的数据。
	if _, err := DBQueryFn("sqlite3", ":memory:", "CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := DBQueryFn("sqlite3", ":memory:", "INSERT INTO t VALUES (42)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	res, err := DBQueryFn("sqlite3", ":memory:", "SELECT id FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 || rows[0]["id"] != float64(42) {
		t.Fatalf("连接池复用失败: %v", rows)
	}
}

// TestDBExecBatch 验证事务批量：成功聚合 rowsAffected；失败整体回滚。
func TestDBExecBatch(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "t4.db")
	// 成功路径：DDL + 多行 INSERT 一个事务
	res, err := DBExecBatchFn("sqlite3", dbFile, `[
		"CREATE TABLE t (id INTEGER, name TEXT)",
		"INSERT INTO t VALUES (1, 'a'); INSERT INTO t VALUES (2, 'b');",
		"INSERT INTO t VALUES (3, 'c')"
	]`)
	if err != nil {
		t.Fatalf("batch ok: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["rowsAffected"] != float64(3) || m["statements"] != float64(4) {
		t.Fatalf("batch result: %v", m)
	}
	q, _ := DBQueryFn("sqlite3", dbFile, "SELECT COUNT(*) AS n FROM t")
	var rows []map[string]any
	json.Unmarshal([]byte(q), &rows)
	if len(rows) != 1 || rows[0]["n"] != float64(3) {
		t.Fatalf("batch 后行数: %v", rows)
	}

	// 失败路径：第二条非法，第一条（合法插入）必须回滚
	_, err = DBExecBatchFn("sqlite3", dbFile, `[
		"INSERT INTO t VALUES (99, 'x')",
		"INSERT INTO t VALUES (999, 'y'); INSERT INTO bad_table VALUES (1)",
		"INSERT INTO t VALUES (100, 'z')"
	]`)
	if err == nil {
		t.Fatalf("batch 失败应报错")
	}
	q2, _ := DBQueryFn("sqlite3", dbFile, "SELECT COUNT(*) AS n FROM t")
	json.Unmarshal([]byte(q2), &rows)
	if rows[0]["n"] != float64(3) {
		t.Fatalf("回滚失败，行数=%v want 3", rows[0]["n"])
	}
}

// TestHostDBExecBatchVisible 验证 db_exec_batch 在 goja 中可见可调。
func TestHostDBExecBatchVisible(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("batch.ts", `export function tool_batch(): string { return db_exec_batch("sqlite3", "/tmp/mizar_bt_test.db", "[\"DROP TABLE IF EXISTS bt\", \"CREATE TABLE bt (id INTEGER)\", \"INSERT INTO bt VALUES (1)\"]"); }`)
	if err := e.RunScript("batch.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	out, err := e.Call("tool_batch")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := `{"rowsAffected":1,"statements":3}`
	if fmt.Sprintf("%v", out) != want {
		t.Fatalf("want %s, got %v", want, out)
	}
}

// TestDBClose 验证 db_close 丢弃会话残留（临时表/PRAGMA 状态）。
func TestDBClose(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "t5.db")
	// 建临时表（per-connection）：复用连接时跨调用可见
	if _, err := DBQueryFn("sqlite3", dbFile, "CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 关闭连接 → 重建后临时表消失（此例用普通表验证连接重建语义：
	// 普通表持久所以仍存在，但连接对象已变——用 :memory: 验证更彻底）
	if _, err := DBCloseFn("sqlite3", dbFile); err != nil {
		t.Fatalf("close: %v", err)
	}
	// 关闭后再操作：应正常重建连接（不报错）
	if _, err := DBQueryFn("sqlite3", dbFile, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("reuse after close: %v", err)
	}
	// 幂等：重复关闭不报错
	if _, err := DBCloseFn("sqlite3", dbFile); err != nil {
		t.Fatalf("close again: %v", err)
	}
}

// TestDBCloseMemory 验证 :memory: 库 close 后数据确实消失（连接重建）。
func TestDBCloseMemory(t *testing.T) {
	dsn := ":memory:"
	// 用唯一表名避免与其他测试撞表
	DBQueryFn("sqlite3", dsn, "CREATE TABLE memc (id INTEGER)")
	DBQueryFn("sqlite3", dsn, "INSERT INTO memc VALUES (7)")
	res, err := DBQueryFn("sqlite3", dsn, "SELECT COUNT(*) AS n FROM memc")
	if err != nil {
		t.Fatalf("pre-close query: %v", err)
	}
	var rows []map[string]any
	json.Unmarshal([]byte(res), &rows)
	if rows[0]["n"] != float64(1) {
		t.Fatalf("pre-close: %v", rows)
	}
	// close 后 :memory: 库重建，表不存在
	DBCloseFn("sqlite3", dsn)
	if _, err := DBQueryFn("sqlite3", dsn, "SELECT * FROM memc"); err == nil {
		t.Fatalf("close 后 :memory: 表应消失")
	}
}

// TestHostDBCloseVisible 验证 db_close 在 goja 中可见可调。
func TestHostDBCloseVisible(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("close.ts", `export function tool_dc(): string { return db_close("sqlite3", "/tmp/mizar_dc_test.db"); }`)
	if err := e.RunScript("close.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	out, err := e.Call("tool_dc")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := `{"closed":true}`
	if fmt.Sprintf("%v", out) != want {
		t.Fatalf("want %s, got %v", want, out)
	}
}

// TestHostDBQueryVisible 验证 db_query 宿主函数在 goja 中可见可调。
func TestHostDBQueryVisible(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("db.ts", `export function tool_db(): string { return db_query("sqlite3", ":memory:", "SELECT 1 AS one"); }`)
	if err := e.RunScript("db.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := e.Call("tool_db")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := `[{"one":1}]`
	if res != want {
		t.Fatalf("want %q got %q", want, res)
	}
}
