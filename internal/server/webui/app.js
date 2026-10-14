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
  const settingsBtn = $("settingsBtn"), settingsModal = $("settings"), settingsClose = $("settingsClose");
  const settingsSave = $("settingsSave"), settingsMsg = $("settingsMsg"), refreshModels = $("refreshModels");

  let mode = "chat";   // chat | agent
  let busy = false;
  let ws = null;
  let curConv = null;  // 当前会话 id
  let convs = [];      // 会话摘要列表
  let streamBody = null;   // 当前流式渲染的目标消息体
  let streamBuf = "";      // 当前流式累计文本
  let thinkingBuf = "";    // 当前思考过程累计
  let streamStep = -1;     // 当前流式 step

  // ---------- 工具 ----------
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  // 简易 markdown：代码块 + 行内代码
  function md(s) {
    let t = esc(s);
    t = t.replace(/```(\w*)\n?([\s\S]*?)```/g, (m, l, c) => "<pre>" + c.replace(/\n$/, "") + "</pre>");
    t = t.replace(/`([^`]+)`/g, "<code>$1</code>");
    return t;
  }
  async function api(path, opts) {
    const r = await fetch(path, Object.assign({ credentials: "same-origin" }, opts || {}));
    let j = null;
    try { j = await r.json(); } catch (e) {}
    if (r.status === 401) { showLogin(); throw new Error("未登录"); }
    if (!r.ok) throw new Error((j && j.error && j.error.message) || ("HTTP " + r.status));
    return j;
  }
  function addMsg(kind, who, text) {
    const wrap = document.createElement("div");
    wrap.className = "msg " + kind;
    wrap.innerHTML = '<div class="who">' + esc(who) + '</div><div class="body"></div>';
    const body = wrap.querySelector(".body");
    if (kind === "assistant" || kind === "error") body.innerHTML = md(text);
    else body.textContent = text;
    log.appendChild(wrap);
    log.scrollTop = log.scrollHeight;
    return body;
  }
  function clearLog() { log.innerHTML = ""; }

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
      ? "普通对话：直接问答，Enter 发送"
      : "Agent 模式：可调用工具执行任务，Enter 发送";
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
    if (window.matchMedia("(max-width: 720px)").matches) sidebar.classList.add("collapsed");
  }
  // 侧栏开关：主动打开后，点主区空白处自动收起（含桌面端）
  let sidebarToggled = false;
  if (menuBtn) menuBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    const willOpen = sidebar.classList.contains("collapsed");
    sidebar.classList.toggle("collapsed");
    sidebarToggled = willOpen; // 打开状态才标记为"用户主动打开"
  });
  document.addEventListener("click", (e) => {
    if (!sidebarToggled) return;
    if (sidebar.contains(e.target) || (menuBtn && menuBtn.contains(e.target))) return;
    sidebar.classList.add("collapsed");
    sidebarToggled = false;
  });
  newConvBtn.addEventListener("click", createConv);

  // ---------- WebSocket 事件（Agent 模式实时输出） ----------
  function connectWS() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(proto + "://" + location.host + "/web/ws");
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type !== "event") return;
      if (m.event === "tool_call") {
        // 工具调用前先把已流式的正文收尾（新一段回复要另起）
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

  // 流式：按 step 分段渲染。reply 进正文，thinking 进灰色思考块。
  function handleStream(step, delta, kind) {
    if (!delta) return;
    if (kind === "thinking") {
      if (!thinkingBuf) {
        streamBody = addMsg("sys", "思考", "");
        thinkingBuf = "";
      }
      thinkingBuf += delta;
      if (streamBody) { streamBody.textContent = thinkingBuf; streamBody.classList.add("cursor"); }
      log.scrollTop = log.scrollHeight;
      return;
    }
    // reply：step 变化或从思考切回正文 → 新起一段
    if (step !== streamStep || (streamBody && streamBody.parentElement.classList.contains("sys"))) {
      streamStep = step;
      streamBody = addMsg("assistant", "开阳", "");
      streamBuf = "";
    }
    if (!streamBody) { streamBody = addMsg("assistant", "开阳", ""); streamBuf = ""; }
    streamBuf += delta;
    streamBody.innerHTML = md(streamBuf);
    streamBody.classList.add("cursor");
    log.scrollTop = log.scrollHeight;
  }
  function finalizeStream() {
    if (streamBody) { streamBody.classList.remove("cursor"); }
    streamBody = null; streamBuf = ""; thinkingBuf = ""; streamStep = -1;
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
    // 本地即时更新标题
    const item = convs.find((c) => c.id === curConv);
    if (item && (item.title === "新会话" || !item.title)) { item.title = text.slice(0, 24); renderConvList(); }

    try {
      const j = await api(mode === "chat" ? "/web/api/chat" : "/web/api/run", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ conversation_id: curConv, message: text }),
      });
      // 流式已渲染则不再重复追加；否则（无流式，如降级路径）用完整回复补上
      if (!streamBuf) {
        if (streamBody) streamBody.innerHTML = md(j.reply || "(空)");
        else addMsg("assistant", "开阳", j.reply || "(空)");
      } else if (streamBody) {
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
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 180) + "px";
  }
  input.addEventListener("input", autoGrow);

  // ---------- 设置面板 ----------
  const setModel = $("setModel"), setThinking = $("setThinking"), setTemp = $("setTemp"),
    setTopp = $("setTopp"), setMaxTokens = $("setMaxTokens"), setSystem = $("setSystem"),
    tempVal = $("tempVal"), toppVal = $("toppVal"), modelList = $("modelList"), modelHint = $("modelHint");
  let curSettings = {};
  let tempTouched = false, toppTouched = false;

  function loadSettings(s) {
    curSettings = s || {};
    setModel.value = curSettings.model || "";
    setThinking.value = curSettings.thinking || "auto";
    setMaxTokens.value = curSettings.max_tokens || "";
    setSystem.value = curSettings.system || "";
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

  settingsSave.addEventListener("click", async () => {
    const v = {
      model: setModel.value.trim(),
      thinking: setThinking.value,
      max_tokens: parseInt(setMaxTokens.value || "0", 10) || 0,
      system: setSystem.value,
    };
    if (tempTouched) v.temperature = parseFloat(setTemp.value);
    if (toppTouched) v.top_p = parseFloat(setTopp.value);
    settingsMsg.textContent = "保存中…";
    try {
      const j = await api("/web/api/settings", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(v),
      });
      curSettings = j.settings || v;
      setModelTag(j.model);
      settingsMsg.textContent = "✓ 已保存并应用";
      setTimeout(() => settingsModal.classList.add("hidden"), 600);
    } catch (e) { settingsMsg.textContent = "✗ " + e.message; }
  });

  // ---------- 启动 ----------
  setMode("chat");
  checkAuth();
})();
