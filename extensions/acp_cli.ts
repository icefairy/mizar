// acp_cli.ts —— 官方 CLI 合规接入（ACP: Agent Client Protocol）
//
// 场景：网关侧已移除厂商账号池（codebuddy/qoder/cline），合规化改走官方 CLI。
// 本插件管理本机官方 CLI 的 serve 进程，通过官方暴露的 ACP over HTTP SSE
// 接口完成对话。凭据完全由官方 CLI 自管（过期 401 时提示 /login），
// 本插件不做指纹伪装、不碰令牌。
//
// 支持多种官方 CLI：见下方 CLI_REGISTRY（新增 CLI 在此登记即可）。
//
// 协议形态（codebuddy 2.180.0 实测）：
//   1. codebuddy --serve --port <p> --host 127.0.0.1 --auth password
//      --permission-mode bypassPermissions
//      ⚠ serve 启动目录即会话工作目录（session/new 的 cwd 不被采纳）；
//      ⚠ permission-mode 必须免审批，否则 CLI 弹审批对话框挂起 → 客户端 timeout
//   2. POST /api/v1/acp/connect {} → {connectionId, sessionToken}
//      （连接令牌生命周期很短，prompt 必须紧跟 connect/new）
//   3. POST /api/v1/acp （JSON-RPC over streamable-http，SSE 响应）
//      方法：initialize → session/new（需 mcpServers:[]）→ session/prompt
//      回复内容在流式通知 session/update（agent_message_chunk），不在最终 result

// ============ CLI 注册表：新增官方 CLI 在此登记 ============
// kind: serve=HTTP serve 常驻（需探活/守护），oneshot=单次调用（每次冷启动）
const CLI_REGISTRY: any = {
  codebuddy: {
    name: "CodeBuddy CLI",
    kind: "serve",
    bin: "codebuddy",
    servePort: 3011,
    serveArgs: "--serve --host 127.0.0.1 --auth password --permission-mode bypassPermissions",
    healthPath: "/api/v1/auth/status",
    healthHeader: "x-codebuddy-request: 1",
    pwLogFiles: ["/tmp/codebuddy-serve.log", "/var/log/codebuddy-serve.log"],
    pwRegex: "Password\\s+(\\S+)",
    serveCmdEnv: "x-codebuddy-request",
    oneshotArgs: "-p {prompt}",
    loginHint: "本机执行 codebuddy /login（或从 Windows 侧同步 auth 文件）",
    default: true, // acp_chat 未指定 cli 时用它
  },
  // 新增示例（AC over stdio 的 CLI 适配见 acp_watchdog 同目录计划）：
  // claude: {
  //   name: "Claude Code CLI", kind: "oneshot", bin: "claude",
  //   oneshotArgs: "-p {prompt}", loginHint: "claude /login",
  // },
};

function defaultCliName(): string {
  for (const k in CLI_REGISTRY) if (CLI_REGISTRY[k].default) return k;
  for (const k in CLI_REGISTRY) return k; // 兜底第一个
  return "";
}

function resolveCli(name: string): any {
  if (name && CLI_REGISTRY[name]) return { key: name, cfg: CLI_REGISTRY[name] };
  const d = defaultCliName();
  if (d && CLI_REGISTRY[d]) return { key: d, cfg: CLI_REGISTRY[d] };
  return null;
}

const SERVE_CWD = "/data/codes/mizar"; // serve 的工作目录 = 会话默认目录
const STATE_FILE = "/tmp/mizar-acp-cli.json";

function stateKey(cliKey: string): string {
  // 每个独立的 CLI/端口 缓存独立状态
  return STATE_FILE.replace(/\.json$/, "-" + cliKey + ".json");
}

function serveBase(port: number): string {
  return "http://127.0.0.1:" + port;
}

// ============ serve 常驻类 CLI：探活/取密码/自愈 ============

function probeHealth(cfg: any): string {
  return shell_exec(
    "curl -s -m 3 -o /dev/null -w '%{http_code}' -H '" + cfg.healthHeader + "' " +
    serveBase(cfg.servePort) + cfg.healthPath + " 2>/dev/null; true",
  ).trim();
}

/** 读缓存的密码；丢失则从启动日志补抓 */
function servePassword(cliKey: string, cfg: any): string | null {
  const stf = stateKey(cliKey);
  const st = shell_exec("cat " + stf + " 2>/dev/null; true");
  if (st) {
    try {
      const o = JSON.parse(st);
      if (o.password) return o.password;
    } catch {}
  }
  for (const lf of cfg.pwLogFiles) {
    const log = fs_read(lf) || "";
    const m = log.match(new RegExp(cfg.pwRegex));
    if (m) {
      fs_write(stf, JSON.stringify({ password: m[1] }));
      return m[1];
    }
  }
  return null;
}

