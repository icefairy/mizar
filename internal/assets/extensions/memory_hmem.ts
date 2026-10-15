/**
 * Mizar 记忆插件 (mizar-memory) — HMEM 服务端版
 *
 * 连接 HMEM Server REST API，实现跨会话、跨 agent 的长期记忆共享。
 * 项目地址: https://github.com/icefairy/hmem_hermes_agent
 *
 * 与内置的本地 SQLite 版（memory.ts）二选一：
 *   - memory.ts      本地 SQLite，零依赖，开箱即用（默认启用）
 *   - memory_hmem.ts 连接 HMEM Server，多 agent 共用一套记忆（默认禁用，需自建服务）
 * 切换：/disable_plugin memory.ts && /enable_plugin memory_hmem.ts
 *
 * 配置（优先级从高到低）：
 *   1) 环境变量 MIZAR_HMEM_API_URL / MIZAR_HMEM_API_KEY / MIZAR_HMEM_NAMESPACE
 *   2) 配置文件 ~/.mizar/memory/hmem-config.json（/mem config set <key> <value> 写入）
 *
 * 记忆层级: observation → experience → [reflect] → insight / mental_model
 */

const DEFAULT_API_URL = "http://localhost:8000";
const DEFAULT_NS = "mizar-default";

interface HmemConfig {
  apiUrl: string;
  apiKey: string;
  namespace: string;
  sharedNs?: string;
}

interface MemItem {
  id: number;
  content: string;
  memory_type: string;
  mem_action?: string;
  mem_context?: Record<string, unknown>;
  mem_outcome?: Record<string, unknown>;
  mem_metadata?: Record<string, unknown>;
  score?: number;
  created_at?: string;
  updated_at?: string;
  parent_id?: number | null;
  hit_count?: number;
}

// ── 路径 / 环境 ───────────────────────────────

function homeDir(): string {
  let h = "";
  try { h = env("MIZAR_HOME") || env("HOME") || ""; } catch (e) { /* env 不可用则降级 */ }
  return h || "/root";
}
function configFile(): string {
  return homeDir() + "/.mizar/memory/hmem-config.json";
}
function getEnv(k: string): string {
  try { return env(k) || ""; } catch (e) { return ""; }
}

// ── 配置管理 ──────────────────────────────────

function loadConfig(): HmemConfig {
  // 1) 环境变量
  const envUrl = getEnv("MIZAR_HMEM_API_URL");
  const envKey = getEnv("MIZAR_HMEM_API_KEY");
  const envNs = getEnv("MIZAR_HMEM_NAMESPACE");

  // 2) 文件
  let fileCfg: Partial<HmemConfig> = {};
  try {
    const raw = fs_read(configFile());
    if (raw) fileCfg = JSON.parse(raw);
  } catch (e) { /* 文件不存在则忽略 */ }

  return {
    apiUrl: envUrl || fileCfg.apiUrl || DEFAULT_API_URL,
    apiKey: envKey || fileCfg.apiKey || "",
    namespace: envNs || fileCfg.namespace || DEFAULT_NS,
    sharedNs: fileCfg.sharedNs || "",
  };
}

function saveConfigFile(cfg: HmemConfig): void {
  try {
    fs_write(configFile(), JSON.stringify(cfg, null, 2));
  } catch (e) {
    log("mem: save config failed: " + String(e));
  }
}

// ── HTTP 辅助 ─────────────────────────────────

function headers(cfg: HmemConfig): Record<string, string> {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (cfg.apiKey) h["Authorization"] = "Bearer " + cfg.apiKey;
  return h;
}

function baseUrl(cfg: HmemConfig): string {
  return cfg.apiUrl.replace(/\/+$/, "");
}

function apiGet<T>(cfg: HmemConfig, path: string, params?: Record<string, string | number | undefined>): { ok: boolean; data?: T; error?: string } {
  try {
    let url = baseUrl(cfg) + path;
    if (params) {
      const qs = Object.entries(params)
        .filter(([_, v]) => v !== undefined && v !== null && v !== "")
        .map(([k, v]) => encodeURIComponent(k) + "=" + encodeURIComponent(String(v)))
        .join("&");
      if (qs) url += "?" + qs;
    }
    const resp = http_request("GET", url, "", JSON.stringify(headers(cfg)));
    if (!resp || resp === "") return { ok: false, error: "empty response from " + path };
    const data = JSON.parse(resp) as T;
    return { ok: true, data };
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    return { ok: false, error: "HMEM GET " + path + " failed: " + msg };
  }
}

function apiPost<T>(cfg: HmemConfig, path: string, body: unknown): { ok: boolean; data?: T; error?: string } {
  try {
    const resp = http_request("POST", baseUrl(cfg) + path, JSON.stringify(body), JSON.stringify(headers(cfg)));
    if (!resp || resp === "") return { ok: false, error: "empty response from " + path };
    const data = JSON.parse(resp) as T;
    return { ok: true, data };
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    return { ok: false, error: "HMEM POST " + path + " failed: " + msg };
  }
}

