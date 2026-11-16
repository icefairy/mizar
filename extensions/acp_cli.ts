// acp_cli.ts —— 官方 CLI 合规接入（ACP: Agent Client Protocol）
//
// 场景：网关侧已移除厂商账号池（codebuddy/qoder/cline），合规化改走官方 CLI。
// 本插件管理本机 codebuddy --serve 进程，通过官方暴露的 ACP over HTTP SSE
// 接口（/api/v1/acp/connect → /api/v1/acp JSON-RPC）完成一次对话。
//
// 协议实测（codebuddy 2.180.0）：
//   1. codebuddy --serve --port <p> --host 127.0.0.1 --auth password
//      --permission-mode bypassPermissions
//      ⚠ serve 启动目录即会话工作目录（session/new 的 cwd 不被采纳）；
//      ⚠ permission-mode 必须免审批（bypassPermissions），否则 CLI 弹工具
//      审批对话框挂起，ACP 客户端只能收到 timeout。
//      启动日志打印随机密码；HTTP 头需 x-codebuddy-request: 1 + Authorization: Bearer <pw>
//   2. POST /api/v1/acp/connect {} → {connectionId, sessionToken}
//      （连接令牌生命周期很短，prompt 必须紧跟 connect/new）
//   3. POST /api/v1/acp （JSON-RPC over streamable-http，SSE 响应）
//      头：acp-connection-id, X-ACP-Session-Token, Accept: application/json, text/event-stream
//      方法：initialize → session/new（需 mcpServers:[]）→ session/prompt
//      回复内容在流式通知 session/update（agent_message_chunk），不在最终 result
//
// 凭据完全由官方 CLI 自管（过期 401 时提示 /login），本插件不碰令牌。

const SERVE_PORT = 3011;
const SERVE_CWD = "/data/codes/mizar"; // serve 的工作目录 = 会话默认目录
const STATE_FILE = "/tmp/mizar-acp-cli.json";

function serveBase(): string {
  return "http://127.0.0.1:" + SERVE_PORT;
}

/** 读取 serve 密码（serve 由 supervisor 常驻管理；异常时可自动兜底拉起） */
function ensureServe(): { base: string; password: string } | string {
  // 先探活：serve 在跑则直接用缓存密码
  const alive = shell_exec(
    "curl -s -m 3 -o /dev/null -w '%{http_code}' -H 'x-codebuddy-request: 1' " +
    "http://127.0.0.1:" + SERVE_PORT + "/api/v1/auth/status 2>/dev/null",
  ).trim();
  if (alive === "200") {
    const st = shell_exec("cat " + STATE_FILE + " 2>/dev/null");
    if (st) {
      try {
        const o = JSON.parse(st);
        if (o.password) return { base: serveBase(), password: o.password };
      } catch {}
    }
    // 在跑但没缓存密码：从启动日志补抓
    const log0 = fs_read("/tmp/codebuddy-serve.log") || fs_read("/var/log/codebuddy-serve.log") || "";
    const m0 = log0.match(/Password\s+(\S+)/);
    if (m0) {
      fs_write(STATE_FILE, JSON.stringify({ password: m0[1] }));
      return { base: serveBase(), password: m0[1] };
    }
    return "serve 在运行但拿不到密码（请检查 /var/log/codebuddy-serve.log）";
  }
  // 探活失败：先试 supervisor（常驻首选），再兜底前台拉起
  shell_exec(
    "supervisorctl start codebuddy-serve 2>/dev/null; " +
    "sleep 3; curl -s -m 3 -o /dev/null -w '%{http_code}' -H 'x-codebuddy-request: 1' " +
    "http://127.0.0.1:" + SERVE_PORT + "/api/v1/auth/status 2>/dev/null",
  );
  const log = fs_read("/tmp/codebuddy-serve.log") || fs_read("/var/log/codebuddy-serve.log") || "";
  const m = log.match(/Password\s+(\S+)/);
  if (!m) return "codebuddy --serve 启动失败或未抓到密码，见 /tmp/codebuddy-serve.log";
  fs_write(STATE_FILE, JSON.stringify({ password: m[1] }));
  return { base: serveBase(), password: m[1] };
}

function acpHeaders(connId: string, tok: string, pw: string): string {
  return JSON.stringify({
    "Content-Type": "application/json",
    Accept: "application/json, text/event-stream",
    "x-codebuddy-request": "1",
    Authorization: "Bearer " + pw,
    "acp-connection-id": connId,
    "X-ACP-Session-Token": tok,
  });
}

