package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	// 内置驱动白名单（纯 Go，无 CGO，保持单二进制）
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// ---------- 连接池（方案一：按 DSN 复用连接） ----------

// poolEntry 连接池条目。
type poolEntry struct {
	db       *sql.DB
	lastUsed time.Time
	memory   bool // :memory: 库不参与空闲清理（数据有生命周期价值）
}

// dbConnPool 按 driver|dsn 复用 *sql.DB，避免每次调用重开连接。
// 文件库空闲 idleTimeout 后 Close 回收；:memory: 库进程内永久保留。
type dbConnPool struct {
	mu          sync.Mutex
	conns       map[string]*poolEntry
	idleTimeout time.Duration
}

var dbPool = &dbConnPool{
	conns:       make(map[string]*poolEntry),
	idleTimeout: 5 * time.Minute,
}

// getDB 返回 driver+dsn 对应的 *sql.DB（无则创建），并更新最近使用时间。
func (p *dbConnPool) getDB(driver, dsn string) (*sql.DB, bool, error) {
	key := driver + "|" + dsn
	memory := strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory")

	p.mu.Lock()
	if e, ok := p.conns[key]; ok {
		e.lastUsed = time.Now()
		p.mu.Unlock()
		return e.db, false, nil
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		p.mu.Unlock()
		return nil, false, fmt.Errorf("db_query open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	p.conns[key] = &poolEntry{db: db, lastUsed: time.Now(), memory: memory}
	p.mu.Unlock()
	return db, true, nil
}

// closeDB 关闭指定 driver+dsn 的连接（不存在则无操作）。
// 插件可用它丢弃连接上的会话残留（PRAGMA/SET/临时表等），下次调用重建。
func (p *dbConnPool) closeDB(driver, dsn string) {
	key := driver + "|" + dsn
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.conns[key]; ok {
		e.db.Close()
		delete(p.conns, key)
	}
}

// closeAll 关闭全部连接（进程退出/测试清理用）。
func (p *dbConnPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.conns {
		e.db.Close()
		delete(p.conns, k)
	}
}

// closeIdle 回收空闲超时的文件库连接（:memory: 跳过）。
func (p *dbConnPool) closeIdle(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.conns {
		if e.memory {
			continue
		}
		if now.Sub(e.lastUsed) > p.idleTimeout {
			e.db.Close()
			delete(p.conns, k)
		}
	}
}

// startIdleReaper 启动后台空闲回收（60s 一次）。进程生命周期内运行。
func (p *dbConnPool) startIdleReaper() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			p.closeIdle(time.Now())
		}
	}()
}

var dbReaperOnce sync.Once

// StartDBReaper 启动连接池空闲回收（幂等，可多次调用）。
func StartDBReaper() {
	dbReaperOnce.Do(func() {
		dbPool.startIdleReaper()
	})
}

// DBCloseFn 实现 db_close 宿主函数：关闭 driver+dsn 的连接，丢弃会话残留。
// 不存在则无操作（幂等）。返回 JSON {"closed": true|false}。
func DBCloseFn(driver, dsn string) (string, error) {
	d, err := normalizeDriver(driver)
	if err != nil {
		return "", err
	}
	dbPool.closeDB(d, dsn)
	return `{"closed":true}`, nil
}

// ---------- db_query 宿主函数 ----------

// DBQueryFn 实现 db_query 宿主函数。
// 插件调用: db_query(driver, dsn, sql) -> JSON 字符串
// driver 白名单: sqlite3 / mysql / postgres
// 返回: 查询结果 JSON 数组（行对象），非查询语句返回 {"rowsAffected": N}
func DBQueryFn(driver, dsn, query string) (string, error) {
	driver, err := normalizeDriver(driver)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("db_query: empty SQL")
	}
	if len(dsn) > 4096 {
		return "", fmt.Errorf("db_query: dsn too long")
	}

	db, _, err := dbPool.getDB(driver, dsn)
	if err != nil {
		return "", err
	}

	// 连接 + 查询总超时，防止插件卡死宿主
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lower := strings.ToLower(strings.TrimSpace(query))
	isQuery := strings.HasPrefix(lower, "select") ||
		strings.HasPrefix(lower, "show") ||
		strings.HasPrefix(lower, "pragma") ||
		strings.HasPrefix(lower, "explain") ||
		strings.HasPrefix(lower, "with")

	if isQuery {
		return queryRows(ctx, db, query)
	}
	return execStmt(ctx, db, query)
}

