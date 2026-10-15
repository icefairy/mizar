/**
 * Mizar 记忆插件 (mizar-memory) — 自托管 SQLite 版
 *
 * 数据完全存在本地 SQLite（~/.mizar/memory/mem.db），零外部服务、零依赖。
 * 检索：中文用二元组(bigram)分词 + 关键词命中，英文/数字按词切分，
 *       用「重合度 + 覆盖率 + 频次」加权打分，返回相似度最高的若干条（默认 3 条）。
 *
 * 工具：mem_write / mem_search / mem_list / mem_get / mem_delete / mem_stats
 *       / mem_clear
 * 命令：/mem <store|search|list|get|delete|stats|clear|config>
 *
 * 宿主函数：db_query / db_exec_batch / db_close / fs_read / fs_write / log
 */

const DEFAULT_LIMIT = 3;

function homeDir(): string {
  // env() 是宿主函数（读进程环境变量）；测试/多用户下可定位正确 HOME。
  let h = "";
  try {
    h = env("MIZAR_HOME") || env("HOME") || "";
  } catch (e) { /* env 不可用时降级 */ }
  return h || "/root";
}
function dbPath(): string {
  return homeDir() + "/.mizar/memory/mem.db";
}
function dbDir(): string {
  return homeDir() + "/.mizar/memory";
}
function namespace(): string {
  const env = (typeof process !== "undefined" ? (process as any).env : {}) || {};
  return env.MIZAR_MEM_NAMESPACE || "default";
}

// ── SQLite 访问（经宿主 db_query）────────────────

let initialized = false;

function sql(statement: string): string {
  // 统一走 sqlite3 驱动；db_query 返回行数组 JSON 或 {"rowsAffected":N}
  return db_query("sqlite3", dbPath(), statement);
}

function sqlRows(statement: string): any[] {
  try {
    const out = sql(statement);
    const parsed = JSON.parse(out);
    return Array.isArray(parsed) ? parsed : [];
  } catch (e) {
    log("mem: query failed: " + String(e));
    return [];
  }
}

// SQL 字符串字面量（转义单引号）
function q(v: string): string {
  return "'" + String(v == null ? "" : v).replace(/'/g, "''") + "'";
}

function ensureInit(): void {
  if (initialized) return;
  // 建目录（fs_write 到目录下文件会创建父目录；这里先写一个占位再删不行，
  // 故用 db 自身建库——SQLite 首次写入会自动创建文件，但父目录需存在）。
  try {
    fs_write(dbDir() + "/.keep", "");
  } catch (e) { /* 已存在 */ }
  sql(`CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace TEXT NOT NULL DEFAULT 'default',
    content TEXT NOT NULL,
    tags TEXT DEFAULT '',
    memory_type TEXT DEFAULT 'observation',
    tokens TEXT DEFAULT '',
    created_at TEXT DEFAULT (datetime('now')),
    updated_at TEXT DEFAULT (datetime('now'))
  )`);
  sql(`CREATE INDEX IF NOT EXISTS idx_mem_ns ON memories(namespace)`);
  sql(`CREATE INDEX IF NOT EXISTS idx_mem_created ON memories(created_at)`);
  initialized = true;
}

// ── 中文分词（bigram）+ 关键词提取 ──────────────

// 停用词（高频无意义词，检索时忽略）
const STOPWORDS: Record<string, boolean> = {
  "的": true, "了": true, "是": true, "在": true, "和": true, "与": true,
  "我": true, "你": true, "他": true, "它": true, "这": true, "那": true,
  "有": true, "没": true, "不": true, "就": true, "也": true, "都": true,
  "一个": true, "什么": true, "怎么": true, "如何": true, "可以": true,
  "a": true, "an": true, "the": true, "is": true, "are": true, "of": true,
  "to": true, "and": true, "or": true, "in": true, "on": true, "for": true,
};

// 判断是否 CJK 字符
function isCJK(ch: string): boolean {
  const c = ch.charCodeAt(0);
  return (c >= 0x4e00 && c <= 0x9fff) || (c >= 0x3400 && c <= 0x4dbf);
}

// 分词：CJK 用二元组；连续 ASCII 字母/数字按整词
function tokenize(text: string): string[] {
  const s = String(text == null ? "" : text).toLowerCase();
  const tokens: string[] = [];
  let buf = "";
  const flushAscii = () => {
    if (buf.length >= 2) tokens.push(buf);
    else if (buf.length === 1 && /[a-z0-9]/.test(buf)) tokens.push(buf);
    buf = "";
  };
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (isCJK(ch)) {
      flushAscii();
      // 单个汉字也保留（作为 unigram），并生成与后一个汉字的 bigram
      tokens.push(ch);
      if (i + 1 < s.length && isCJK(s[i + 1])) {
        tokens.push(ch + s[i + 1]);
      }
    } else if (/[a-z0-9]/.test(ch)) {
      buf += ch;
    } else {
      flushAscii();
    }
  }
  flushAscii();
  // 去停用词、去重
  const seen: Record<string, boolean> = {};
  const out: string[] = [];
  for (const t of tokens) {
    if (STOPWORDS[t]) continue;
    if (seen[t]) continue;
    seen[t] = true;
    out.push(t);
  }
  return out;
}

