// db.ts — 数据库操作插件（内置通用版）
/**
 * 支持 PostgreSQL / MySQL / SQLite 的常用增删改查。
 * 连接信息由用户或其他技能提供（保存在 ~/.mizar/db_connections.json，或按需直接传 dsn）。
 *
 * 工具：
 *   db_conn     管理连接配置（add / list / remove / test）
 *   db_tables   列出表
 *   db_schema   查看表结构
 *   db_query    执行查询 SQL（SELECT 等，返回行）
 *   db_exec     执行写 SQL（INSERT/UPDATE/DELETE/DDL，返回受影响行数）
 *   db_insert   插入一行（对象形式，自动拼 SQL）
 *   db_update   更新（where 条件 + set 对象）
 *   db_delete   删除（where 条件）
 *   db_count    计数
 *
 * 宿主函数：db_query / db_exec_batch / db_close（driver: sqlite3 | mysql | postgres）
 */

const CONFIG_PATH = (function () {
  let h = "";
  try { h = env("MIZAR_HOME") || env("HOME") || ""; } catch (e) {}
  return (h || "/root") + '/.mizar/db_connections.json';
})();

interface Conn {
  name: string;
  driver: string; // sqlite3 | mysql | postgres
  dsn: string;
}

interface ConnFile {
  connections: Conn[];
}

function loadConns(): Conn[] {
  try {
    const raw = fs_read(CONFIG_PATH);
    if (!raw) return [];
    const j = JSON.parse(raw) as ConnFile;
    return (j && j.connections) || [];
  } catch {
    return [];
  }
}

function saveConns(conns: Conn[]): void {
  fs_write(CONFIG_PATH, JSON.stringify({ connections: conns }, null, 2));
}

// 解析连接：参数里给了 name 就查配置，否则直接用 driver+dsn
function resolveConn(args: { name?: string; driver?: string; dsn?: string }): Conn {
  if (args.name) {
    const c = loadConns().find((x) => x.name === args.name);
    if (!c) throw new Error(`未找到连接配置: ${args.name}（用 db_conn add 添加，或 db_conn list 查看）`);
    return c;
  }
  if (args.driver && args.dsn) return { name: '(inline)', driver: args.driver, dsn: args.dsn };
  throw new Error('需要指定 name（已保存的连接）或 driver+dsn（临时连接）');
}

// 值转 SQL 字面量（防注入：字符串转义单引号）
function lit(v: any): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'number') return String(v);
  if (typeof v === 'boolean') return v ? '1' : '0';
  return "'" + String(v).replace(/'/g, "''") + "'";
}

function whereClause(where: Record<string, any>): string {
  const keys = Object.keys(where || {});
  if (keys.length === 0) throw new Error('where 不能为空（防止全表误删/误改）');
  return keys.map((k) => `${k} = ${lit(where[k])}`).join(' AND ');
}

// ---------- 工具实现 ----------

/** 管理数据库连接配置。参数: {action:"add"|"list"|"remove"|"test", name?, driver?, dsn?} */
export function tool_db_conn(args: string): string {
  const p = JSON.parse(args || '{}') as { action: string; name?: string; driver?: string; dsn?: string };
  const conns = loadConns();
  switch (p.action) {
    case 'add': {
      if (!p.name || !p.driver || !p.dsn) return '错误: add 需要 name + driver + dsn';
      const existing = conns.findIndex((c) => c.name === p.name);
      const rec: Conn = { name: p.name, driver: p.driver, dsn: p.dsn };
      if (existing >= 0) conns[existing] = rec; else conns.push(rec);
      saveConns(conns);
      return `已保存连接: ${p.name} (${p.driver})`;
    }
    case 'list':
      if (conns.length === 0) return '（无已保存连接。用 db_conn add 添加）';
      return conns.map((c) => `- ${c.name} [${c.driver}] ${maskDsn(c.dsn)}`).join('\n');
    case 'remove': {
      const i = conns.findIndex((c) => c.name === p.name);
      if (i < 0) return `未找到连接: ${p.name}`;
      conns.splice(i, 1);
      saveConns(conns);
      return `已删除连接: ${p.name}`;
    }
    case 'test': {
      const c = resolveConn(p);
      db_query(c.driver, c.dsn, 'SELECT 1');
      return `连接正常: ${c.name} (${c.driver})`;
    }
    default:
      return '未知 action（支持 add / list / remove / test）';
  }
}

