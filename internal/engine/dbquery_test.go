package engine

import (
	"encoding/json"
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