/** 从 SSE 响应里抠出 data: 行中 id 匹配的 JSON-RPC result/error，并收集流式通知文本 */
function parseSSE(resp: string, id: number, collect: string[] | null): any {
  const lines = resp.split("\n");
  let final: any = { error: { code: -1, message: "no jsonrpc response for id " + id } };
  for (const ln of lines) {
    if (ln.indexOf("data: ") !== 0) continue;
    try {
      const j = JSON.parse(ln.slice(6));
      // 流式更新：session/update 通知里带 agent 消息
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
  // 非 SSE（直接 JSON）也兼容
  try {
    const j = JSON.parse(resp);
    if (j.id === id) final = j;
  } catch {}
  return final;
}

function rpc(
  connId: string,
  tok: string,
  pw: string,
  method: string,
  params: any,
  id: number,
): any {
  return rpcCollect(connId, tok, pw, method, params, id, null);
}

function rpcCollect(
  connId: string,
  tok: string,
  pw: string,
  method: string,
  params: any,
  id: number,
  collect: string[] | null,
): any {
  const resp = http_request(
    "POST",
    serveBase() + "/api/v1/acp",
    JSON.stringify({ jsonrpc: "2.0", id: id, method: method, params: params }),
    acpHeaders(connId, tok, pw),
  );
  return parseSSE(resp, id, collect);
}

/**
 * 用官方 CodeBuddy CLI 完成一次对话（ACP 会话：connect → initialize → session/new → prompt）。
 * 何时用：需要官方 CLI 背书的大模型算力（合规路径）时。
 * 参数: {prompt: string 消息, cwd?: string 工作目录(默认 /tmp), sessionCwd?: 同义}
 */
export function tool_acp_chat(args: string): string {
  let p: any = {};
  try { p = JSON.parse(args); } catch { p = { prompt: args }; }
  const prompt = String(p.prompt || args || "");
  if (!prompt) return "缺少 prompt";
  const cwd = String(p.cwd || "/tmp");

  const srv = ensureServe();
  if (typeof srv === "string") return srv;
  const H = JSON.stringify({
    "Content-Type": "application/json",
    "x-codebuddy-request": "1",
    Authorization: "Bearer " + srv.password,
  });

  // 1. connect
  const cresp = http_request("POST", srv.base + "/api/v1/acp/connect", "{}", H);
  let conn: any;
  try { conn = JSON.parse(cresp); } catch { return "connect 失败: " + cresp.slice(0, 300); }
  if (!conn.connectionId) return "connect 失败: " + cresp.slice(0, 300);
  const connId = conn.connectionId, tok = conn.sessionToken;

  // 2. initialize
  const init = rpc(connId, tok, srv.password, "initialize",
    { protocolVersion: 1, clientCapabilities: {} }, 1);
  if (init.error) return "initialize 失败: " + JSON.stringify(init.error).slice(0, 300);

  // 3. session/new
  const sn = rpc(connId, tok, srv.password, "session/new",
    { cwd: cwd, mcpServers: [] }, 2);
  if (sn.error) return "session/new 失败: " + JSON.stringify(sn.error).slice(0, 300) +
    "（若为 401 类错误，请到本机执行 codebuddy /login）";
  const sessionId = (sn.result && sn.result.sessionId) || "";

  // 4. session/prompt：收集流式 agent_message
  //    ⚠ permission-mode 必须选免审批（bypassPermissions），否则 CLI 侧
  //    弹出工具审批对话框挂起，ACP 客户端只能收到 timeout。
  const texts: string[] = [];
  const pr = rpcCollect(connId, tok, srv.password, "session/prompt",
    {
      sessionId: sessionId,
      prompt: [{ type: "text", text: prompt }],
      _meta: { "codebuddy.ai/permissionMode": "bypassPermissions" },
    }, 3, texts);
  if (pr.error) return "prompt 失败: " + JSON.stringify(pr.error).slice(0, 500);

  const r = pr.result || {};
  if (!texts.length) {
    const walk = (o: any) => {
      if (!o || typeof o !== "object") return;
      if (Array.isArray(o)) { o.forEach(walk); return; }
      if (o.type === "text" && typeof o.text === "string") texts.push(o.text);
      for (const k in o) walk(o[k]);
    };
    walk(r);
  }
  const out = texts.join("\n").trim();
  return out || JSON.stringify(r).slice(0, 2000);
}

/** 查看/关闭 serve 状态 */
export function tool_acp_status(_args: string): string {
  const running = shell_exec("pgrep -f 'codebuddy --serve' >/dev/null 2>&1 && echo yes || echo no").trim();
  return "codebuddy --serve 运行中: " + running + "（端口 " + SERVE_PORT + "）";
}
