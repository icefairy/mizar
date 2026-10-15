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

  let mode = "chat";   // chat | agent
  let busy = false;
  let ws = null;
  let curConv = null;  // 当前会话 id
  let convs = [];      // 会话摘要列表
  let streamBody = null;   // 当前流式正文目标
  let streamBuf = "";      // 流式正文累计
  let streamStep = -1;     // 当前流式 step
  // 当前轮（turn）：用户提问 → 工作过程（折叠）→ 最终答复
  let turnEl = null;       // 当前轮容器
  let procEl = null;       // 当前轮「工作过程」<details>
  let procBody = null;     // 工作过程内容区
  let procCount = 0;       // 步骤计数
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
  // 行内 markdown（输入已转义的纯文本）
  function inline(t) {
    let x = esc(t);
    x = x.replace(/`([^`]+)`/g, "<code>$1</code>");
    x = x.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    x = x.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
    x = x.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    x = x.replace(/\n/g, "<br>");
    return x;
  }
  // markdown：各级标题 / 代码块（带复制）/ 列表 / 引用 / 行内
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
      // 无序列表
      if (/^\s*[-*+]\s+/.test(line)) {
        const buf = [];
        while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) { buf.push(lines[i].replace(/^\s*[-*+]\s+/, "")); i++; }
        html += "<ul>" + buf.map((x) => "<li>" + inline(x) + "</li>").join("") + "</ul>";
        continue;
      }
      // 有序列表
      if (/^\s*\d+\.\s+/.test(line)) {
        const buf = [];
        while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) { buf.push(lines[i].replace(/^\s*\d+\.\s+/, "")); i++; }
        html += "<ol>" + buf.map((x) => "<li>" + inline(x) + "</li>").join("") + "</ol>";
        continue;
      }
      // 空行
      if (/^\s*$/.test(line)) { i++; continue; }
      // 段落
      const buf = [line]; i++;
      while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^\s*```/.test(lines[i]) &&
        !/^#{1,6}\s/.test(lines[i]) && !/^\s*[-*+]\s+/.test(lines[i]) &&
        !/^\s*\d+\.\s+/.test(lines[i]) && !/^\s*>\s?/.test(lines[i])) { buf.push(lines[i]); i++; }
      html += "<p>" + inline(buf.join("\n")) + "</p>";
    }
    return html;
  }

  async function api(path, opts) {
    const r = await fetch(path, Object.assign({ credentials: "same-origin" }, opts || {}));
    let j = null;
    try { j = await r.json(); } catch (e) {}
    if (r.status === 401) { showLogin(); throw new Error("未登录"); }
    if (!r.ok) throw new Error((j && j.error && j.error.message) || ("HTTP " + r.status));
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
    turnEl = null; procEl = null; procBody = null; procCount = 0;
    thinkStepEl = null; thinkStepBuf = "";
    streamBody = null; streamBuf = ""; streamStep = -1;
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
  function addUserMsg(text) {
    const wrap = document.createElement("div");
    wrap.className = "msg user";
    wrap.__raw = text == null ? "" : String(text);
    wrap.innerHTML = '<div class="who">我</div><div class="body"></div>' +
      '<div class="msg-actions"><button class="act msg-copy" type="button">复制</button></div>';
    wrap.querySelector(".body").textContent = wrap.__raw;
    turnHost().appendChild(wrap);
    scrollLog();
    return wrap;
  }
  // 确保「工作过程」分组存在（默认折叠）
  function ensureProc() {
    if (procBody) return procBody;
    procEl = document.createElement("details");
    procEl.className = "proc";
    procEl.innerHTML = '<summary><span class="proc-caret">▸</span>' +
      '<span class="proc-title">工作过程</span>' +
      '<span class="proc-count">0 步</span></summary>' +
      '<div class="proc-body"></div>';
    procBody = procEl.querySelector(".proc-body");
    turnHost().appendChild(procEl);
    return procBody;
  }
  function bumpProc() {
    procCount++;
    const c = procEl && procEl.querySelector(".proc-count");
    if (c) c.textContent = procCount + " 步";
  }
  // 确保最终答复气泡存在
  function ensureReply() {
    if (streamBody) return streamBody;
    const wrap = document.createElement("div");
    wrap.className = "msg assistant";
    wrap.__raw = "";
    wrap.innerHTML = '<div class="who">开阳</div><div class="body"></div>' +
      '<div class="msg-actions"><button class="act msg-copy" type="button">复制</button></div>';
    turnHost().appendChild(wrap);
    streamBody = wrap.querySelector(".body");
    streamBuf = "";
    return streamBody;
  }
  // 把当前流式正文（中间文本）收进工作过程
  function stashStreamIntoProc() {
    if (!streamBody) return;
    const wrap = streamBody.parentElement;
    if (wrap && wrap.parentElement === turnHost()) {
      wrap.classList.remove("cursor");
      const txt = streamBuf || wrap.__raw || "";
      if (txt.trim()) {
        const pb = ensureProc();
        const step = document.createElement("div");
        step.className = "pstep pstep-text";
        step.innerHTML = md(txt);
        pb.appendChild(step);
        bumpProc();
      }
      wrap.remove();
    }
    streamBody = null; streamBuf = "";
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
    if (procEl) {
      const empty = procBody && !procBody.hasChildNodes();
      if (empty) { procEl.remove(); }
      else if (streamBody && streamBody.parentElement === turnHost()) {
        turnHost().insertBefore(procEl, streamBody.parentElement);
      }
      procEl.querySelector(".proc-caret") && (procEl.querySelector(".proc-caret").textContent = "▸");
    }
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
    input.focus();
    connectWS();
    loadSettings(j.settings);
    loadConversations(true);
  }
  function setModelTag(m) { modelTag.textContent = m || ""; }
  loginForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    loginErr.textContent = "";
    loginBtn.disabled = true;
    try {
      await api("/web/api/login", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: $("username").value, password: $("password").value }),
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
    convs.forEach((c) => {
      const el = document.createElement("div");
      el.className = "conv-item" + (c.id === curConv ? " active" : "");
      el.innerHTML = '<span class="title">' + esc(c.title || "新会话") + '</span>' +
        '<span class="tag">' + (c.mode === "agent" ? "Agent" : "对话") + '</span>' +
        '<button class="del" title="删除">✕</button>';
      el.querySelector(".title").addEventListener("click", () => selectConv(c.id));
      el.querySelector(".tag").addEventListener("click", () => selectConv(c.id));
      el.querySelector(".del").addEventListener("click", async (ev) => {
        ev.stopPropagation();
        if (!confirm("删除该会话？")) return;
        try { await api("/web/api/conversations?id=" + encodeURIComponent(c.id), { method: "DELETE" }); } catch (e) {}
        if (c.id === curConv) curConv = null;
        await loadConversations(!curConv);
      });
      convList.appendChild(el);
    });
  }
  async function selectConv(id) {
    curConv = id;
    renderConvList();
    collapseOnMobile();
    clearLog();
    resetTurn();
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
          addUserMsg(m.content);
          curTurn = turnEl;
        } else if (m.role === "assistant") {
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          addMsg("assistant", "开阳", m.content);
        } else {
          // tool / sys / 其他：进工作过程（折叠）
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          const pb = ensureProc();
          const step = document.createElement("div");
          step.className = "pstep";
          step.textContent = compact(m.content, 300);
          pb.appendChild(step);
          bumpProc();
        }
      });
      finalizeTurn();
      setMode(c.mode === "agent" ? "agent" : "chat");
    } catch (e) { addMsg("error", "错误", e.message); }
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

  // ---------- WebSocket 事件 ----------
  function connectWS() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(proto + "://" + location.host + "/web/ws");
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type !== "event") return;
      if (m.event === "tool_call") {
        onToolCall(m.tool, m.args);
      } else if (m.event === "tool_result") {
        onToolResult(m.tool, m.result);
      } else if (m.event === "stream") {
        handleStream(m.step, m.delta, m.kind);
      }
    };
    ws.onclose = () => { ws = null; };
  }

  // 工具调用：先收尾正在流式的正文（它是中间文本），再往「工作过程」加一行紧凑条目
  function onToolCall(tool, args) {
    stashStreamIntoProc();
    if (thinkStepEl) { thinkStepEl.classList.remove("cursor"); thinkStepEl = null; }
    const pb = ensureProc();
    const step = document.createElement("div");
    step.className = "pstep pstep-tool";
    const a = compact(args, 160);
    step.innerHTML = '<span class="pico">🔧</span><span class="ptool">' + esc(tool) + '</span>' +
      (a ? '<span class="pargs">' + esc(a) + '</span>' : "");
    pb.appendChild(step);
    bumpProc();
    scrollLog();
  }
  // 工具结果：紧凑单行预览，挂在最近一次工具调用下方（空结果不再产生空气泡）
  function onToolResult(tool, result) {
    const r = compact(result, 200);
    if (!r) return; // 空白结果直接丢弃
    const pb = ensureProc();
    const step = document.createElement("div");
    step.className = "pstep pstep-res";
    step.innerHTML = '<span class="pico">↳</span><span class="pres">' + esc(r) + '</span>';
    pb.appendChild(step);
    bumpProc();
    scrollLog();
  }

  // 流式：thinking 进「工作过程」；正文进最终答复气泡（可被 stash 回过程）
  function handleStream(step, delta, kind) {
    if (!delta) return;
    if (kind === "thinking") {
      if (!thinkStepEl) {
        const pb = ensureProc();
        thinkStepEl = document.createElement("div");
        thinkStepEl.className = "pstep pstep-think cursor";
        thinkStepEl.textContent = "";
        pb.appendChild(thinkStepEl);
        thinkStepBuf = "";
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
    streamBuf += delta;
    body.__raw = streamBuf;
    body.innerHTML = md(streamBuf);
    body.classList.add("cursor");
    scrollLog();
  }
  function finalizeStream() {
    if (streamBody) streamBody.classList.remove("cursor");
    if (thinkStepEl) thinkStepEl.classList.remove("cursor");
    streamBody = null; streamBuf = ""; thinkStepEl = null; thinkStepBuf = ""; streamStep = -1;
  }

  // ---------- 发送 ----------
  async function send() {
    const text = input.value.trim();
    if (!text || busy) return;
    if (!curConv) await createConv();
    newTurn();
    addUserMsg(text);
    input.value = "";
    autoGrow();
    busy = true; sendBtn.disabled = true;
    abortBtn.classList.remove("hidden");
    const item = convs.find((c) => c.id === curConv);
    if (item && (item.title === "新会话" || !item.title)) { item.title = text.slice(0, 24); renderConvList(); }

    try {
      const j = await api(mode === "chat" ? "/web/api/chat" : "/web/api/run", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ conversation_id: curConv, message: text }),
      });
      // 用最终答复覆盖当前答复气泡（流式已渲染则替换为完整文本；否则补渲染）
      const body = ensureReply();
      body.__raw = j.reply || streamBuf || "";
      body.innerHTML = md(j.reply || streamBuf || "(空)");
    } catch (e) {
      const body = ensureReply();
      const wrap = body.parentElement;
      wrap.className = "msg error";
      body.__raw = "⚠ " + e.message;
      body.innerHTML = md("⚠ " + e.message);
    } finally { finish(); }
  }
  function finish() {
    finalizeTurn();
    busy = false; sendBtn.disabled = false;
    abortBtn.classList.add("hidden");
    input.focus();
    api("/web/api/conversations").then((j) => { convs = j.conversations || []; renderConvList(); }).catch(() => {});
  }
  sendBtn.addEventListener("click", send);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
  });
  abortBtn.addEventListener("click", async () => {
    await fetch("/web/api/abort", { method: "POST", credentials: "same-origin" }).catch(() => {});
  });
  function autoGrow() {
    // 固定 5 行高度（由 CSS min-height 控制），超过则纵向滚动
    input.style.height = "";
  }

  // ---------- 设置面板 ----------
  const setModel = $("setModel"), setThinking = $("setThinking"), setTemp = $("setTemp"),
    setTopp = $("setTopp"), setMaxTokens = $("setMaxTokens"), setSystem = $("setSystem"),
    setMaxHistory = $("setMaxHistory"),
    tempVal = $("tempVal"), toppVal = $("toppVal"), modelList = $("modelList"), modelHint = $("modelHint");
  let curSettings = {};
  let tempTouched = false, toppTouched = false;

  function loadSettings(s) {
    curSettings = s || {};
    setModel.value = curSettings.model || "";
    setThinking.value = curSettings.thinking || "auto";
    setMaxTokens.value = curSettings.max_tokens || "";
    setMaxHistory.value = curSettings.max_history || 50;
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

  // ---------- 启动 ----------
  setMode("chat");
  checkAuth();
})();
