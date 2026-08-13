package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	// 内置驱动白名单（纯 Go，无 CGO，保持单二进制）
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

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

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return "", fmt.Errorf("db_query open: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

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
	res, err := db.ExecContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("db_query exec: %w", err)
	}
	n, _ := res.RowsAffected()
	return fmt.Sprintf(`{"rowsAffected":%d}`, n), nil
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
