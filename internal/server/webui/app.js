"use strict";
// 开阳 Mizar Web UI —— 纯原生 JS，无外部依赖。
(function () {
  const $ = (id) => document.getElementById(id);
  const loginView = $("login"), appView = $("app");
  const log = $("log"), input = $("input"), sendBtn = $("sendBtn");
  const loginForm = $("loginForm"), loginBtn = $("loginBtn"), loginErr = $("loginErr");
  const modeChat = $("modeChat"), modeAgent = $("modeAgent");
  const abortBtn = $("abortBtn"), logoutBtn = $("logoutBtn"), modelTag = $("modelTag");
  const convList = $("convList"), newConvBtn = $("newConvBtn"), sidebar = $("sidebar"), menuBtn = $("menuBtn");
  const pinBtn = $("pinBtn"), themeBtn = $("themeBtn");
  const settingsBtn = $("settingsBtn"), settingsModal = $("settings"), settingsClose = $("settingsClose");
  const settingsSave = $("settingsSave"), settingsMsg = $("settingsMsg"), refreshModels = $("refreshModels");
  const qkModel = $("qkModel"), qkThinking = $("qkThinking"), quickMsg = $("quickMsg");
  const brandVer = $("brandVer"), statusbar = $("statusbar"), queueMode = $("queueMode"), queueLabel = $("queueLabel");
  const queueBar = $("queueBar");
  const providerSelect = $("providerSelect"), providerUse = $("providerUse"), providerDel = $("providerDel");
  const providerCurrent = $("providerCurrent"), providerHint = $("providerHint"), providerAdd = $("providerAdd");
  const pvName = $("pvName"), pvURL = $("pvURL"), pvKey = $("pvKey"), pvModel = $("pvModel");

  let mode = "chat";   // chat | agent
  // busyConv：当前正在跑一轮的会话 id（null=空闲）。用它代替全局 busy，
  // 这样切换到别的会话时不会「假忙碌」，也避免不同会话的流式增量互相串台。
  let busyConv = null;
  // runningConv：服务端「当前正在运行」的会话 id（来自 /web/api/status）。
  // Agent 是单实例，全局只有一个循环在跑；用它区分「哪个会话占着循环」，
  // 这样在 B 会话发送时能明确提示「A 会话正在运行」，而不是莫名其妙排队。
  let runningConv = null;
  // evtLog：按会话缓存运行中事件。切走期间 A 会话持续输出，前端仍会收 WS 事件
  // （只是不渲染到 DOM），先缓存起来；切回 A 时重放，避免「切走期间的内容丢失」。
  // 结构：{ convId: [ {kind, ...} ] }，每轮开始时清空对应会话。
  let evtLog = {};
  function pushEvt(conv, e) {
    const key = conv == null ? "" : conv;
    const arr = evtLog[key] || (evtLog[key] = []);
    // 防御性上限，避免极端长任务把内存撑爆（保留最近的）
    arr.push(e);
    if (arr.length > 4000) arr.splice(0, arr.length - 4000);
  }
  // 打断/排队模式：true=打断（等待期间发送=注入当前循环），false=排队（本轮结束再发）。
  // 默认打断，选择持久化到 localStorage。
  let interruptMode = true;
  try { interruptMode = localStorage.getItem("mizar-queue-mode") !== "1"; } catch (e) {}
  function isBusy() { return busyConv !== null && busyConv === curConv; }
  function applyQueueToggle() {
    if (!queueMode) return;
    queueMode.checked = interruptMode;
    if (queueLabel) queueLabel.textContent = interruptMode ? "打断" : "排队";
    if (queueMode.parentElement) {
      queueMode.parentElement.title = interruptMode
        ? "打断模式（默认）：等待期间发送的消息会注入当前循环纠正它"
        : "排队模式：消息排队，本轮结束后自动发送";
    }
  }
  let ws = null;
  let history = [];      // 输入历史
  let historyIndex = 0;  // 历史指针
  let historySaved = "";  // 历史导航前的临时内容
  let curConv = null;  // 当前会话 id
  let convs = [];      // 会话摘要列表
  // 浏览器标题：保留原有「开阳 Mizar 版本号」，前面加上当前会话主题。
  let baseTitle = "开阳 Mizar";
  function setBaseTitle(t) { baseTitle = t || "开阳 Mizar"; applyBrowserTitle(); }
  function applyBrowserTitle() {
    const c = convs.find((x) => x.id === curConv);
    const t = c && c.title && c.title !== "新会话" ? c.title : "";
    document.title = t ? (t + " · " + baseTitle) : baseTitle;
  }
  let streamBody = null;   // 当前流式正文目标（.《text》）
  let streamBuf = "";      // 流式正文累计
  let streamStep = -1;     // 当前流式 step
  let stepsBody = null;    // 答复框内的步骤容器（.《steps》）
  // 当前轮（turn）：用户提问 → 最终答复（内含 .steps 过程 + .text 正文）
  let turnEl = null;       // 当前轮容器
  let thinkStepEl = null;  // 当前思考步骤
  let thinkStepBuf = "";   // 思考累计

  // ---------- 主题 ----------
  function applyTheme(t) {
    document.documentElement.setAttribute("data-theme", t);
    try { localStorage.setItem("mizar-theme", t); } catch (e) {}
    if (themeBtn) themeBtn.textContent = t === "dark" ? "☀️" : "🌙";
  }
  (function initTheme() {
    let t = "light";
    try { t = localStorage.getItem("mizar-theme") || (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"); } catch (e) {}
    applyTheme(t);
  })();
  themeBtn.addEventListener("click", () => {
    applyTheme(document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark");
  });

  // ---------- 侧栏常驻锁定 ----------
  let pinned = false;
  try { pinned = localStorage.getItem("mizar-pin") === "1"; } catch (e) {}
  function applyPin() {
    sidebar.classList.toggle("pinned", pinned);
    pinBtn.classList.toggle("active", pinned);
    pinBtn.title = pinned ? "已锁定常驻（点击解锁）" : "锁定侧栏常驻（PC 端）";
    if (pinned) sidebar.classList.remove("collapsed");
    try { localStorage.setItem("mizar-pin", pinned ? "1" : "0"); } catch (e) {}
  }
  pinBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    pinned = !pinned;
    applyPin();
  });
  applyPin();

  // ---------- 工具 ----------
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  // 会话最后活动时间：今天显示 HH:MM，今年显示 M月D日，更早显示 YYYY-MM-DD。
  function fmtConvTime(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    const now = new Date();
    const p2 = (n) => String(n).padStart(2, "0");
    const hm = p2(d.getHours()) + ":" + p2(d.getMinutes());
    if (d.toDateString() === now.toDateString()) return hm;          // 今天
    const y = new Date(now); y.setDate(now.getDate() - 1);
    if (d.toDateString() === y.toDateString()) return "昨天 " + hm;   // 昨天
    if (d.getFullYear() === now.getFullYear()) return (d.getMonth() + 1) + "月" + d.getDate() + "日";
    return d.getFullYear() + "-" + p2(d.getMonth() + 1) + "-" + p2(d.getDate());
  }
  // 行内 markdown（输入已转义的纯文本）
  function inline(t) {
    let x = esc(t);
    x = x.replace(/`([^`]+)`/g, "<code>$1</code>");
    // 图片（须在链接之前）
    x = x.replace(/!\[([^\]]*)\]\(([^)]+)\)/g,
      '<img src="$2" alt="$1" loading="lazy" style="max-width:100%">');
    // 链接
    x = x.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    x = x.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    x = x.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
    // 删除线
    x = x.replace(/~~([^~]+)~~/g, "<del>$1</del>");
    // 高亮 ==x==
    x = x.replace(/==([^=]+)==/g, "<mark>$1</mark>");
    x = x.replace(/\n/g, "<br>");
    return x;
  }
  // 列表解析：支持缩进嵌套与任务列表 - [ ] / - [x]
  // 返回 { html, next }。以起始行缩进为基准，更深缩进的条目归入子列表。
  function parseList(lines, start) {
    const first = lines[start].match(/^(\s*)([-*+]|\d+\.)\s+/);
    const baseIndent = first[1].length;
    const ordered = /\d+\./.test(first[2]);
    const tag = ordered ? "ol" : "ul";
    let html = "<" + tag + ">";
    let i = start;
    while (i < lines.length) {
      const m = lines[i].match(/^(\s*)([-*+]|\d+\.)\s+(.*)$/);
      if (!m) break;
      const indent = m[1].length;
      if (indent < baseIndent) break;
      if (indent > baseIndent) {
        // 子列表：递归，结果并进上一个 <li>（移除其闭合标签后追加）
        const sub = parseList(lines, i);
        html = html.replace(/<\/li>$/, sub.html + "</li>");
        i = sub.next;
        continue;
      }
      let text = m[3];
      // 任务列表
      const task = text.match(/^\[([ xX])\]\s+(.*)$/);
      if (task) {
        const checked = task[1].toLowerCase() === "x";
        html += '<li class="task"><input type="checkbox" disabled' + (checked ? " checked" : "") +
          ">" + inline(task[2]) + "</li>";
      } else {
        html += "<li>" + inline(text) + "</li>";
      }
      i++;
    }
    return { html: html + "</" + tag + ">", next: i };
  }
  // 表格辅助：按未转义的 | 拆单元格
  function splitRow(line) {
    let s = String(line).trim();
    if (s.startsWith("|")) s = s.slice(1);
    if (s.endsWith("|")) s = s.slice(0, -1);
    const cells = [];
    let cur = "";
    for (let k = 0; k < s.length; k++) {
      if (s[k] === "\\" && s[k + 1] === "|") { cur += "|"; k++; }
      else if (s[k] === "|") { cells.push(cur.trim()); cur = ""; }
      else cur += s[k];
    }
    cells.push(cur.trim());
    return cells;
  }
  // 分隔行：| --- | :--: | 之类（至少一个 -）
  function isSepRow(line) {
    return /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(line);
  }
  // 表格起始：当前行含 |，且下一行是分隔行
  function isTableStart(lines, i) {
    return i + 1 < lines.length && String(lines[i]).includes("|") && isSepRow(lines[i + 1]);
  }
  function alignOf(sep) {
    return splitRow(sep).map((c) => {
      const l = c.startsWith(":"), r = c.endsWith(":");
      if (l && r) return "center"; if (r) return "right"; if (l) return "left"; return "";
    });
  }
  // markdown：各级标题 / 代码块（带复制）/ 表格 / 列表 / 引用 / 行内
  function md(s) {
    const lines = String(s == null ? "" : s).split("\n");
    let html = "", i = 0;
    while (i < lines.length) {
      const line = lines[i];
      // 围栏代码块
      const fence = line.match(/^\s*```(\w*)\s*$/);
      if (fence) {
        const lang = fence[1] || "";
        const buf = [];
        i++;
        while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) { buf.push(lines[i]); i++; }
        i++; // 跳过结束围栏
        html += '<div class="code-wrap">' +
          (lang ? '<span class="code-lang">' + esc(lang) + "</span>" : "") +
          '<button class="code-copy" type="button">复制</button>' +
          "<pre><code>" + esc(buf.join("\n")) + "</code></pre></div>";
        continue;
      }
      // 表格（当前行含 | 且下一行是分隔行）
      if (isTableStart(lines, i)) {
        const header = splitRow(line);
        const aligns = alignOf(lines[i + 1]);
        i += 2; // 跳过表头 + 分隔行
        let th = "<tr>" + header.map((c, k) =>
          '<th' + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : "") + ">" + inline(c) + "</th>").join("") + "</tr>";
        let tb = "";
        while (i < lines.length && String(lines[i]).includes("|") && !/^\s*$/.test(lines[i])) {
          const cells = splitRow(lines[i]);
          tb += "<tr>" + cells.map((c, k) =>
            "<td" + (aligns[k] ? ' style="text-align:' + aligns[k] + '"' : "") + ">" + inline(c) + "</td>").join("") + "</tr>";
          i++;
        }
        html += '<div class="md-table-wrap"><table><thead>' + th + "</thead><tbody>" + tb + "</tbody></table></div>";
        continue;
      }
      // 标题
      const h = line.match(/^(#{1,6})\s+(.*)$/);
      if (h) { const n = h[1].length; html += "<h" + n + ">" + inline(h[2]) + "</h" + n + ">"; i++; continue; }
      // 分隔线
      if (/^\s*(-{3,}|\*{3,})\s*$/.test(line)) { html += "<hr>"; i++; continue; }
      // 引用
      if (/^\s*>\s?/.test(line)) {
        const buf = [];
        while (i < lines.length && /^\s*>\s?/.test(lines[i])) { buf.push(lines[i].replace(/^\s*>\s?/, "")); i++; }
        html += "<blockquote>" + inline(buf.join("\n")) + "</blockquote>";
        continue;
      }
      // 列表（有序/无序，支持缩进嵌套与任务列表）
      if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
        const consumed = parseList(lines, i);
        html += consumed.html;
        i = consumed.next;
        continue;
      }
      // 空行
      if (/^\s*$/.test(line)) { i++; continue; }
      // 段落
      const buf = [line]; i++;
      while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^\s*```/.test(lines[i]) &&
        !/^#{1,6}\s/.test(lines[i]) && !/^\s*[-*+]\s+/.test(lines[i]) &&
        !/^\s*\d+\.\s+/.test(lines[i]) && !/^\s*>\s?/.test(lines[i]) &&
        !/^\s*(-{3,}|\*{3,})\s*$/.test(lines[i]) && !isTableStart(lines, i)) { buf.push(lines[i]); i++; }
      html += "<p>" + inline(buf.join("\n")) + "</p>";
    }
    return html;
  }

  async function api(path, opts) {
    const r = await fetch(path, Object.assign({ credentials: "same-origin" }, opts || {}));
    let j = null;
    try { j = await r.json(); } catch (e) {}
    if (r.status === 401) { showLogin(); throw new Error("未登录"); }
    if (!r.ok) {
      const err = new Error((j && j.error && j.error.message) || ("HTTP " + r.status));
      err.status = r.status;
      throw err;
    }
    return j;
  }

  // 追加一条消息；assistant/error 走 md 渲染，其余纯文本
  function addMsg(kind, who, text) {
    const wrap = document.createElement("div");
    wrap.className = "msg " + kind;
    wrap.__raw = text == null ? "" : String(text);
    wrap.innerHTML = '<div class="who">' + esc(who) + '</div><div class="body"></div>' +
      '<div class="msg-actions"><button class="act msg-copy" type="button">复制</button></div>';
    const body = wrap.querySelector(".body");
    if (kind === "assistant" || kind === "error") body.innerHTML = md(text);
    else body.textContent = text;
    turnHost().appendChild(wrap);
    scrollLog();
    return body;
  }
  function clearLog() { log.innerHTML = ""; }

  // ---------- 轮（turn）结构 ----------
  // 一轮 = 用户提问 + 工作过程（思考/工具调用，可折叠）+ 最终答复。
  // 过程默认折叠、紧凑单行；最终答复独立成气泡，便于快速定位。
  function resetTurn() {
    turnEl = null; thinkStepEl = null; thinkStepBuf = "";
    streamBody = null; streamBuf = ""; streamStep = -1; stepsBody = null;
  }
  function newTurn() {
    resetTurn();
    turnEl = document.createElement("div");
    turnEl.className = "turn";
    log.appendChild(turnEl);
    return turnEl;
  }
  function turnHost() { return turnEl || log; }
  function scrollLog() { log.scrollTop = log.scrollHeight; }
  // 消息时间：RFC3339 → HH:MM（跨天带日期），解析失败则原样展示
  function fmtTs(ts) {
    if (!ts) return "";
    const d = new Date(ts);
    if (isNaN(d.getTime())) return String(ts);
    const pad = (n) => String(n).padStart(2, "0");
    const hm = pad(d.getHours()) + ":" + pad(d.getMinutes());
    const now = new Date();
    const sameDay = d.toDateString() === now.toDateString();
    return sameDay ? hm : (pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + hm);
  }
  // 构建 who 行（角色名 + 可选时间）
  function whoHtml(name, ts) {
    const t = fmtTs(ts);
    return '<div class="who">' + esc(name) + (t ? '<span class="ts">' + esc(t) + '</span>' : '') + '</div>';
  }
  function addUserMsg(text, ts) {
    const wrap = document.createElement("div");
    wrap.className = "msg user";
    wrap.__raw = text == null ? "" : String(text);
    wrap.innerHTML = whoHtml("我", ts) + '<div class="body"></div>' +
      '<div class="msg-actions"><button class="act msg-copy" type="button">复制</button></div>';
    wrap.querySelector(".body").textContent = wrap.__raw;
    turnHost().appendChild(wrap);
    scrollLog();
    return wrap;
  }
  // bumpProc 已改为空操作：步骤直接累加进答复框 .steps，不再独立计数展示
  function bumpProc() {}
  // 确保最终答复气泡存在：body 内含 steps（过程步骤）+ text（markdown 正文），两者独立容器避免互相覆盖
  function ensureReply(ts) {
    if (streamBody) return streamBody;
    const wrap = document.createElement("div");
    wrap.className = "msg assistant";
    wrap.__raw = "";
    wrap.innerHTML = whoHtml("开阳", ts) + '<div class="body">' +
      '<div class="steps"></div><div class="text"></div></div>' +
      '<div class="msg-actions"><button class="act msg-copy" type="button">复制</button></div>';
    turnHost().appendChild(wrap);
    stepsBody = wrap.querySelector(".steps");
    streamBody = wrap.querySelector(".text");
    streamBuf = "";
    return streamBody;
  }
  // 确保步骤容器存在（供 onToolCall/onToolResult/thinking 追加）
  function ensureSteps() {
    if (stepsBody) return stepsBody;
    if (streamBody) { stepsBody = streamBody.parentElement.querySelector(".steps"); }
    if (!stepsBody) { const b = ensureReply(); stepsBody = b.parentElement.querySelector(".steps"); }
    return stepsBody;
  }
  // 单行压缩：换行/多空格折叠，超长截断
  function compact(s, n) {
    const t = String(s == null ? "" : s).replace(/\s+/g, " ").trim();
    return t.length > n ? t.slice(0, n) + " …" : t;
  }
  // 收尾当前轮：过程为空则删除；保证「过程」在最终答复之前
  function finalizeTurn() {
    if (streamBody) streamBody.classList.remove("cursor");
    if (thinkStepEl) thinkStepEl.classList.remove("cursor");
    scrollLog();
  }

  // 复制（带降级）
  async function copyText(txt, btn) {
    try {
      await navigator.clipboard.writeText(txt);
    } catch (e) {
      const ta = document.createElement("textarea");
      ta.value = txt; ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.select();
      try { document.execCommand("copy"); } catch (e2) {}
      document.body.removeChild(ta);
    }
    if (btn) { const old = btn.textContent; btn.textContent = "已复制"; setTimeout(() => { btn.textContent = old; }, 900); }
  }
  // 事件委托：代码块复制 / 整条消息复制
  log.addEventListener("click", (e) => {
    const cc = e.target.closest(".code-copy");
    if (cc) {
      const code = cc.parentElement.querySelector("pre code");
      if (code) copyText(code.textContent, cc);
      return;
    }
    const mc = e.target.closest(".msg-copy");
    if (mc) {
      const wrap = mc.closest(".msg");
      if (wrap) copyText(wrap.__raw || "", mc);
    }
  });

  // ---------- 登录 ----------
  async function checkAuth() {
    try {
      enterApp(await api("/web/api/me"));
    } catch (e) { if (e.message !== "未登录") showLogin(); }
  }
  function showLogin() {
    loginView.classList.remove("hidden");
    appView.classList.add("hidden");
    if (ws) { try { ws.close(); } catch (e) {} ws = null; }
  }
  function enterApp(j) {
    loginView.classList.add("hidden");
    appView.classList.remove("hidden");
    setModelTag(j.model);
    if (brandVer && j.version) {
      brandVer.textContent = j.version;
      setBaseTitle("开阳 Mizar " + j.version);
    }
    input.focus();
    connectWS();
    startStatusPoll();
    loadSettings(j.settings);
    loadConversations(true);
  }
  function setModelTag(m) { modelTag.textContent = m || ""; }
  loginForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    loginErr.textContent = "";
    loginBtn.disabled = true;
    try {
      // 用户名留空时用服务端默认值 admin：以前占位符写着 "admin" 而实际为空，
      // 用户以为已填好直接输密码 → 必然登录失败。现在留空才走默认，且输入框不再有
      // 易误解的占位符。
      const uname = $("username").value.trim() || "admin";
      await api("/web/api/login", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: uname, password: $("password").value }),
      });
      $("password").value = "";
      enterApp(await api("/web/api/me"));
    } catch (err) {
      loginErr.textContent = err.message || "登录失败";
    } finally { loginBtn.disabled = false; }
  });
  logoutBtn.addEventListener("click", async () => {
    await fetch("/web/api/logout", { method: "POST", credentials: "same-origin" }).catch(() => {});
    showLogin();
  });

  // ---------- 模式切换 ----------
  function setMode(m) {
    mode = m;
    modeChat.classList.toggle("active", m === "chat");
    modeAgent.classList.toggle("active", m === "agent");
    input.placeholder = m === "chat"
      ? "普通对话：直接问答，Enter 发送 / Shift+Enter 换行"
      : "Agent 模式：可调用工具执行任务，Enter 发送 / Shift+Enter 换行";
  }
  modeChat.addEventListener("click", () => setMode("chat"));
  modeAgent.addEventListener("click", () => setMode("agent"));

  // ---------- 会话管理 ----------
  async function loadConversations(autoSelect) {
    try {
      const j = await api("/web/api/conversations");
      convs = j.conversations || [];
    } catch (e) { convs = []; }
    renderConvList();
    if (autoSelect) {
      if (convs.length > 0) selectConv(convs[0].id);
      else createConv();
    }
  }
  function renderConvList() {
    convList.innerHTML = "";
    // 按最后更新时间倒序（服务端已排序，这里再兜一次，避免本地插入新会话后顺序错乱）
    convs.sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0));
    convs.forEach((c) => {
      const el = document.createElement("div");
      el.className = "conv-item" + (c.id === curConv ? " active" : "");
      el.innerHTML = '<div class="row"><span class="title">' + esc(c.title || "新会话") + '</span>' +
        '<button class="del" title="删除">✕</button></div>' +
        '<div class="meta"><span class="time" title="' + esc(c.updated_at || "") + '">' +
        esc(fmtConvTime(c.updated_at)) + '</span>' +
        '<span class="tag">' + (c.mode === "agent" ? "Agent" : "对话") + '</span></div>';
      el.querySelector(".title").addEventListener("click", () => selectConv(c.id));
      el.querySelector(".meta").addEventListener("click", () => selectConv(c.id));
      el.querySelector(".del").addEventListener("click", async (ev) => {
        ev.stopPropagation();
        if (!confirm("删除该会话？")) return;
        try { await api("/web/api/conversations?id=" + encodeURIComponent(c.id), { method: "DELETE" }); } catch (e) {}
        if (c.id === curConv) curConv = null;
        await loadConversations(!curConv);
      });
      convList.appendChild(el);
    });
    applyBrowserTitle();
  }
  async function selectConv(id) {
    curConv = id;
    renderConvList();
    collapseOnMobile();
    clearLog();
    resetTurn();
    renderQueueBar();
    if (askState) closeAsk();
    restorePendingAsks();
    // 中止按钮只反映「当前会话是否正在运行」，切到别的会话时隐藏，避免误以为卡住；
    // busyConv（本标签页发起的运行）或 runningConv（服务端正在跑且属于本会话）都算。
    abortBtn.classList.toggle("hidden", !(busyConv === id || runningConv === id));
    try {
      const j = await api("/web/api/conversation?id=" + encodeURIComponent(id));
      const c = j.conversation;
      const msgs = c.messages || [];
      // 回放历史：连续的用户消息各起一轮；assistant 作为最终答复；
      // 其余（tool/sys/未知）归入当前轮的「工作过程」。
      let curTurn = null;
      msgs.forEach((m) => {
        if (m.role === "user") {
          newTurn();
          addUserMsg(m.content, m.ts);
          curTurn = turnEl;
        } else if (m.role === "assistant") {
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          const textEl = ensureReply(m.ts);
          const wrap = textEl.parentElement.parentElement;
          wrap.__raw = m.content;
          textEl.innerHTML = md(m.content);
        } else {
          // tool / sys / 其他：进答复框步骤容器（累加可见）
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          const sb = ensureSteps();
          const step = document.createElement("div");
          step.className = "pstep";
          step.textContent = compact(m.content, 300);
          sb.appendChild(step);
          bumpProc();
        }
      });
      finalizeTurn();
      setMode(c.mode === "agent" ? "agent" : "chat");
      applyBrowserTitle();
      // 切回本会话：如果该会话仍在后台运行（本标签页发起 busyConv，或服务端正在跑
      // 该会话 runningConv——可能来自别的标签页），把切走期间缓存的事件重放到界面，
      // 否则用户会看到「切回来要等一会才有新内容 / 中间内容缺失」。
      const liveHere = (busyConv === id || runningConv === id);
      if (liveHere) { newTurn(); replayEvtLog(id); }
      // 切回本会话：若服务端空闲且有本会话排队的消息，立即补发（不再需要等下一轮）
      drainPending();
    } catch (e) { addMsg("error", "错误", e.message); }
  }
  // replayEvtLog 把某会话缓存的运行中事件重新渲染到当前 DOM（切回时补齐）。
  // 落盘的历史 + 缓存增量可能重复，这里只重放「历史之后」的增量即可，
  // 因为最后与服务端的完整答复会用 runTurn 的返回值校正。
  function replayEvtLog(convId) {
    const arr = evtLog[convId];
    if (!arr || !arr.length) return;
    arr.forEach((e) => {
      if (e.kind === "tool_call") onToolCall(e.tool, e.args);
      else if (e.kind === "tool_result") onToolResult(e.tool, e.result);
      else if (e.kind === "stream") handleStream(e.step, e.delta, e.skind);
    });
    scrollLog();
  }
  async function createConv() {
    try {
      const j = await api("/web/api/conversations", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: mode }),
      });
      curConv = j.conversation.id;
      await loadConversations(false);
      renderConvList();
      clearLog();
      resetTurn();
      renderQueueBar();
      collapseOnMobile();
      input.focus();
    } catch (e) { addMsg("error", "错误", e.message); }
  }
  function collapseOnMobile() {
    if (pinned) return; // 已锁定常驻，不自动收起
    if (window.matchMedia("(max-width: 720px)").matches) sidebar.classList.add("collapsed");
  }
  // 侧栏开关：主动打开后，点主区空白处自动收起（含桌面端）
  let sidebarToggled = false;
  if (menuBtn) menuBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    if (pinned) return; // 锁定时不允许收起
    const willOpen = sidebar.classList.contains("collapsed");
    sidebar.classList.toggle("collapsed");
    sidebarToggled = willOpen;
  });
  document.addEventListener("click", (e) => {
    if (pinned) return;
    if (!sidebarToggled) return;
    if (sidebar.contains(e.target) || (menuBtn && menuBtn.contains(e.target))) return;
    sidebar.classList.add("collapsed");
    sidebarToggled = false;
  });
  newConvBtn.addEventListener("click", createConv);

  // ---------- 状态栏（对齐 TUI 状态栏） ----------
  // 每 1.5s 拉一次 /web/api/status；运行中加密到 700ms 以内（由服务端 running 决定）。
  let statusTimer = null;
  function startStatusPoll() {
    if (statusTimer) return;
    const tick = async () => {
      try {
        const q = curConv ? ("?id=" + encodeURIComponent(curConv)) : "";
        const j = await api("/web/api/status" + q);
        renderStatus(j);
        // 记录服务端当前运行的会话：用于区分「哪个会话在跑」，并在空闲时补发排队消息
        const rc = (j && j.running) ? (j.running_conv || "") : null;
        if (rc !== runningConv) {
          // 某个会话的运行结束了：清掉它的增量缓存（内容已落盘，切回时从历史加载）
          if (runningConv && runningConv !== rc) evtLog[runningConv] = [];
          runningConv = rc;
          renderQueueBar();
        }
        if (j && !j.running) drainPending();
        // 运行中更快刷新速度/上下文
        statusTimer = setTimeout(tick, j && j.running ? 700 : 1500);
      } catch (e) {
        statusTimer = setTimeout(tick, 3000);
      }
    };
    tick();
  }
  function fmtTok(v) {
    v = v || 0;
    if (v < 1000) return String(v);
    if (v < 1e6) return (v / 1000).toFixed(1) + "k";
    return (v / 1e6).toFixed(1) + "M";
  }
  function renderStatus(j) {
    if (!statusbar) return;
    const seg = [];
    // 项目行（对齐 TUI）：📁 目录 ⎇ 分支 ✱改动 │ MCP 已连/配置
    const pj = j.project || {};
    if (pj.dir || pj.dir_full) {
      let proj = '<span class="seg"><span class="v teal">📁 ' + esc(pj.dir || pj.dir_full) + '</span>';
      if (pj.dir) proj += '<span class="k" title="' + esc(pj.dir_full || "") + '"></span>';
      if (pj.is_git && pj.branch) {
        proj += '<span class="k"> ⎇ </span><span class="v">' + esc(pj.branch) + '</span>';
        if (pj.dirty > 0) proj += '<span class="v yellow"> ✱' + pj.dirty + '</span>';
      }
      const mc = (pj.mcp_conf || 0) > 0 ? ('MCP ' + (pj.mcp_conn || 0) + '/' + pj.mcp_conf)
                                        : 'MCP -';
      proj += '<span class="k"> │ </span><span class="v">' + mc + '</span></span>';
      seg.push(proj);
    }
    const t = j.tokens || {};
    seg.push('<span class="seg"><span class="k">tokens: </span><span class="v">' + fmtTok(t.total) +
      '</span><span class="k">(in:' + fmtTok(t.in) + ' out:' + fmtTok(t.out) + ')</span></span>');
    const ctx = j.context || {};
    if (ctx.limit > 0) {
      const pct = ctx.pct || 0;
      const cls = pct > 80 ? "red" : pct > 60 ? "yellow" : "green";
      seg.push('<span class="seg"><span class="k">上下文: </span><span class="v ' + cls + '">' + pct.toFixed(0) + '%</span></span>');
    }
    const cacheTxt = t.have_cache ? ((t.cache_hit || 0).toFixed(0) + "%") : "-";
    seg.push('<span class="seg"><span class="k">缓存: </span><span class="v">' + cacheTxt + '</span></span>');
    const sp = j.speed || 0;
    seg.push('<span class="seg"><span class="v ' + (sp > 0 ? "yellow" : "") + '">' + (sp > 0 ? sp.toFixed(0) + " tok/s" : ". tok/s") + '</span></span>');
    seg.push('<span class="seg"><span class="v teal">' + esc(j.model || "") + '</span></span>');
    seg.push('<span class="seg"><span class="k">思考: </span><span class="v">' + esc(j.thinking || "auto") + '</span></span>');
    statusbar.innerHTML = seg.join('<span class="sep">|</span>');
  }

  // ---------- WebSocket 事件 ----------
  function connectWS() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(proto + "://" + location.host + "/web/ws");
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type !== "event") return;
      // 会话过滤：服务端事件携带 conversation_id。当前打开的会话与之不符时，
      // 不再直接丢弃 —— 先按会话缓存（A 会话在后台持续输出的内容不能丢），
      // 等用户切回该会话时再从缓存重放。修复「切到 B 期间 A 的内容丢失 / 切回要等一会才出现」。
      const evConv = m.conversation_id == null ? "" : m.conversation_id;
      const forCurrent = (evConv === "" || evConv === curConv);
      if (m.event === "tool_call") {
        pushEvt(evConv, { kind: "tool_call", tool: m.tool, args: m.args });
        if (forCurrent) onToolCall(m.tool, m.args);
      } else if (m.event === "tool_result") {
        pushEvt(evConv, { kind: "tool_result", tool: m.tool, result: m.result });
        if (forCurrent) onToolResult(m.tool, m.result);
      } else if (m.event === "stream") {
        pushEvt(evConv, { kind: "stream", step: m.step, delta: m.delta, skind: m.kind });
        if (forCurrent) handleStream(m.step, m.delta, m.kind);
      } else if (m.event === "ask_user") {
        showAsk(m.id, m.questions, m.conversation_id);
      } else if (m.event === "ask_close") {
        closeAsk(m.id);
      }
    };
    ws.onclose = () => { ws = null; };
    ws.onopen = () => { restorePendingAsks(); };
  }

  // 工具调用：追加到答复框的步骤容器（累加可见）
  function onToolCall(tool, args) {
    if (thinkStepEl) { thinkStepEl.classList.remove("cursor"); thinkStepEl = null; }
    const sb = ensureSteps();
    const a = compact(args, 160);
    const step = document.createElement("div");
    step.className = "pstep pstep-tool";
    step.innerHTML = '<span class="pico">🔧</span><span class="ptool">' + esc(tool) + '</span>' +
      (a ? '<span class="pargs">' + esc(a) + '</span>' : "");
    sb.appendChild(step);
    bumpProc();
    scrollLog();
  }
  // 工具结果：追加到步骤容器（可见）
  function onToolResult(tool, result) {
    const r = compact(result, 200);
    if (!r) return;
    const sb = ensureSteps();
    const step = document.createElement("div");
    step.className = "pstep pstep-res";
    step.innerHTML = '<span class="pico">↳</span><span class="pres">' + esc(r) + '</span>';
    sb.appendChild(step);
    bumpProc();
    scrollLog();
  }

  // 流式：thinking 与 reply 都累加到最终答复框
  function handleStream(step, delta, kind) {
    if (!delta) return;
    if (kind === "thinking") {
      const sb = ensureSteps();
      if (!thinkStepEl) {
        thinkStepEl = document.createElement("div");
        thinkStepEl.className = "pstep pstep-think cursor";
        thinkStepEl.textContent = "";
        thinkStepBuf = "";
        sb.appendChild(thinkStepEl);
        bumpProc();
      }
      thinkStepBuf += delta;
      thinkStepEl.textContent = thinkStepBuf;
      scrollLog();
      return;
    }
    // reply：思考结束
    if (thinkStepEl) { thinkStepEl.classList.remove("cursor"); thinkStepEl = null; }
    const body = ensureReply();
    // 步骤切换（工具调用后模型重新输出）：不同步骤的回复之间补空行分隔，
    // 否则多步正文会首尾相连堆在一起、看不出条目边界。
    if (streamBuf && step !== streamStep) {
      streamBuf = streamBuf.replace(/\s+$/, "") + "\n\n";
    }
    streamStep = step;
    streamBuf += delta;
    body.parentElement.parentElement.__raw = streamBuf;
    body.innerHTML = md(streamBuf);
    body.classList.add("cursor");
    scrollLog();
  }
  function finalizeStream() {
    if (streamBody) streamBody.classList.remove("cursor");
    if (thinkStepEl) thinkStepEl.classList.remove("cursor");
    streamBody = null; streamBuf = ""; thinkStepEl = null; thinkStepBuf = ""; streamStep = -1; stepsBody = null;
  }

  // ---------- ask_user_question 弹窗 ----------
  // 模型调用 ask_user_question / exit_plan_mode 时，服务端广播 ask_user 事件并
  // 阻塞等待；这里弹窗让用户选，选完 POST /web/api/answer 回填。
  // ⚠ 不设超时：用户可能很久才回来看，题目过期比多等更糟。
  const askModal = $("askModal"), askBody = $("askBody"), askSubmit = $("askSubmit"),
    askMsg = $("askMsg"), askTitle = $("askTitle");
  let askState = null;   // { id, questions, picked: {qid: [labels]}, free: {qid: text} }

  function showAsk(id, questions, convId) {
    // 只处理当前会话的问题（切到别的会话时不该弹别人的问题）
    if (convId != null && convId !== "" && convId !== curConv) return;
    // 已有同 id 的弹窗：不重复渲染
    if (askState && askState.id === id) return;
    const qs = Array.isArray(questions) ? questions : [];
    if (!qs.length) return;
    askState = { id: id, questions: qs, picked: {}, free: {} };
    renderAsk();
    askTitle.textContent = qs.length > 1 ? ("需要你的选择（" + qs.length + " 个问题）") : "需要你的选择";
    askMsg.textContent = "";
    askModal.classList.remove("hidden");
  }
  function closeAsk(id) {
    if (!askState) return;
    if (id && askState.id !== id) return;
    askState = null;
    askModal.classList.add("hidden");
  }
  function renderAsk() {
    if (!askState) return;
    const qs = askState.questions;
    let html = "";
    qs.forEach((q, qi) => {
      const qid = q.id || ("q" + (qi + 1));
      const opts = Array.isArray(q.options) ? q.options : [];
      html += '<div class="ask-q" data-qid="' + esc(qid) + '" data-multi="' + (q.multi_select ? "1" : "0") + '">';
      html += '<div class="qhead">' +
        (qs.length > 1 ? '<span class="qidx">' + (qi + 1) + '/' + qs.length + '</span>' : '') +
        esc(q.question || q.header || ("问题 " + (qi + 1))) + '</div>';
      if (q.header && q.question) html += '<div class="qdesc">' + esc(q.header) + '</div>';
      if (opts.length) {
        opts.forEach((o, oi) => {
          const label = o.label || String(oi + 1);
          html += '<label class="ask-opt"><input type="' + (q.multi_select ? "checkbox" : "radio") +
            '" name="ask-' + esc(qid) + '" value="' + esc(label) + '">' +
            '<span class="olabel">' + esc(label) +
            (o.description ? '<div class="odesc">' + esc(o.description) + '</div>' : '') +
            '</span></label>';
        });
      } else {
        // 无预设选项：自由输入
        html += '<input class="ask-free" type="text" placeholder="输入你的回答">';
      }
      html += '</div>';
    });
    askBody.innerHTML = html;
    // 绑定选择事件（不用内联 onclick，避免 CSP/转义问题）
    askBody.querySelectorAll('.ask-q').forEach((qEl) => {
      qEl.querySelectorAll('input[type=radio]').forEach((el) => {
        el.addEventListener('change', () => {
          const qid = qEl.dataset.qid;
          askState.picked[qid] = [el.value];
        });
      });
      qEl.querySelectorAll('input[type=checkbox]').forEach((el) => {
        el.addEventListener('change', () => {
          const qid = qEl.dataset.qid;
          const cur = askState.picked[qid] || [];
          const idx = cur.indexOf(el.value);
          if (el.checked && idx < 0) cur.push(el.value);
          if (!el.checked && idx >= 0) cur.splice(idx, 1);
          askState.picked[qid] = cur;
        });
      });
      const free = qEl.querySelector('.ask-free');
      if (free) {
        free.addEventListener('input', () => { askState.free[qEl.dataset.qid] = free.value; });
      }
    });
  }
  async function submitAsk() {
    if (!askState) return;
    const qs = askState.questions;
    const answers = [];
    for (let qi = 0; qi < qs.length; qi++) {
      const q = qs[qi];
      const qid = q.id || ("q" + (qi + 1));
      const opts = Array.isArray(q.options) ? q.options : [];
      if (opts.length === 0) {
        const txt = (askState.free[qid] || "").trim();
        if (!txt) { askMsg.textContent = "请填写第 " + (qi + 1) + " 个问题的回答"; return; }
        answers.push({ id: qid, value: txt });
        continue;
      }
      const picked = askState.picked[qid] || [];
      if (!picked.length) { askMsg.textContent = "请选择第 " + (qi + 1) + " 个问题的选项"; return; }
      if (q.multi_select) answers.push({ id: qid, value: picked[0], values: picked });
      else answers.push({ id: qid, value: picked[0] });
    }
    askSubmit.disabled = true;
    askMsg.textContent = "提交中…";
    try {
      const j = await api("/web/api/answer", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: askState.id, answers: answers }),
      });
      if (j && j.ok === false) {
        // 已被其他地方作答/或任务已取消：静默关闭，避免卡住界面
        closeAsk();
        flashQuick(j.reason === "already_answered" ? "该问题已被作答" : "问题已失效");
        return;
      }
      closeAsk();
      flashQuick("已提交选择");
    } catch (e) {
      askMsg.textContent = "提交失败: " + e.message;
    } finally {
      askSubmit.disabled = false;
    }
  }
  if (askSubmit) askSubmit.addEventListener("click", submitAsk);
  // Enter 提交（在自由输入框里也允许；Shift+Enter 换行不适用单行输入）
  if (askModal) {
    askModal.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submitAsk(); }
    });
  }
  // 刷新/重连后恢复未作答的问题（服务端保留 pending）
  async function restorePendingAsks() {
    try {
      const j = await api("/web/api/ask");
      const list = (j && j.pending) || [];
      for (const p of list) showAsk(p.id, p.questions, p.conversation_id);
    } catch (e) {}
  }

  // ---------- 发送 ----------
  // 等待期间再发送的处理方式（由发送按钮上方的开关控制）：
  //   - 打断模式（默认）：Agent 模式 → 注入当前循环做实时纠偏（steer）；
  //     普通对话模式无循环可注入，自动降级为排队。
  //   - 排队模式：消息进 pendingQueue，本轮结束后自动补发为新任务。
  // 服务端若已无活跃循环（running=false）也会自动降级为排队，避免消息被静默丢弃。
  //
  // 可见性：无论注入还是排队，都立刻在「排队队列」条里显示一条（含状态徽标），
  // 直到本轮结束。此前这些消息在 WebUI 里完全不可见，用户以为丢了。
  let pendingQueue = [];   // { id, text, conv, kind: "queue"|"steer" }
  let queueSeq = 0;
  function pushQueue(text, conv, kind) {
    const id = ++queueSeq;
    pendingQueue.push({ id: id, text: text, conv: conv, kind: kind || "queue" });
    renderQueueBar();
    return id;
  }
  function setQueueKind(id, kind) {
    const it = pendingQueue.find((x) => x.id === id);
    if (it) { it.kind = kind; renderQueueBar(); }
  }
  function dropQueue(id) {
    pendingQueue = pendingQueue.filter((x) => x.id !== id);
    renderQueueBar();
  }
  function renderQueueBar() {
    if (!queueBar) return;
    const mine = pendingQueue.filter((x) => x.conv === curConv);
    if (!mine.length) { queueBar.classList.add("hidden"); queueBar.innerHTML = ""; return; }
    const nSteer = mine.filter((x) => x.kind === "steer").length;
    const nQueue = mine.length - nSteer;
    let head = [];
    if (nSteer) head.push(nSteer + " 条已注入当前任务");
    if (nQueue) {
      const otherBusy = (busyConv !== null || runningConv !== null) && busyConv !== curConv && runningConv !== curConv;
      head.push(nQueue + " 条" + (otherBusy ? "等待其它会话结束后发送" : "等待本轮结束后发送"));
    }
    let html = '<div class="qhead">待处理消息 · ' + esc(head.join(" · ")) + '</div>';
    mine.forEach((it) => {
      html += '<div class="qitem" data-qid="' + it.id + '">' +
        '<span class="qbadge ' + (it.kind === "steer" ? "steer" : "") + '">' +
        (it.kind === "steer" ? "已注入" : "排队") + '</span>' +
        '<span class="qtext" title="' + esc(it.text) + '">' + esc(it.text) + '</span>' +
        '<button class="qdel" type="button" title="取消这条">✕</button>' +
        '</div>';
    });
    queueBar.innerHTML = html;
    queueBar.classList.remove("hidden");
    queueBar.scrollTop = queueBar.scrollHeight;
  }
  if (queueBar) {
    queueBar.addEventListener("click", (e) => {
      const btn = e.target.closest(".qdel");
      if (!btn) return;
      const item = btn.closest(".qitem");
      if (item) dropQueue(Number(item.dataset.qid));
    });
  }
  async function send() {
    const text = input.value.trim();
    if (!text) return;
    // /stop：立即停止当前任务（TUI 与 WebUI 通用命令，这里直接打 abort 接口）
    if (/^\/(stop|abort|停止|取消)$/i.test(text)) {
      input.value = "";
      autoGrow();
      await requestStop();
      return;
    }
    history.push(text);
    historyIndex = history.length;
    historySaved = "";
    input.value = "";
    autoGrow();
    const convId = curConv;
    // 服务端当前正在跑的会话（可能不是本会话，也可能来自别的浏览器标签）。
    const serverRunning = (busyConv !== null) ? busyConv : runningConv;
    if (serverRunning !== null && serverRunning !== convId) {
      // 别的会话正在占用 Agent（单实例）：本会话消息排到「本会话」队列，
      // 并明确告知用户是谁在跑 —— 不是莫名排队，也不是串台。
      pushQueue(text, convId, "queue");
      flashQuick("其它会话正在运行，已排队，该会话结束后自动发送");
      return;
    }
    if (busyConv !== null) {
      // 本会话正在跑一轮。
      const runningHere = busyConv === convId;
      if (!interruptMode || mode !== "agent" || !runningHere) {
        pushQueue(text, convId, "queue");
        flashQuick("已排队，本轮结束后自动发送");
        return;
      }
      const qid = pushQueue(text, convId, "queue");
      try {
        const j = await api("/web/api/steer", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: text, conversation_id: convId }),
        });
        if (j && j.running === false) { flashQuick("已排队，本轮结束后自动发送"); }
        else { setQueueKind(qid, "steer"); flashQuick("已发送纠正，后台生效"); }
      } catch (e) { flashQuick("已排队，本轮结束后自动发送"); }
      return;
    }
    await runTurn(text);
  }
  // requestStop 停止当前任务：中止后台循环 + 清空本会话的待处理消息。
  async function requestStop() {
    const convId = curConv;
    // 若正弹着问答框，一并关掉（服务端会返回 "[]" 让模型知道用户未作答）
    if (askState) closeAsk();
    // 先把本会话的排队消息清掉（停止后不应再自动补发）
    pendingQueue = pendingQueue.filter((x) => x.conv !== convId);
    renderQueueBar();
    let stopped = false;
    try {
      const j = await api("/web/api/abort", { method: "POST" });
      stopped = !!(j && j.ok);
    } catch (e) {}
    if (stopped && busyConv === convId) flashQuick("已停止当前任务");
    else flashQuick(stopped ? "已发送停止请求（当前无运行任务）" : "停止失败");
  }
  // runTurn 执行一轮完整对话（用户消息 → 最终答复）。
  async function runTurn(text, targetConv) {
    if (targetConv && targetConv !== curConv) curConv = targetConv;
    if (!curConv) await createConv();
    const myConv = curConv;
    newTurn();
    addUserMsg(text);
    evtLog[myConv] = [];   // 新一轮开始：清掉上一轮缓存的增量事件
    busyConv = myConv;
    // 注意：不禁用发送按钮——等待期间用户可继续发送消息（打断/排队）。
    abortBtn.classList.remove("hidden");
    const item = convs.find((c) => c.id === myConv);
    if (item && (item.title === "新会话" || !item.title)) { item.title = text.slice(0, 24); renderConvList(); }

    try {
      const j = await api(mode === "chat" ? "/web/api/chat" : "/web/api/run", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ conversation_id: myConv, message: text }),
      });
      // 用户可能已切换到别的会话：此时当前 DOM 已不属于本轮，跳过渲染（服务端已落盘）。
      if (curConv === myConv) {
        if (j && j.aborted) {
          // 主动停止：不当作错误。保留已流式输出的部分内容，末尾补一条「已停止」提示。
          if (streamBuf) {
            const textEl = streamBody || ensureReply();
            textEl.innerHTML = md(streamBuf) + "\n\n<span class=\"stopped\">⏹ 任务已停止</span>";
          } else {
            const body = ensureReply();
            const wrap = body.parentElement.parentElement;
            wrap.className = "msg sys";
            wrap.__raw = "⏹ 任务已停止";
            body.innerHTML = "⏹ 任务已停止";
          }
        } else {
          const textEl = streamBody || ensureReply();
          const wrap = textEl.parentElement.parentElement;
          wrap.__raw = j.reply || streamBuf || "";
          textEl.innerHTML = md(j.reply || streamBuf || "(空)");
        }
      }
    } catch (e) {
      if (e && e.status === 409) {
        // 服务端仍在跑（别的标签页/竞态）：不当错误展示，改为排队，稍后自动补发。
        if (turnEl) { try { turnEl.remove(); } catch (e2) {} }
        pushQueue(text, myConv, "queue");
        flashQuick("其它会话正在运行，已排队，结束后自动发送");
      } else if (curConv === myConv) {
        const body = ensureReply();
        const wrap = body.parentElement.parentElement;
        wrap.className = "msg error";
        wrap.__raw = "⚠ " + e.message;
        body.innerHTML = md("⚠ " + e.message);
      }
    } finally { finish(myConv); }
  }
  function finish(myConv) {
    if (curConv === myConv) finalizeTurn();
    busyConv = null;
    evtLog[myConv] = [];   // 本轮已结束，增量事件已并入最终答复，清空缓存避免重复渲染
    abortBtn.classList.add("hidden");
    // 本轮结束后清理属于本会话的待处理项：
    //   - "steer"（已注入当前循环）：已被消费或已由服务端落盘，不能再补发（否则重复）；
    //   - "queue"（排队）：本轮结束后补发为新任务。
    const mine = pendingQueue.filter((it) => it.conv === myConv);
    pendingQueue = pendingQueue.filter((it) => it.conv !== myConv);
    const resend = mine.filter((it) => it.kind === "queue");
    // 本会话待发消息放回队首，保持顺序；随后统一 drain（也覆盖别的会话排队的消息）
    if (resend.length) pendingQueue = resend.concat(pendingQueue);
    renderQueueBar();
    input.focus();
    api("/web/api/conversations").then((j) => { convs = j.conversations || []; renderConvList(); }).catch(() => {});
    // 自动标题由服务端异步生成（小模型），稍后再拉一次即可显示新标题
    setTimeout(() => {
      api("/web/api/conversations").then((j) => { convs = j.conversations || []; renderConvList(); }).catch(() => {});
    }, 4000);
    drainPending();
  }
  // drainPending 服务端空闲时补发「当前会话」的排队消息。
  // 只补发当前打开会话的消息：运行 streaming 直接写当前 DOM，后台跑别的会话会串台。
  // 别的会话的排队消息保留在队列条里，等用户切回该会话时再自动发出。
  let draining = false;
  function drainPending() {
    if (draining) return;
    if (busyConv !== null || runningConv !== null) return;
    const items = pendingQueue.filter((it) => it.conv === curConv && it.kind === "queue");
    if (!items.length) return;
    pendingQueue = pendingQueue.filter((it) => !(it.conv === curConv && it.kind === "queue"));
    renderQueueBar();
    draining = true;
    runTurn(items.map((it) => it.text).join("\n"), curConv).finally(() => { draining = false; });
  }
  // 短暂提示（复用快捷栏的 quickMsg 区域）
  let quickTimer = null;
  function flashQuick(msg) {
    if (!quickMsg) return;
    quickMsg.textContent = msg;
    if (quickTimer) clearTimeout(quickTimer);
    quickTimer = setTimeout(() => { quickMsg.textContent = ""; }, 1500);
  }
  sendBtn.addEventListener("click", send);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); return; }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      if (historyIndex > 0) {
        historySaved = input.value;
        historyIndex--;
        input.value = history[historyIndex] || "";
        autoGrow();
      }
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      if (historyIndex < history.length) {
        historyIndex++;
        if (historyIndex >= history.length) input.value = historySaved;
        else input.value = history[historyIndex] || "";
        autoGrow();
      }
    }
  });
  abortBtn.addEventListener("click", requestStop);
  // 打断/排队开关
  if (queueMode) {
    applyQueueToggle();
    queueMode.addEventListener("change", () => {
      interruptMode = queueMode.checked;
      try { localStorage.setItem("mizar-queue-mode", interruptMode ? "0" : "1"); } catch (e) {}
      applyQueueToggle();
    });
  }
  function autoGrow() {
    // 固定 5 行高度（由 CSS min-height 控制），超过则纵向滚动
    input.style.height = "";
  }

  // ---------- 设置面板 ----------
  const setModel = $("setModel"), setThinking = $("setThinking"), setTemp = $("setTemp"),
    setTopp = $("setTopp"), setMaxTokens = $("setMaxTokens"), setSystem = $("setSystem"),
    setMaxHistory = $("setMaxHistory"), setTitleModel = $("setTitleModel"),
    tempVal = $("tempVal"), toppVal = $("toppVal"), modelList = $("modelList"), modelHint = $("modelHint");
  let curSettings = {};
  let tempTouched = false, toppTouched = false;

  function loadSettings(s) {
    curSettings = s || {};
    setModel.value = curSettings.model || "";
    setThinking.value = curSettings.thinking || "auto";
    setMaxTokens.value = curSettings.max_tokens || "";
    setMaxHistory.value = curSettings.max_history || 50;
    setTitleModel.value = curSettings.title_model || "";
    setSystem.value = curSettings.system || "";
    // 快捷行同步
    qkModel.value = curSettings.model || "";
    qkThinking.value = curSettings.thinking || "auto";
    tempTouched = curSettings.temperature != null;
    toppTouched = curSettings.top_p != null;
    if (tempTouched) { setTemp.value = curSettings.temperature; tempVal.textContent = curSettings.temperature; }
    else { setTemp.value = 0.7; tempVal.textContent = "默认"; }
    if (toppTouched) { setTopp.value = curSettings.top_p; toppVal.textContent = curSettings.top_p; }
    else { setTopp.value = 1; toppVal.textContent = "默认"; }
  }
  function openSettings() {
    api("/web/api/settings").then((j) => { loadSettings(j.settings); setModelTag(j.model); }).catch(() => {});
    loadProviders();
    settingsModal.classList.remove("hidden");
  }
  settingsBtn.addEventListener("click", openSettings);
  settingsClose.addEventListener("click", () => settingsModal.classList.add("hidden"));
  settingsModal.addEventListener("click", (e) => { if (e.target === settingsModal) settingsModal.classList.add("hidden"); });
  setTemp.addEventListener("input", () => { tempTouched = true; tempVal.textContent = setTemp.value; });
  setTopp.addEventListener("input", () => { toppTouched = true; toppVal.textContent = setTopp.value; });
  $("tempClear").addEventListener("click", () => { tempTouched = false; setTemp.value = 0.7; tempVal.textContent = "默认"; });
  $("toppClear").addEventListener("click", () => { toppTouched = false; setTopp.value = 1; toppVal.textContent = "默认"; });

  refreshModels.addEventListener("click", async () => {
    modelHint.textContent = "拉取中…";
    try {
      const j = await api("/web/api/models");
      modelList.innerHTML = "";
      (j.models || []).forEach((m) => { const o = document.createElement("option"); o.value = m; modelList.appendChild(o); });
      modelHint.textContent = j.error ? ("拉取失败：" + j.error) : ("共 " + (j.models || []).length + " 个模型，可下拉选择");
    } catch (e) { modelHint.textContent = "拉取失败：" + e.message; }
  });

  // 保存设置（合并：保留未在本次表单中出现的字段）
  async function saveSettings(patch, okMsg) {
    const v = Object.assign({}, curSettings, patch);
    const j = await api("/web/api/settings", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(v),
    });
    curSettings = j.settings || v;
    setModelTag(j.model);
    return j;
  }
  settingsSave.addEventListener("click", async () => {
    const v = {
      model: setModel.value.trim(),
      thinking: setThinking.value,
      max_tokens: parseInt(setMaxTokens.value || "0", 10) || 0,
      max_history: parseInt(setMaxHistory.value || "50", 10) || 50,
      title_model: setTitleModel.value.trim(),
      system: setSystem.value,
    };
    if (tempTouched) v.temperature = parseFloat(setTemp.value);
    if (toppTouched) v.top_p = parseFloat(setTopp.value);
    settingsMsg.textContent = "保存中…";
    try {
      await saveSettings(v);
      // 同步快捷行
      qkModel.value = curSettings.model || "";
      qkThinking.value = curSettings.thinking || "auto";
      settingsMsg.textContent = "✓ 已保存并应用";
      setTimeout(() => settingsModal.classList.add("hidden"), 600);
    } catch (e) { settingsMsg.textContent = "✗ " + e.message; }
  });

  // 快捷行：模型 + 思考等级 即时保存
  async function quickSave(patch) {
    quickMsg.textContent = "保存中…";
    try {
      await saveSettings(patch);
      setModel.value = curSettings.model || "";
      setThinking.value = curSettings.thinking || "auto";
      quickMsg.textContent = "✓ 已应用";
      setTimeout(() => { quickMsg.textContent = ""; }, 1200);
    } catch (e) { quickMsg.textContent = "✗ " + e.message; }
  }
  qkThinking.addEventListener("change", () => quickSave({ thinking: qkThinking.value }));
  qkModel.addEventListener("change", () => quickSave({ model: qkModel.value.trim() }));

  // ---------- 供应商收藏夹 ----------
  async function loadProviders() {
    try {
      const j = await api("/web/api/providers");
      renderProviders(j);
    } catch (e) { if (providerHint) providerHint.textContent = "加载失败：" + e.message; }
  }
  function renderProviders(j) {
    if (!providerSelect) return;
    const list = j.providers || [];
    providerSelect.innerHTML = "";
    const opt0 = document.createElement("option");
    opt0.value = "";
    opt0.textContent = list.length ? "— 选择供应商 —" : "（暂无收藏，请在下方添加）";
    providerSelect.appendChild(opt0);
    list.forEach((p) => {
      const o = document.createElement("option");
      o.value = p.name;
      o.textContent = p.name + "  (" + p.base_url + ")";
      o.dataset.url = p.base_url;
      o.dataset.model = p.model || "";
      providerSelect.appendChild(o);
    });
    if (j.active) providerSelect.value = j.active;
    else if (j.current) {
      // 未收藏当前供应商时，提示可保存
    }
    if (providerCurrent) {
      const cur = j.current || {};
      providerCurrent.textContent = "当前生效：" + (cur.base_url || "—") + (cur.model ? "  ·  " + cur.model : "");
    }
    if (providerHint) providerHint.textContent = list.length ? ("共 " + list.length + " 个收藏") : "";
  }
  async function providerAction(body, okMsg) {
    if (providerHint) providerHint.textContent = "处理中…";
    try {
      const j = await api("/web/api/providers", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      renderProviders(j);
      if (providerHint) providerHint.textContent = okMsg;
      // 同步模型显示
      if (j.current && j.current.model) { setModelTag(j.current.model); qkModel.value = j.current.model; setModel.value = j.current.model; }
      return j;
    } catch (e) {
      if (providerHint) providerHint.textContent = "✗ " + e.message;
      return null;
    }
  }
  if (providerAdd) providerAdd.addEventListener("click", async () => {
    const name = pvName.value.trim(), url = pvURL.value.trim();
    if (!name || !url) { providerHint.textContent = "名称与 baseURL 必填"; return; }
    const j = await providerAction({ action: "add", name, base_url: url, api_key: pvKey.value.trim(), model: pvModel.value.trim() }, "✓ 已收藏");
    if (j) { pvKey.value = ""; pvName.value = ""; pvURL.value = ""; pvModel.value = ""; }
  });
  if (providerUse) providerUse.addEventListener("click", async () => {
    const name = providerSelect.value;
    if (!name) { providerHint.textContent = "请先选择一个供应商"; return; }
    await providerAction({ action: "use", name }, "✓ 已切换到 " + name);
  });
  if (providerDel) providerDel.addEventListener("click", async () => {
    const name = providerSelect.value;
    if (!name) { providerHint.textContent = "请先选择一个供应商"; return; }
    if (!confirm("删除供应商 " + name + "？（不影响当前生效配置）")) return;
    await providerAction({ action: "del", name }, "✓ 已删除 " + name);
  });
  if (providerSelect) providerSelect.addEventListener("change", () => {
    const o = providerSelect.selectedOptions[0];
    if (o && o.dataset.url) {
      pvName.value = o.value;
      pvURL.value = o.dataset.url;
      pvModel.value = o.dataset.model || "";
      pvKey.value = "";
    }
  });

  // ---------- 启动 ----------
  // 登录页也显示版本号（无需鉴权）
  fetch("/web/api/version", { credentials: "same-origin" })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => {
      if (!j || !j.version) return;
      setBaseTitle("开阳 Mizar " + j.version);
      const sub = document.querySelector(".login-card .sub");
      if (sub) sub.textContent = "Agent Web UI · " + j.version;
    })
    .catch(() => {});
  setMode("chat");
  checkAuth();
})();