// ── 相似度打分 ────────────────────────────────

// 对「记忆 tokens」与「查询 tokens」计算加权相似度：
//   score = 命中数 / 查询token数 * 覆盖率权重 + 命中数 / 记忆token数 * 精确度权重
// 再叠加命中 token 长度权重（长 token 更有信息量）。
function similarity(memTokens: string[], queryTokens: string[]): number {
  if (memTokens.length === 0 || queryTokens.length === 0) return 0;
  const memSet: Record<string, boolean> = {};
  for (const t of memTokens) memSet[t] = true;
  let hit = 0;
  let hitWeight = 0;
  let totalWeight = 0;
  for (const qt of queryTokens) {
    const w = Math.max(1, qt.length); // 长 token 权重高
    totalWeight += w;
    if (memSet[qt]) {
      hit++;
      hitWeight += w;
    }
  }
  if (hit === 0) return 0;
  const coverage = hitWeight / totalWeight;          // 查询被覆盖程度
  const precision = hit / memTokens.length;          // 记忆被命中比例
  // 以覆盖率为主、精确度为辅
  return coverage * 0.75 + precision * 0.25;
}

// ── 工具实现 ──────────────────────────────────

// 写入一条记忆（本地 SQLite 持久化）
// 参数(JSON): {"content":"记忆内容(必填)","tags?":["标签"],"memory_type?":"observation|experience|insight"}
export function tool_mem_write(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  const content = (p.content || "").toString().trim();
  if (!content) return "❌ `content` 不能为空";

  ensureInit();
  const tags = Array.isArray(p.tags) ? p.tags.join(",") : ((p.tags || "").toString());
  const memoryType = (p.memory_type || "observation").toString();
  const tokens = tokenize(content).join(" ");
  const ns = (p.namespace || namespace()).toString();

  sql(`INSERT INTO memories (namespace, content, tags, memory_type, tokens)
       VALUES (${q(ns)}, ${q(content)}, ${q(tags)}, ${q(memoryType)}, ${q(tokens)})`);
  const rows = sqlRows(`SELECT id FROM memories WHERE namespace=${q(ns)} ORDER BY id DESC LIMIT 1`);
  const id = rows.length ? rows[0].id : "?";

  return "✅ 已保存记忆 #" + id + "（namespace: " + ns + "）";
}