function apiDel<T>(cfg: HmemConfig, path: string, params?: Record<string, string | number | undefined>): { ok: boolean; data?: T; error?: string } {
  try {
    let url = baseUrl(cfg) + path;
    if (params) {
      const qs = Object.entries(params)
        .filter(([_, v]) => v !== undefined && v !== null && v !== "")
        .map(([k, v]) => encodeURIComponent(k) + "=" + encodeURIComponent(String(v)))
        .join("&");
      if (qs) url += "?" + qs;
    }
    const resp = http_request("DELETE", url, "", JSON.stringify(headers(cfg)));
    if (!resp || resp === "") return { ok: false, error: "empty response from " + path };
    const data = JSON.parse(resp) as T;
    return { ok: true, data };
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    return { ok: false, error: "HMEM DEL " + path + " failed: " + msg };
  }
}

// ── 工具函数 ──────────────────────────────────

function truncate(s: string, max = 300): string {
  if (s.length <= max) return s;
  return s.slice(0, max) + "...";
}

function json(data: unknown): string {
  return JSON.stringify(data, null, 2);
}

// 写入一条长期记忆（HMEM 持久化并自动嵌入向量）
// 参数(JSON): {"content":"记忆内容(必填)","memory_type?":"observation|experience(默认 observation)","mem_action?":"动作类型","mem_context?":{},"mem_outcome?":{},"mem_metadata?":{},"namespace?":"命名空间"}
// 注意: 不能直接写入 insight/mental_model（由 mem_reflect 自动生成）
export function tool_mem_write(args: string): string {
  const raw = JSON.parse(args || "{}");
  // 兼容工具系统可能包装的 {args: "..."} 格式
  const p = raw.args ? (typeof raw.args === 'string' ? JSON.parse(raw.args) : raw.args) : raw;
  const content = (p.content || "").toString().trim();
  if (!content) return "❌ `content` 不能为空";

  const cfg = loadConfig();
  const memoryType = (p.memory_type as string) || "observation";
  if (memoryType === "insight" || memoryType === "mental_model") {
    return "❌ 不能直接写入 insight/mental_model，它们由 mem_reflect 自动生成。请用 observation 或 experience。";
  }

  const result = apiPost<{ memory_id: number; content: string; namespace: string; embedded: boolean; auto_reflected: boolean }>(
    cfg,
    "/api/v1/memories",
    {
      content,
      namespace: (p.namespace as string) || cfg.namespace,
      memory_type: memoryType,
      mem_action: (p.mem_action as string) || "",
      mem_context: (p.mem_context as Record<string, unknown>) || {},
      mem_outcome: (p.mem_outcome as Record<string, unknown>) || {},
      mem_metadata: (p.mem_metadata as Record<string, unknown>) || {},
    }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const d = result.data!;
  return json({
    memory_id: d.memory_id,
    content: truncate(d.content, 100),
    namespace: d.namespace,
    embedded: d.embedded,
    auto_reflected: d.auto_reflected,
  });
}

// 语义搜索记忆（向量+关键词混合检索）
// 参数(JSON): {"query":"搜索词(必填)","limit?":10,"use_rerank?":true,"min_score?":0.1,"namespace?":"命名空间"}
export function tool_mem_search(args: string): string {
  const p = JSON.parse(args || "{}");
  const query = (p.query || "").toString().trim();
  if (!query) return "❌ `query` 不能为空";

  const cfg = loadConfig();
  const limit = Math.min(Number(p.limit) || 10, 50);
  const useRerank = p.use_rerank !== false;
  const minScore = p.min_score !== undefined ? Number(p.min_score) : undefined;
  const ns = (p.namespace as string) || cfg.namespace;

  // 分级共享
  const extra: string[] = [];
  if (cfg.sharedNs && cfg.sharedNs !== ns) extra.push(cfg.sharedNs);

  const result = apiPost<{ results: MemItem[]; count: number }>(
    cfg,
    "/api/v1/search",
    {
      query,
      limit,
      namespace: ns,
      use_rerank: useRerank,
      min_score: minScore,
      extra_namespaces: extra.length ? extra : undefined,
    }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const items = result.data!.results;
  if (items.length === 0) return "📭 未找到相关记忆。";

  const lines = items.map((m, i) => {
    const score = m.score === undefined ? "" : "[" + m.score.toFixed(2) + "]";
    const type = m.memory_type ? "(" + m.memory_type + ")" : "";
    const id = m.id ? "#" + m.id : "";
    return (i + 1) + ". " + score + " " + type + " " + id + " " + truncate(m.content, 200);
  });

  return "📚 找到 " + items.length + " 条相关记忆:\n\n" + lines.join("\n");
}

// 列出最近记忆（按时间倒序）
// 参数(JSON): {"namespace?":"命名空间","limit?":20,"memory_type?":"observation|experience|insight|mental_model"}
export function tool_mem_list(args: string): string {
  const p = JSON.parse(args || "{}");
  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;
  const limit = Math.min(Number(p.limit) || 20, 100);
  const memoryType = p.memory_type as string | undefined;

  const result = apiGet<{ results: MemItem[]; count: number }>(
    cfg,
    "/api/v1/memories",
    { namespace: ns, limit, memory_type: memoryType }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const items = result.data!.results;
  if (items.length === 0) return "📭 暂无记忆";

  const lines = items.map((m) => {
    const type = m.memory_type ?? "observation";
    const id = m.id ?? "?";
    const date = m.created_at ? m.created_at.slice(0, 19) : "";
    return "  #" + id + " [" + type + "] " + date + "  " + truncate(m.content, 120);
  });

  return "📖 " + items.length + " 条记忆:\n" + lines.join("\n");
}

// 按 ID 获取单条记忆详情
// 参数(JSON): {"memory_id":123,"namespace?":"命名空间"}
export function tool_mem_get(args: string): string {
  const p = JSON.parse(args || "{}");
  const memoryId = Number(p.memory_id);
  if (!memoryId) return "❌ `memory_id` 为必填";

  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;

  const result = apiGet<MemItem>(cfg, "/api/v1/memories/" + memoryId, { namespace: ns });
  if (!result.ok) return "❌ " + (result.error || "unknown error");
  return json(result.data);
}

// 按 ID 删除一条记忆
// 参数(JSON): {"memory_id":123,"namespace?":"命名空间"}
export function tool_mem_delete(args: string): string {
  const p = JSON.parse(args || "{}");
  const memoryId = Number(p.memory_id);
  if (!memoryId) return "❌ `memory_id` 为必填";

  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;

  const result = apiDel<{ deleted: boolean; memory_id: number }>(cfg, "/api/v1/memories/" + memoryId, { namespace: ns });
  if (!result.ok) return "❌ " + (result.error || "unknown error");
  return "🗑️ 已删除记忆 #" + memoryId;
}

// 查看记忆统计（总数/嵌入状态/按类型分布）
// 参数(JSON): {"namespace?":"命名空间"}
export function tool_mem_stats(args: string): string {
  const p = JSON.parse(args || "{}");
  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;

  const result = apiGet<{ total_memories: number; embedding_enabled: boolean; by_type: Record<string, number>; namespace: string }>(
    cfg,
    "/api/v1/stats",
    { namespace: ns }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const s = result.data!;
  const byType = Object.entries(s.by_type ?? {})
    .map(([k, v]) => "  " + k + ": " + v)
    .join("\n");

  return [
    "📊 记忆统计 — namespace: `" + s.namespace + "`",
    "  Total: " + s.total_memories,
    "  Embeddings: " + (s.embedding_enabled ? "✅ ON" : "❌ OFF"),
    byType ? "\n按类型:\n" + byType : "",
  ].filter(Boolean).join("\n");
}

// 触发反思引擎：从 experiences 自动生成 insight 与 mental model
// 参数(JSON): {"namespace?":"命名空间"}
export function tool_mem_reflect(args: string): string {
  const p = JSON.parse(args || "{}");
  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;

  const result = apiPost<{ status: string; stage?: string; reflect_count: number; counts?: Record<string, number> }>(
    cfg,
    "/api/v1/reflect",
    { namespace: ns }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const r = result.data!;
  return json({
    status: r.status,
    stage: r.stage ?? "unknown",
    reflect_count: r.reflect_count,
    counts: r.counts ?? {},
  });
}

// 查看已生成的洞察与心智模型
// 参数(JSON): {"namespace?":"命名空间","limit?":20}
export function tool_mem_models(args: string): string {
  const p = JSON.parse(args || "{}");
  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;
  const limit = Math.min(Number(p.limit) || 20, 100);

  const result = apiGet<{ results: Array<{ id: number; content: string; memory_type: string; supporting_experiences?: MemItem[]; created_at?: string }>; count: number }>(
    cfg,
    "/api/v1/mental-models",
    { namespace: ns, limit }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const items = result.data!.results;
  if (items.length === 0) return "🧠 还没有心智模型。多写 experience 并执行 mem_reflect 生成！";

  const lines = items.map((m, i) => {
    const type = m.memory_type === "mental_model" ? "🧠 心智模型" : "💡 洞察";
    const date = m.created_at ? m.created_at.slice(0, 19) : "";
    const supporting = m.supporting_experiences?.length ?? 0;
    return (i + 1) + ". " + type + " #" + m.id + " " + date + "\n   " + truncate(m.content, 200) + (supporting ? "\n   ↳ " + supporting + " 条支撑经验" : "");
  });

  return "🧠 洞察与心智模型 (" + items.length + "):\n\n" + lines.join("\n\n");
}

// 查看记忆知识图谱（节点/边规模与样例边）
// 参数(JSON): {"namespace?":"命名空间"}
export function tool_mem_graph(args: string): string {
  const p = JSON.parse(args || "{}");
  const cfg = loadConfig();
  const ns = (p.namespace as string) || cfg.namespace;

  const result = apiGet<{ nodes: Array<{ id: number; label: string; type: string }>; edges: Array<{ source: number; target: number; relation: string }> }>(
    cfg,
    "/api/v1/graph",
    { namespace: ns }
  );

  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const g = result.data!;
  return json({
    nodes: g.nodes.length,
    edges: g.edges.length,
    sample_edges: g.edges.slice(0, 10).map((e) => ({
      source: e.source,
      target: e.target,
      relation: e.relation,
    })),
  });
}

// 列出所有命名空间及各自记忆数
// 参数(JSON): {}
export function tool_mem_namespaces(args: string): string {
  const cfg = loadConfig();
  const result = apiGet<{ namespaces: Array<{ namespace: string; total_memories: number }> }>(cfg, "/api/v1/namespaces");
  if (!result.ok) return "❌ " + (result.error || "unknown error");
  const nss = result.data!.namespaces;
  if (nss.length === 0) return "📭 暂无命名空间。";
  return json(nss);
}

// ── 辅助: 检索并格式化成上下文 ────────────────

export function memory_context(query: string, limit = 3): string {
  if (!query || !query.trim()) return "";
  const cfg = loadConfig();
  const result = apiPost<{ results: MemItem[]; count: number }>(
    cfg,
    "/api/v1/search",
    { query: query.trim(), limit, namespace: cfg.namespace, use_rerank: true, min_score: 0.1 }
  );
  if (!result.ok || !result.data?.results.length) return "";

  const lines = result.data.results.map((m) => {
    const score = m.score === undefined ? "" : "[" + m.score.toFixed(2) + "]";
    const type = m.memory_type ?? "observation";
    return "- " + score + " (" + type + ") " + m.content;
  });

  return "## HMEM 记忆上下文\n\n相关经验:\n" + lines.join("\n");
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
      if (!body) return "用法: /mem store <内容> [--type experience] [--tag t1,t2]";
      const typeM = body.match(/--type\s+(\S+)/);
      const tagM = body.match(/--tag\s+(\S+)/);
      const content = body.replace(/--type\s+\S+/, "").replace(/--tag\s+\S+/, "").trim();
      return tool_mem_write(JSON.stringify({
        content,
        memory_type: typeM?.[1] || "observation",
        mem_metadata: tagM ? { tags: tagM[1].split(",") } : undefined,
      }));
    }

    case "search":
      return rest ? tool_mem_search(JSON.stringify({ query: rest })) : "用法: /mem search <query>";

    case "list":
      return tool_mem_list(JSON.stringify({ limit: 20 }));

    case "stats":
      return tool_mem_stats("{}");

    case "models":
      return tool_mem_models("{}");

    case "reflect":
      return tool_mem_reflect("{}");

    case "graph":
      return tool_mem_graph("{}");

    case "namespaces":
      return tool_mem_namespaces("{}");

    case "config": {
      const cfg = loadConfig();
      if (parts.length >= 3 && parts[1] === "set") {
        const key = parts[2];
        const value = parts.slice(3).join(" ");
        if (!value) return "用法: /mem config set <apiUrl|apiKey|namespace|sharedNs> <value>";
        const newCfg = { ...cfg, [key]: value };
        saveConfigFile(newCfg);
        return "✅ 已设置: " + key + "=" + value;
      }
      return [
        "当前配置（HMEM 服务端版）:",
        "  apiUrl:    " + cfg.apiUrl,
        "  apiKey:    " + (cfg.apiKey ? "****" : "(空)"),
        "  namespace: " + cfg.namespace,
        "  sharedNs:  " + (cfg.sharedNs || "(无)"),
        "",
        "设置: /mem config set <key> <value>",
      ].join("\n");
    }

    default:
      return [
        "记忆命令（HMEM 服务端版，多 agent 共用）:",
        "  /mem store <内容> [--type 类型] [--tag t1,t2]  存储记忆",
        "  /mem search <query>                           检索",
        "  /mem list                                     列出",
        "  /mem stats                                    统计",
        "  /mem models                                   列出洞察/心智模型",
        "  /mem reflect                                  归纳洞察",
        "  /mem graph                                    知识图谱",
        "  /mem namespaces                               列出命名空间",
        "  /mem config                                   查看/设置配置",
      ].join("\n");
  }
}