/** 确保 serve 在跑：探活→watchdog start→兜底直拉。返回 null=OK */
function ensureServe(cliKey: string, cfg: any): string | null {
  if (probeHealth(cfg) === "200") return null; // 在跑
  // 1) Zig 守护器（首选：脱离进程组，拷文件即跑）
  const wd = "/data/codes/mizar/extensions/acp_watchdog";
  shell_exec("cd " + SERVE_CWD + " && " + wd + " start >/dev/null 2>&1 || true");
  // 2) 兜底直拉（无 watchdog 二进制的机器）
  if (probeHealth(cfg) !== "200") {
    shell_exec(
      "cd " + SERVE_CWD + " && setsid nohup " + cfg.bin + " " + cfg.serveArgs +
      " --port " + cfg.servePort + " >/tmp/" + cliKey + "-serve.log 2>&1 < /dev/null & sleep 6 || true",
    );
  }
  // 3) 等就绪（CLI 冷启动可能 ~10s）
  for (let i = 0; i < 6; i++) {
    if (probeHealth(cfg) === "200") break;
    shell_exec("sleep 3");
  }
  return null;
}

// ============ ACP over HTTP 通用链路 ============

function acpHeaders(cfg: any, connId: string, tok: string, pw: string): string {
  return JSON.stringify({
    "Content-Type": "application/json",
    Accept: "application/json, text/event-stream",
    Authorization: "Bearer " + pw,
    "acp-connection-id": connId,
    "X-ACP-Session-Token": tok,
    // codebuddy 特有：请求头标记
    ...((cfg.serveCmdEnv ? JSON.parse('{"' + cfg.serveCmdEnv + '": "1"}') : {})),
  });
}

/** 从 SSE 响应里抠出 id 匹配的 JSON-RPC result/error，并收集流式通知文本 */
function parseSSE(resp: string, id: number, collect: string[] | null): any {
  const lines = resp.split("\n");
  let final: any = { error: { code: -1, message: "no jsonrpc response for id " + id } };
  for (const ln of lines) {
    if (ln.indexOf("data: ") !== 0) continue;
    try {
      const j = JSON.parse(ln.slice(6));
      if (j.method === "session/update" && collect) {
        const upd = j.params && j.params.update;
        if (upd && (upd.sessionUpdate === "agent_message" ||
                    upd.sessionUpdate === "agent_message_chunk" ||
                    upd.sessionUpdate === "agent_message_delta")) {
          const c = upd.content;
          if (Array.isArray(c)) {
            for (const it of c) if (it && it.type === "text" && it.text) collect.push(it.text);
          } else if (c && c.text) collect.push(c.text);
        }
      }
      if (j.id === id) final = j;
    } catch {}
  }
  try {
    const j = JSON.parse(resp);
    if (j.id === id) final = j;
  } catch {}
  return final;
}

function rpc(
  cfg: any, connId: string, tok: string, pw: string,
  method: string, params: any, id: number,
): any {
  const resp = http_request(
    "POST",
    serveBase(cfg.servePort) + "/api/v1/acp",
    JSON.stringify({ jsonrpc: "2.0", id: id, method: method, params: params }),
    acpHeaders(cfg, connId, tok, pw),
  );
  return parseSSE(resp, id, null);
}

function rpcCollect(
  cfg: any, connId: string, tok: string, pw: string,
  method: string, params: any, id: number, collect: string[] | null,
): any {
  const resp = http_request(
    "POST",
    serveBase(cfg.servePort) + "/api/v1/acp",
    JSON.stringify({ jsonrpc: "2.0", id: id, method: method, params: params }),
    acpHeaders(cfg, connId, tok, pw),
  );
  return parseSSE(resp, id, collect);
}

/**
 * 用官方 CLI 完成一次对话（ACP 会话：connect → initialize → session/new → prompt）。
 * 何时用：需要官方 CLI 背书的大模型算力（合规路径）时。
 * 参数: {prompt: string, cli?: string(注册表 key，默认 codebuddy),
 *        cwd?: string 工作目录(默认 SERVE_CWD)，serve 类 CLI 忽略 cwd}
 */