// 语义/关键词检索记忆，返回相似度最高的若干条（默认 3 条）
// 参数(JSON): {"query":"检索内容(必填)","limit?":3,"min_score?":0.05,"namespace?":"命名空间"}
export function tool_mem_search(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  const query = (p.query || "").toString().trim();
  if (!query) return "❌ `query` 不能为空";

  ensureInit();
  const ns = (p.namespace || namespace()).toString();
  const limit = Math.max(1, Math.min(Number(p.limit) || DEFAULT_LIMIT, 50));
  const minScore = p.min_score !== undefined ? Number(p.min_score) : 0.05;

  const queryTokens = tokenize(query);
  if (queryTokens.length === 0) return "📭 查询内容无有效词元。";

  // 拉取该命名空间全部记忆，在插件侧打分（SQLite 无中文分词，故内存计算）
  const all = sqlRows(
    `SELECT id, content, tags, memory_type, tokens, created_at FROM memories
     WHERE namespace=${q(ns)} ORDER BY id DESC LIMIT 2000`
  );
  if (all.length === 0) return "📭 暂无记忆。";

  const scored: Array<{ m: any; score: number }> = [];
  for (const m of all) {
    const memTokens = String(m.tokens || "").split(" ").filter((x: string) => x);
    const score = similarity(memTokens, queryTokens);
    if (score >= minScore) scored.push({ m, score });
  }
  scored.sort((a, b) => b.score - a.score);
  const top = scored.slice(0, limit);

  if (top.length === 0) return "📭 未找到相关记忆（可降低 min_score）。";

  const lines = top.map((it, i) => {
    const date = String(it.m.created_at || "").slice(0, 19);
    const tags = it.m.tags ? " [" + it.m.tags + "]" : "";
    return (i + 1) + ". [" + it.score.toFixed(2) + "] #" + it.m.id + tags + " " + date + "\n   " + it.m.content;
  });
  return "📚 找到 " + top.length + " 条相关记忆:\n\n" + lines.join("\n\n");
}

// 列出最近记忆（时间倒序）
// 参数(JSON): {"limit?":20,"namespace?":"命名空间","memory_type?":"类型"}
export function tool_mem_list(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  ensureInit();
  const ns = (p.namespace || namespace()).toString();
  const limit = Math.max(1, Math.min(Number(p.limit) || 20, 200));
  let where = `namespace=${q(ns)}`;
  if (p.memory_type) where += ` AND memory_type=${q(String(p.memory_type))}`;

  const rows = sqlRows(
    `SELECT id, content, tags, memory_type, created_at FROM memories
     WHERE ${where} ORDER BY id DESC LIMIT ${limit}`
  );
  if (rows.length === 0) return "📭 暂无记忆。";

  const lines = rows.map((m: any) => {
    const date = String(m.created_at || "").slice(0, 19);
    const tags = m.tags ? " [" + m.tags + "]" : "";
    const type = m.memory_type || "observation";
    return "  #" + m.id + " (" + type + ")" + tags + " " + date + "  " + m.content;
  });
  return "📖 " + rows.length + " 条记忆:\n" + lines.join("\n");
}

// 按 ID 获取单条记忆
// 参数(JSON): {"id":123}
export function tool_mem_get(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  const id = Number(p.id || p.memory_id);
  if (!id) return "❌ `id` 为必填";
  ensureInit();
  const rows = sqlRows(`SELECT * FROM memories WHERE id=${id} LIMIT 1`);
  if (rows.length === 0) return "📭 未找到记忆 #" + id;
  return JSON.stringify(rows[0], null, 2);
}

// 按 ID 删除一条记忆
// 参数(JSON): {"id":123}
export function tool_mem_delete(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  const id = Number(p.id || p.memory_id);
  if (!id) return "❌ `id` 为必填";
  ensureInit();
  const before = sqlRows(`SELECT id FROM memories WHERE id=${id} LIMIT 1`);
  if (before.length === 0) return "📭 未找到记忆 #" + id;
  sql(`DELETE FROM memories WHERE id=${id}`);
  return "🗑️ 已删除记忆 #" + id;
}

