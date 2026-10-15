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
  let thinkingEl = null;   // 当前思考块
  let thinkingBuf = "";    // 思考累计
  let streamStep = -1;     // 当前流式 step

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
    log.appendChild(wrap);
    log.scrollTop = log.scrollHeight;
    return body;
  }
  function clearLog() { log.innerHTML = ""; }

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
    try {
      const j = await api("/web/api/conversation?id=" + encodeURIComponent(id));
      const c = j.conversation;
      (c.messages || []).forEach((m) => addMsg(m.role === "user" ? "user" : "assistant",
        m.role === "user" ? "我" : "开阳", m.content));
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
        finalizeStream();
        addMsg("tool", "工具", "→ " + m.tool + "(" + (m.args || "") + ")");
      } else if (m.event === "tool_result") {
        addMsg("tool", "结果", (m.result || "").slice(0, 2000));
      } else if (m.event === "stream") {
        handleStream(m.step, m.delta, m.kind);
      }
    };
    ws.onclose = () => { ws = null; };
  }

  // 流式：reply 进正文，thinking 进独立思考块（不再持续闪烁光标）
  function handleStream(step, delta, kind) {
    if (!delta) return;
    if (kind === "thinking") {
      if (!thinkingEl) {
        thinkingEl = addMsg("sys", "思考", "");
        thinkingBuf = "";
        thinkingEl.classList.add("cursor");
      }
      thinkingBuf += delta;
      thinkingEl.textContent = thinkingBuf;
      log.scrollTop = log.scrollHeight;
      return;
    }
    // reply：一旦正文开始，思考块结束（移除闪烁光标）
    if (thinkingEl) { thinkingEl.classList.remove("cursor"); }
    if (step !== streamStep) {
      streamStep = step;
      streamBody = addMsg("assistant", "开阳", "");
      streamBuf = "";
    }
    if (!streamBody) { streamBody = addMsg("assistant", "开阳", ""); streamBuf = ""; }
    streamBuf += delta;
    streamBody.__raw = streamBuf;
    streamBody.innerHTML = md(streamBuf);
    streamBody.classList.add("cursor");
    log.scrollTop = log.scrollHeight;
  }
  function finalizeStream() {
    if (streamBody) streamBody.classList.remove("cursor");
    if (thinkingEl) thinkingEl.classList.remove("cursor");
    streamBody = null; streamBuf = ""; thinkingEl = null; thinkingBuf = ""; streamStep = -1;
  }

  // ---------- 发送 ----------
  async function send() {
    const text = input.value.trim();
    if (!text || busy) return;
    if (!curConv) await createConv();
    addMsg("user", "我", text);
    input.value = "";
    autoGrow();
    busy = true; sendBtn.disabled = true;
    abortBtn.classList.remove("hidden");
    finalizeStream();
    const item = convs.find((c) => c.id === curConv);
    if (item && (item.title === "新会话" || !item.title)) { item.title = text.slice(0, 24); renderConvList(); }

    try {
      const j = await api(mode === "chat" ? "/web/api/chat" : "/web/api/run", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ conversation_id: curConv, message: text }),
      });
      if (!streamBuf) {
        if (streamBody) { streamBody.__raw = j.reply || ""; streamBody.innerHTML = md(j.reply || "(空)"); }
        else addMsg("assistant", "开阳", j.reply || "(空)");
      } else if (streamBody) {
        streamBody.__raw = j.reply || streamBuf;
        streamBody.innerHTML = md(j.reply || streamBuf);
      }
    } catch (e) {
      if (streamBody) { streamBody.parentElement.className = "msg error"; streamBody.innerHTML = md("⚠ " + e.message); }
      else addMsg("error", "错误", e.message);
    } finally { finish(); }
  }
  function finish() {
    finalizeStream();
    busy = false; sendBtn.disabled = false;
    abortBtn.classList.add("hidden");
    log.scrollTop = log.scrollHeight;
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