/** 列出数据库中的表。参数: {name?} 或 {driver?,dsn?}；可选 schema?（pg/mysql） */
export function tool_db_tables(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  const c = resolveConn(p);
  let sql: string;
  const d = c.driver.toLowerCase();
  if (d === 'sqlite' || d === 'sqlite3') {
    sql = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name";
  } else if (d === 'mysql' || d === 'mariadb') {
    sql = 'SHOW TABLES';
  } else {
    sql = "SELECT table_schema, table_name FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema, table_name";
  }
  return db_query(c.driver, c.dsn, sql);
}

/** 查看表结构。参数: {name?|driver?+dsn?, table} */
export function tool_db_schema(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.table) return '错误: 需要 table 参数';
  const c = resolveConn(p);
  const d = c.driver.toLowerCase();
  let sql: string;
  if (d === 'sqlite' || d === 'sqlite3') sql = `PRAGMA table_info(${p.table})`;
  else if (d === 'mysql' || d === 'mariadb') sql = `SHOW COLUMNS FROM ${p.table}`;
  else sql = `SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_name = ${lit(p.table)} ORDER BY ordinal_position`;
  return db_query(c.driver, c.dsn, sql);
}

/** 执行查询 SQL（SELECT）。参数: {name?|driver?+dsn?, sql, limit?} */
export function tool_db_query(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.sql) return '错误: 需要 sql 参数';
  const c = resolveConn(p);
  let sql = p.sql;
  if (p.limit && /^\s*select/i.test(sql) && !/limit\s+\d+/i.test(sql)) {
    sql = sql.replace(/;?\s*$/, '') + ` LIMIT ${parseInt(p.limit, 10)}`;
  }
  return db_query(c.driver, c.dsn, sql);
}

/** 执行写 SQL（INSERT/UPDATE/DELETE/DDL）。参数: {name?|driver?+dsn?, sql} */
export function tool_db_exec(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.sql) return '错误: 需要 sql 参数';
  const c = resolveConn(p);
  return db_query(c.driver, c.dsn, p.sql);
}

/** 插入一行。参数: {name?|driver?+dsn?, table, values:{...}} */
export function tool_db_insert(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.table || !p.values) return '错误: 需要 table + values';
  const c = resolveConn(p);
  const keys = Object.keys(p.values);
  if (keys.length === 0) return '错误: values 不能为空';
  const cols = keys.join(', ');
  const vals = keys.map((k) => lit(p.values[k])).join(', ');
  const sql = `INSERT INTO ${p.table} (${cols}) VALUES (${vals})`;
  return db_query(c.driver, c.dsn, sql);
}

/** 更新。参数: {name?|driver?+dsn?, table, values:{...}, where:{...}} */
export function tool_db_update(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.table || !p.values || !p.where) return '错误: 需要 table + values + where';
  const c = resolveConn(p);
  const sets = Object.keys(p.values).map((k) => `${k} = ${lit(p.values[k])}`).join(', ');
  const sql = `UPDATE ${p.table} SET ${sets} WHERE ${whereClause(p.where)}`;
  return db_query(c.driver, c.dsn, sql);
}

/** 删除。参数: {name?|driver?+dsn?, table, where:{...}} */
export function tool_db_delete(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.table || !p.where) return '错误: 需要 table + where（where 不能为空）';
  const c = resolveConn(p);
  const sql = `DELETE FROM ${p.table} WHERE ${whereClause(p.where)}`;
  return db_query(c.driver, c.dsn, sql);
}

/** 计数。参数: {name?|driver?+dsn?, table, where?} */
export function tool_db_count(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  if (!p.table) return '错误: 需要 table 参数';
  const c = resolveConn(p);
  let sql = `SELECT COUNT(*) AS cnt FROM ${p.table}`;
  if (p.where && Object.keys(p.where).length > 0) sql += ` WHERE ${whereClause(p.where)}`;
  return db_query(c.driver, c.dsn, sql);
}

/** 关闭连接（丢弃会话残留）。参数: {name?|driver?+dsn?} */
export function tool_db_close(args: string): string {
  const p = JSON.parse(args || '{}') as any;
  const c = resolveConn(p);
  return db_close(c.driver, c.dsn);
}

// 隐藏 DSN 里的密码
function maskDsn(dsn: string): string {
  return dsn.replace(/:\/\/([^:]+):([^@]+)@/, '://$1:***@');
}