export function tool_acp_chat(args: string): string {
  let p: any = {};
  try { p = JSON.parse(args); } catch { p = { prompt: args }; }
  const prompt = String(p.prompt || args || "");
  if (!prompt) return "缺少 prompt";
  const r = resolveCli(String(p.cli || ""));
  if (!r) return "未知 CLI：" + String(p.cli) + "（可用：" + Object.keys(CLI_REGISTRY).join(", ") + "）";
  const cfg = r.cfg;

  // oneshot 类：直接单次调用
  if (cfg.kind === "oneshot") {
    const cmd = cfg.bin + " " + String(cfg.oneshotArgs).replace("{prompt}", JSON.stringify(prompt).slice(1, -1));
    const out = shell_exec(cmd + " 2>&1").trim();
    return out || "（CLI 无输出）";
  }

  // serve 类：ACP 全链路
  const e = ensureServe(r.key, cfg);
  if (e) return e;
  const pw = servePassword(r.key, cfg);
  if (!pw) return "serve 在运行但拿不到密码（检查 " + cfg.pwLogFiles.join(" / ") + "）";

  const H = JSON.stringify({
    "Content-Type": "application/json",
    Authorization: "Bearer " + pw,
    ...(cfg.serveCmdEnv ? JSON.parse('{"' + cfg.serveCmdEnv + '": "1"}') : {}),
  });

  const cresp = http_request("POST", serveBase(cfg.servePort) + "/api/v1/acp/connect", "{}", H);
  let conn: any;
  try { conn = JSON.parse(cresp); } catch { return "connect 失败: " + cresp.slice(0, 300); }
  if (!conn.connectionId) return "connect 失败: " + cresp.slice(0, 300);
  const connId = conn.connectionId, tok = conn.sessionToken;

  const init = rpc(cfg, connId, tok, pw, "initialize",
    { protocolVersion: 1, clientCapabilities: {} }, 1);
  if (init.error) return "initialize 失败: " + JSON.stringify(init.error).slice(0, 300);

  const sn = rpc(cfg, connId, tok, pw, "session/new",
    { cwd: String(p.cwd || SERVE_CWD), mcpServers: [] }, 2);
  if (sn.error) return "session/new 失败: " + JSON.stringify(sn.error).slice(0, 300) +
    "（若为 401 类错误：" + cfg.loginHint + "）";
  const sessionId = (sn.result && sn.result.sessionId) || "";

  const texts: string[] = [];
  const pr = rpcCollect(cfg, connId, tok, pw, "session/prompt",
    {
      sessionId: sessionId,
      prompt: [{ type: "text", text: prompt }],
    }, 3, texts);
  if (pr.error) return "prompt 失败: " + JSON.stringify(pr.error).slice(0, 500);

  const res = pr.result || {};
  if (!texts.length) {
    const walk = (o: any) => {
      if (!o || typeof o !== "object") return;
      if (Array.isArray(o)) { o.forEach(walk); return; }
      if (o.type === "text" && typeof o.text === "string") texts.push(o.text);
      for (const k in o) walk(o[k]);
    };
    walk(res);
  }
  const out = texts.join("\n").trim();
  return out || JSON.stringify(res).slice(0, 2000);
}

/**
 * 查看/管理当前使用的外部 CLI。
 * 何时用：确认接入了哪些官方 CLI、serve 是否在跑、CLI 版本；增删注册表编辑本文件 CLI_REGISTRY。
 * 参数: {} 状态总览；{"action":"list"} 同；{"cli":"codebuddy"} 指定详情
 */
export function tool_acp_status(args: string): string {
  let p: any = {};
  try { p = JSON.parse(args || "{}"); } catch { p = {}; }
  const lines: string[] = ["== Mizar 外部 CLI 注册表（acp_cli）=="];
  for (const k in CLI_REGISTRY) {
    const cfg = CLI_REGISTRY[k];
    const dflt = (k === defaultCliName()) ? " [默认]" : "";
    lines.push("\n[" + k + "]" + dflt + "  " + cfg.name);
    lines.push("  kind: " + cfg.kind + "  bin: " + cfg.bin);
    if (cfg.kind === "serve") {
      const code = probeHealth(cfg);
      const alive = code === "200" ? "✅ 在跑" : (code === "000" ? "❌ 未运行" : "⚠ HTTP " + code);
      lines.push("  serve: " + serveBase(cfg.servePort) + " → " + alive);
      const wd = shell_exec("test -x /data/codes/mizar/extensions/acp_watchdog && echo yes || echo no; true").trim();
      const wdStat = shell_exec("/data/codes/mizar/extensions/acp_watchdog status 2>/dev/null; true").trim();
      lines.push("  watchdog: " + (wd === "yes" ? "已安装（" + wdStat + "）" : "未安装"));
      const pw = servePassword(k, cfg);
      lines.push("  凭据: " + (pw ? "缓存有效" : "无缓存（首次对话时从日志抓取）"));
    } else {
      const v = shell_exec(cfg.bin + " --version 2>/dev/null | head -1").trim();
      lines.push("  版本: " + (v || "未安装/无版本输出"));
  }
    lines.push("  登录提示: " + cfg.loginHint);
  }
  const sel = String(p.cli || "");
  if (sel) lines.push("\n当前请求 CLI: " + (resolveCli(sel) ? sel + " ✅ 已注册" : sel + " ❌ 未注册"));
  lines.push("\n新增 CLI：编辑 acp_cli.ts 的 CLI_REGISTRY 登记 serve/oneshot 条目即可。");
  return lines.join("\n");
}