// 记忆统计
// 参数(JSON): {"namespace?":"命名空间"}
export function tool_mem_stats(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  ensureInit();
  const ns = (p.namespace || namespace()).toString();

  const total = sqlRows(`SELECT COUNT(*) AS cnt FROM memories WHERE namespace=${q(ns)}`);
  const byType = sqlRows(
    `SELECT memory_type, COUNT(*) AS cnt FROM memories WHERE namespace=${q(ns)} GROUP BY memory_type`
  );
  const nsList = sqlRows(
    `SELECT namespace, COUNT(*) AS cnt FROM memories GROUP BY namespace`
  );

  const lines: string[] = [];
  lines.push("📊 记忆统计 — namespace: `" + ns + "`");
  lines.push("  总数: " + (total.length ? total[0].cnt : 0));
  if (byType.length) {
    lines.push("  按类型:");
    for (const r of byType) lines.push("    " + r.memory_type + ": " + r.cnt);
  }
  if (nsList.length) {
    lines.push("  命名空间:");
    for (const r of nsList) lines.push("    " + r.namespace + ": " + r.cnt);
  }
  lines.push("  数据库: " + dbPath());
  return lines.join("\n");
}

// 清空记忆（按命名空间，谨慎）
// 参数(JSON): {"namespace?":"命名空间","all?":false}
export function tool_mem_clear(args: string): string {
  const raw = JSON.parse(args || "{}");
  const p = raw.args ? (typeof raw.args === "string" ? JSON.parse(raw.args) : raw.args) : raw;
  ensureInit();
  if (p.all) {
    sql(`DELETE FROM memories`);
    return "🗑️ 已清空全部命名空间的记忆。";
  }
  const ns = (p.namespace || namespace()).toString();
  const before = sqlRows(`SELECT COUNT(*) AS cnt FROM memories WHERE namespace=${q(ns)}`);
  const n = before.length ? before[0].cnt : 0;
  sql(`DELETE FROM memories WHERE namespace=${q(ns)}`);
  return "🗑️ 已清空 namespace `" + ns + "` 的 " + n + " 条记忆。";
}

// ── 斜杠命令 /mem ────────────────────────────

export function command_mem(args: string): string {
  const parts = (args || "").trim().split(/\s+/);
  const cmd = (parts[0] || "help").toLowerCase();
  const rest = parts.slice(1).join(" ");

  switch (cmd) {
    case "store": {
      const idx = args.indexOf(" ");
      const body = idx >= 0 ? args.slice(idx + 1).trim() : "";
      if (!body) return "用法: /mem store <内容> [--tag t1,t2] [--type experience]";
      const typeM = body.match(/--type\s+(\S+)/);
      const tagM = body.match(/--tag\s+(\S+)/);
      const content = body.replace(/--type\s+\S+/, "").replace(/--tag\s+\S+/, "").trim();
      return tool_mem_write(JSON.stringify({
        content,
        memory_type: typeM ? typeM[1] : "observation",
        tags: tagM ? tagM[1].split(",") : [],
      }));
    }
    case "search":
      return rest ? tool_mem_search(JSON.stringify({ query: rest })) : "用法: /mem search <query> [limit]";
    case "list":
      return tool_mem_list(JSON.stringify({ limit: 20 }));
    case "get":
      return rest ? tool_mem_get(JSON.stringify({ id: rest })) : "用法: /mem get <id>";
    case "delete":
      return rest ? tool_mem_delete(JSON.stringify({ id: rest })) : "用法: /mem delete <id>";
    case "stats":
      return tool_mem_stats("{}");
    case "clear":
      return tool_mem_clear(rest ? JSON.stringify({ namespace: rest }) : "{}");
    case "config":
      return [
        "记忆存储（自托管 SQLite）:",
        "  数据库:   " + dbPath(),
        "  命名空间: " + namespace() + "（环境变量 MIZAR_MEM_NAMESPACE 可改）",
        "  检索:     中文二元组分词 + 关键词重合度打分，默认返回 3 条",
      ].join("\n");
    default:
      return [
        "记忆命令（本地 SQLite，零外部依赖）:",
        "  /mem store <内容> [--tag t1,t2] [--type 类型]  存储记忆",
        "  /mem search <query> [limit]                   检索（默认 3 条）",
        "  /mem list                                     列出最近记忆",
        "  /mem get <id>                                 查看单条",
        "  /mem delete <id>                              删除",
        "  /mem stats                                    统计",
        "  /mem clear [namespace]                        清空（谨慎）",
        "  /mem config                                   存储信息",
      ].join("\n");
  }
}
