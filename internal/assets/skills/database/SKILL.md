---
name: database
description: Use when 需要读写数据库（PostgreSQL / MySQL / SQLite）——查询、增删改查、看表结构。
---

# 数据库操作

配套插件 `db.ts`（内置）提供以下工具，支持 **PostgreSQL / MySQL / SQLite**。

## 何时用

- 用户要求查询/修改某个数据库的数据。
- 需要查看表结构、列出表、统计数据行数。
- 其他技能/流程需要持久化数据到数据库。

## 连接配置

连接信息**由用户或其他技能提供**，两种方式：

1. **保存为命名连接**（推荐，一次配置多次用）：

   ```
   db_conn {action:"add", name:"mydb", driver:"postgres", dsn:"postgres://user:pass@host:5432/dbname"}
   ```

   保存到 `~/.mizar/db_connections.json`。之后所有工具用 `name:"mydb"` 即可。

2. **临时内联**：每个工具调用直接传 `driver` + `dsn`，不保存。

driver 取值：`sqlite3` / `mysql` / `postgres`（`pg`、`mariadb` 等别名亦可）。

DSN 示例：
- SQLite：`/data/app.db` 或 `:memory:`
- MySQL：`user:pass@tcp(127.0.0.1:3306)/dbname`
- PostgreSQL：`postgres://user:pass@127.0.0.1:5432/dbname?sslmode=disable`

## 工具一览

| 工具 | 用途 | 关键参数 |
|------|------|---------|
| `db_conn` | 管理连接配置 | `action`: add/list/remove/test |
| `db_tables` | 列出所有表 | `name` 或 `driver`+`dsn` |
| `db_schema` | 查看表结构 | `table` |
| `db_query` | 查询（SELECT）| `sql`，可选 `limit` |
| `db_exec` | 写操作（INSERT/UPDATE/DELETE/DDL）| `sql` |
| `db_insert` | 插入一行 | `table` + `values` 对象 |
| `db_update` | 更新 | `table` + `values` + `where` |
| `db_delete` | 删除 | `table` + `where` |
| `db_count` | 计数 | `table`，可选 `where` |
| `db_close` | 关闭连接（丢弃会话残留）| `name` |

## 示例

```
# 列出表
db_tables {name:"mydb"}

# 看表结构
db_schema {name:"mydb", table:"users"}

# 查询
db_query {name:"mydb", sql:"SELECT id,name FROM users WHERE age > 18", limit:50}

# 插入
db_insert {name:"mydb", table:"users", values:{name:"张三", age:30}}

# 更新（where 必填）
db_update {name:"mydb", table:"users", values:{age:31}, where:{id:1}}

# 删除（where 必填）
db_delete {name:"mydb", table:"users", where:{id:1}}
```

## 安全与边界

- **`where` 不能为空**：`db_update` / `db_delete` 强制要求 `where`，防止全表误改/误删。
- **值自动转义**：`db_insert`/`db_update` 的字符串值会转义单引号（防注入）；但 `db_exec` 的裸 SQL 由调用方负责安全。
- **写操作前先 `db_query` 确认影响范围**：比如改之前先 SELECT 出目标行。
- **事务批量**：多条语句需原子执行时，用宿主函数 `db_exec_batch(driver, dsn, ["sql1","sql2"])`——同一事务，任一失败整体回滚。
- 连接按 DSN 复用（空闲 5 分钟回收）；`db_close` 可主动丢弃连接上的会话残留（PRAGMA/SET/临时表）。
