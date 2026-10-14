"use strict";
// 开阳 Mizar Web UI —— 纯原生 JS，无外部依赖。
(function () {
  const $ = (id) => document.getElementById(id);
  const loginView = $("login"), appView = $("app");
  const log = $("log"), input = $("input"), sendBtn = $("sendBtn");
  const loginForm = $("loginForm"), loginBtn = $("loginBtn"), loginErr = $("loginErr");
  const modeChat = $("modeChat"), modeAgent = $("modeAgent");
  const abortBtn = $("abortBtn"), logoutBtn = $("logoutBtn"), modelTag = $("modelTag");

  let mode = "chat"; // chat | agent
  let busy = false;
  let ws = null;

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

  // ---------- 登录 ----------
  async function checkAuth() {
    try {
      const r = await fetch("/web/api/me", { credentials: "same-origin" });
      if (r.ok) {
        const j = await r.json();
        enterApp(j);
        return;
      }
    } catch (e) {}
    showLogin();
  }
  function showLogin() {
    loginView.classList.remove("hidden");
    appView.classList.add("hidden");
    if (ws) { try { ws.close(); } catch (e) {} ws = null; }
  }
  function enterApp(j) {
    loginView.classList.add("hidden");
    appView.classList.remove("hidden");
    modelTag.textContent = j.model || "";
    input.focus();
    connectWS();
  }
  loginForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    loginErr.textContent = "";
    loginBtn.disabled = true;
    try {
      const r = await fetch("/web/api/login", {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: $("username").value, password: $("password").value }),
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { loginErr.textContent = j.error && j.error.message ? j.error.message : "登录失败"; return; }
      $("password").value = "";
      const me = await fetch("/web/api/me", { credentials: "same-origin" }).then((x) => x.json());
      enterApp(me);
    } catch (err) {
      loginErr.textContent = "网络错误：" + err;
    } finally {
      loginBtn.disabled = false;
    }
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

  // ---------- WebSocket 事件（Agent 模式实时输出） ----------
  function connectWS() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(proto + "://" + location.host + "/web/ws");
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === "event") {
        if (m.event === "tool_call") addMsg("tool", "工具", "→ " + m.tool + "(" + (m.args || "") + ")");
        else if (m.event === "tool_result") addMsg("tool", "结果", (m.result || "").slice(0, 2000));
      }
    };
    ws.onclose = () => { ws = null; };
  }

  // ---------- 发送 ----------
  async function send() {
    const text = input.value.trim();
    if (!text || busy) return;
    addMsg("user", "我", text);
    input.value = "";
    autoGrow();
    busy = true; sendBtn.disabled = true;
    abortBtn.classList.remove("hidden");

    if (mode === "chat") {
      const body = addMsg("assistant", "开阳", "…");
      body.classList.add("cursor");
      try {
        const r = await fetch("/web/api/chat", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ message: text }),
        });
        const j = await r.json().catch(() => ({}));
        body.classList.remove("cursor");
        if (!r.ok) { body.innerHTML = md("⚠ " + (j.error ? j.error.message : "请求失败")); body.parentElement.className = "msg error"; }
        else body.innerHTML = md(j.reply || "(空)");
      } catch (e) {
        body.classList.remove("cursor");
        body.innerHTML = md("⚠ 网络错误：" + e);
      } finally { finish(); }
    } else {
      // Agent 模式：run 同步返回最终回复，中间过程走 WS 事件
      try {
        const r = await fetch("/web/api/run", {
          method: "POST", credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ message: text }),
        });
        const j = await r.json().catch(() => ({}));
        if (!r.ok) addMsg("error", "错误", j.error ? j.error.message : "运行失败");
        else addMsg("assistant", "开阳", j.reply || "(空)");
      } catch (e) {
        addMsg("error", "错误", "网络错误：" + e);
      } finally { finish(); }
    }
  }
  function finish() {
    busy = false; sendBtn.disabled = false;
    abortBtn.classList.add("hidden");
    log.scrollTop = log.scrollHeight;
    input.focus();
  }
  sendBtn.addEventListener("click", send);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
  });
  abortBtn.addEventListener("click", async () => {
    await fetch("/web/api/abort", { method: "POST", credentials: "same-origin" }).catch(() => {});
  });

  // 输入框自适应高度
  function autoGrow() {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 180) + "px";
  }
  input.addEventListener("input", autoGrow);

  // ---------- 启动 ----------
  setMode("chat");
  checkAuth();
})();