// DBExecBatchFn 实现 db_exec_batch 宿主函数（方案二：事务批量）。
// 插件调用: db_exec_batch(driver, dsn, sqlsJSON) -> {"rowsAffected": N, "statements": M}
// sqlsJSON 为 SQL 字符串数组；全部语句在**一个事务**内执行，任一条失败整体回滚。
func DBExecBatchFn(driver, dsn, sqlsJSON string) (string, error) {
	driver, err := normalizeDriver(driver)
	if err != nil {
		return "", err
	}
	if len(dsn) > 4096 {
		return "", fmt.Errorf("db_exec_batch: dsn too long")
	}
	var sqls []string
	if err := json.Unmarshal([]byte(sqlsJSON), &sqls); err != nil {
		return "", fmt.Errorf("db_exec_batch: sqls 需为字符串数组: %w", err)
	}
	if len(sqls) == 0 {
		return "", fmt.Errorf("db_exec_batch: empty sqls")
	}

	// 展开分号多语句，保持语句顺序
	var stmts []string
	for _, s := range sqls {
		stmts = append(stmts, splitStatements(s)...)
	}

	db, _, err := dbPool.getDB(driver, dsn)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("db_exec_batch begin: %w", err)
	}
	total := int64(0)
	for _, s := range stmts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		res, err := tx.ExecContext(ctx, s)
		if err != nil {
			tx.Rollback()
			return "", fmt.Errorf("db_exec_batch exec (rollback): %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("db_exec_batch commit: %w", err)
	}
	return fmt.Sprintf(`{"rowsAffected":%d,"statements":%d}`, total, len(stmts)), nil
}

func normalizeDriver(driver string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		return "sqlite", nil
	case "mysql", "mariadb":
		return "mysql", nil
	case "postgres", "postgresql", "pg", "pq":
		return "postgres", nil
	default:
		return "", fmt.Errorf("db_query: unsupported driver %q (allowed: sqlite3, mysql, postgres)", driver)
	}
}

func queryRows(ctx context.Context, db *sql.DB, query string) (string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("db_query query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("db_query columns: %w", err)
	}
	colTypes, _ := rows.ColumnTypes()

	type rowMap = map[string]any
	var out []rowMap
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", fmt.Errorf("db_query scan: %w", err)
		}
		row := make(rowMap, len(cols))
		for i, c := range cols {
			row[c] = normalizeValue(vals[i], colTypes[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("db_query rows: %w", err)
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("db_query marshal: %w", err)
	}
	return string(b), nil
}

func execStmt(ctx context.Context, db *sql.DB, query string) (string, error) {
	// 支持分号分隔的多语句（DDL + DML 混合，常用场景）。
	// Query 模式（SELECT 等）不支持多语句——返回行结构无法合并。
	// rowsAffected 聚合各语句的受影响行数（DDL 为 0，可累加）。
	stmts := splitStatements(query)
	total := int64(0)
	for _, s := range stmts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		res, err := db.ExecContext(ctx, s)
		if err != nil {
			return "", fmt.Errorf("db_query exec: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	return fmt.Sprintf(`{"rowsAffected":%d}`, total), nil
}

// splitStatements 按分号拆分 SQL，但忽略引号内的分号。
func splitStatements(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	quoteChar := rune(0)
	for _, r := range s {
		if inQuote {
			cur.WriteRune(r)
			if r == quoteChar {
				inQuote = false
			}
			continue
		}
		if r == '\'' || r == '"' {
			inQuote = true
			quoteChar = r
			cur.WriteRune(r)
			continue
		}
		if r == ';' {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	rest := cur.String()
	if strings.TrimSpace(rest) != "" {
		parts = append(parts, rest)
	}
	return parts
}

// normalizeValue 把数据库值转成可 JSON 序列化的 Go 类型。
// []byte 按字符串输出（MySQL 的 BLOB/TEXT 常见），time.Time 输出 RFC3339。
func normalizeValue(v any, ct *sql.ColumnType) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	case int64, float64, bool, string:
		return v
	default:
		// 其他类型（如 uint64）尝试转字符串避免 json 溢出
		return fmt.Sprintf("%v", v)
	}
}
