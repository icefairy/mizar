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
  const jumpBottom = $("jumpBottom"), composerEl = $("composer"), composerToggle = $("composerToggle"),
    composerToggleLabel = $("composerToggleLabel");
  const providerSelect = $("providerSelect"), providerUse = $("providerUse"), providerDel = $("providerDel");
  const providerCurrent = $("providerCurrent"), providerHint = $("providerHint"), providerAdd = $("providerAdd");
  const pvName = $("pvName"), pvURL = $("pvURL"), pvKey = $("pvKey"), pvModel = $("pvModel");

  let mode = "chat";   // chat | agent
  // busyConv：当前正在跑一轮的会话 id（null=空闲）。用它代替全局 busy，
  // 这样切换到别的会话时不会「假忙碌」，也避免不同会话的流式增量互相串台。
  let busyConv = null;
  // busyConvs：本标签页发起的运行集合（多会话并发）。
  // busyConv 是单值，两个会话同时跑时会被后发的覆盖 —— 用它判断「本会话是否在跑」
  // 会把仍在跑的另一个会话漏掉（表现为切回去一片空白、也看不到输出）。
  // 服务端 running_convs 是真值，但状态轮询有 0.7~1.5s 延迟，本地集合补这个窗口。
  const busyConvs = new Set();
  // runningConv：服务端「当前正在运行」的会话 id（来自 /web/api/status）。
  // Agent 是单实例，全局只有一个循环在跑；用它区分「哪个会话占着循环」，
  // 这样在 B 会话发送时能明确提示「A 会话正在运行」，而不是莫名其妙排队。
  let runningConv = null;
  // runningConvs：服务端上报的「正在运行的会话」集合（多会话并发）。
  // 单值 runningConv 已不够用 —— 两个会话同时跑时只记一个会误判。
  let runningConvs = new Set();
  // isConvRunningHere 该会话此刻是否真的在跑。
  // 优先看集合（服务端真值），集合为空时退回 busyConv/runningConv 兼容老服务端。
  function isConvRunningHere(convId) {
    // 本地集合与服务端集合取并集：任意一侧认为在跑就算在跑。
    // 取并集而不是"服务端优先"：服务端轮询有延迟，刚发起的这一轮在服务端还没置位，
    // 此时若只看服务端会判成"没在跑"→ 不重放缓存 → 用户看到的是空白。
    if (busyConvs.has(convId)) return true;
    if (runningConvs.size > 0) return runningConvs.has(convId);
    return (runningConv !== null && runningConv === convId);
  }
  // evtLog：按会话缓存运行中事件。切走期间 A 会话持续输出，前端仍会收 WS 事件
  // （只是不渲染到 DOM），先缓存起来；切回 A 时重放，避免「切走期间的内容丢失」。
  // 结构：{ convId: [ {kind, ...} ] }，每轮开始时清空对应会话。
  // 每条事件带服务端分配的 seq（单调递增），按 seq 有序插入去重：
  // 断线重连补齐的「空洞」事件可能比已缓存的事件更旧，必须插到正确位置，
  // 而重复补齐（重放 + 实时双路径）则直接丢弃。
  let evtLog = {};
  function pushEvt(conv, e) {
    const key = conv == null ? "" : conv;
    const arr = evtLog[key] || (evtLog[key] = []);
    if (typeof e.seq === "number") {
      let lo = 0, hi = arr.length;
      while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (typeof arr[mid].seq === "number" && arr[mid].seq < e.seq) lo = mid + 1;
        else hi = mid;
      }
      if (arr[lo] && arr[lo].seq === e.seq) return false;  // 已存在
      arr.splice(lo, 0, e);
    } else {
      arr.push(e);
    }
    // 防御性上限，避免极端长任务把内存撑爆（保留最近的）
    if (arr.length > 4000) arr.splice(0, arr.length - 4000);
    return true;
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
  // ---------- 滚动跟随（用户上翻后必须放弃自动滚底） ----------
  // 旧实现无条件 scrollTop = scrollHeight：输出期间用户往上翻看历史，
  // 每一个流式增量都会把他拽回底部——根本没法读之前的内容。
  // 现在改成「粘底」语义：只有用户本来就贴着底部时，才继续跟随；
  // 一旦检测到用户主动上翻（离底部超过阈值），立刻放弃跟随，
  // 直到他自己回到底部或点「↓ 回到底部」按钮。
  let stickBottom = true;       // 是否跟随最新输出
  const STICK_THRESHOLD = 48;   // 离底部多少 px 以内算「贴着底部」

  function nearBottom() {
    return (log.scrollHeight - log.scrollTop - log.clientHeight) <= STICK_THRESHOLD;
  }
  // 用 scroll 事件（含用户滚动、键盘、程序滚动）同步真实意图。
  // 只信事件而不是「滚动后再校验一次」：程序滚到底触发的事件也会进来，
  // 那时 nearBottom() 为真，正好把 stickBottom 重新置真，自洽。
  log.addEventListener("scroll", () => {
    stickBottom = nearBottom();
    updateJumpBtn();
  }, { passive: true });
  function updateJumpBtn() {
    if (!jumpBottom) return;
    jumpBottom.classList.toggle("hidden", stickBottom);
  }
  function scrollLog() {
    if (!stickBottom) { updateJumpBtn(); return; }
    log.scrollTop = log.scrollHeight;
    updateJumpBtn();
  }
  // 强制滚到底并恢复跟随（发送新消息、点「回到底部」时用）
  function scrollLogForce() {
    stickBottom = true;
    log.scrollTop = log.scrollHeight;
    updateJumpBtn();
  }
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
  // addSteerMsg 打断/注入成功的消息：已经进入当前循环，直接在本轮里显示成
  // 一条「我」的插话。它不再是待发消息——待发队列里对应项已被移除，
  // 本轮结束时也不会被重新补发（服务端已落到会话历史）。
  function addSteerMsg(conv, text) {
    if (conv && curConv && conv !== curConv) return;   // 用户已切走，切回时从历史读回
    const wrap = document.createElement("div");
    wrap.className = "msg user steer";
    wrap.__raw = text == null ? "" : String(text);
    wrap.innerHTML = whoHtml("我（插话）") + '<div class="body"></div>' +
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
    loadQueue();          // 恢复上次未发出的排队消息（页面被回收前的）
    startStatusPoll();
    loadSettings(j.settings);
    loadConversations(true);
    if (isMobile()) sidebar.classList.add("collapsed"); // 手机端初始收起抽屉
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
      // 恢复上次打开的会话（刷新/页面被系统杀掉后重开，应回到原来那个会话，
      // 而不是每次都跳回列表第一条）。找不到则退回首条。
      let last = null;
      try { last = localStorage.getItem("mizar-last-conv"); } catch (e) {}
      const pick = last && convs.some((c) => c.id === last) ? last : (convs.length ? convs[0].id : null);
      if (pick) selectConv(pick);
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
    try { localStorage.setItem("mizar-last-conv", id); } catch (e) {}
    renderConvList();
    collapseOnMobile();
    clearLog();
    resetTurn();
    renderQueueBar();
    if (askState) closeAsk();
    restorePendingAsks();
    // 中止按钮只反映「当前会话是否正在运行」，切到别的会话时隐藏，避免误以为卡住；
    // busyConv（本标签页发起的运行）或 runningConv（服务端正在跑且属于本会话）都算。
    abortBtn.classList.toggle("hidden", !isConvRunningHere(id));
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
        } else if (m.role === "assistant" && m.kind === "tool") {
          // 工具过程（刷新后从落盘历史恢复）：模型的说明 reason + 参数 + 结果
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          const sb = ensureSteps();
          appendToolStep(sb, m);
        } else if (m.role === "assistant") {
          if (!curTurn) { newTurn(); curTurn = turnEl; }
          const textEl = ensureReply(m.ts);
          const wrap = textEl.parentElement.parentElement;
          wrap.__raw = m.content;
          textEl.innerHTML = md(m.content);
        } else {
          // sys / 其他：进答复框步骤容器（累加可见）
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
      scrollLogForce();   // 切会话总是从最新处开始看
      setMode(c.mode === "agent" ? "agent" : "chat");
      applyBrowserTitle();
      // 切回本会话：如果该会话仍在后台运行（本标签页发起 busyConv，或服务端正在跑
      // 该会话 runningConv——可能来自别的标签页），把切走期间缓存的事件重放到界面，
      // 否则用户会看到「切回来要等一会才有新内容 / 中间内容缺失」。
      // 用集合判断（isConvRunningHere）：busyConv/runningConv 都是单值，
      // 多会话并发时会被最后一个会话覆盖，导致切回另一个仍在跑的会话时
      // 判定为「没在跑」→ 不重放缓存也不补齐，表现为「切回来一直空白」。
      const liveHere = isConvRunningHere(id);
      if (liveHere) {
        newTurn();
        replayEvtLog(id);
        // 本地缓存只覆盖「页面还活着时收到的 WS 事件」。若期间页面被冻结/断线，
        // 缺失的部分必须向服务端按 seq 补齐，否则切回来仍会缺中间过程。
        backfillEvents(id, true).then(() => restoreDraft(id));
      } else {
        // 本页不知道它是否在跑（刚打开页面/刚换设备）：问服务端。
        resumeConvIfRunning(id);
      }
      restoreInputDraft();
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
  function isMobile() { return window.matchMedia("(max-width: 720px)").matches; }
  function collapseOnMobile() {
    // 手机端侧栏是覆盖式抽屉，选完会话必须收起，否则挡住内容；
    // pin（常驻锁定）只对 PC 有意义，移动端忽略。
    if (isMobile()) { sidebar.classList.add("collapsed"); return; }
    if (pinned) return; // 已锁定常驻，不自动收起
  }
  // 从窄屏切回宽屏时，把抽屉状态复位成正常展开（避免 PC 上看不到侧栏）
  let wasMobile = isMobile();
  window.addEventListener("resize", () => {
    const nowMobile = isMobile();
    if (wasMobile && !nowMobile) {
      sidebar.classList.remove("collapsed");
    }
    wasMobile = nowMobile;
  });
  // 侧栏开关：主动打开后，点主区空白处自动收起（含桌面端）
  let sidebarToggled = false;
  if (menuBtn) menuBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    if (pinned && !isMobile()) return; // 锁定时不允许收起（仅 PC 有意义）
    const willOpen = sidebar.classList.contains("collapsed");
    sidebar.classList.toggle("collapsed");
    sidebarToggled = willOpen;
  });
  document.addEventListener("click", (e) => {
    if (pinned && !isMobile()) return;
    if (!sidebarToggled) return;
    if (sidebar.contains(e.target) || (menuBtn && menuBtn.contains(e.target))) return;
    sidebar.classList.add("collapsed");
    sidebarToggled = false;
  });
  newConvBtn.addEventListener("click", createConv);
  // 移动端抽屉遮罩：点击关闭侧栏
  const sideOverlay = $("sideOverlay");
  if (sideOverlay) sideOverlay.addEventListener("click", () => {
    sidebar.classList.add("collapsed");
    sidebarToggled = false;
  });

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
        // 记录服务端当前运行的会话：多会话并发下用集合，单值仅作兼容兜底。
        const rc = (j && j.running) ? (j.running_conv || "") : null;
        const convList = (j && Array.isArray(j.running_convs)) ? j.running_convs : null;
        if (convList) {
          const next = new Set(convList);
          // 边跑边被移出集合的会话：清掉它的增量缓存（内容已落盘）
          runningConvs.forEach((id) => { if (!next.has(id)) evtLog[id] = []; });
          runningConvs = next;
        } else if (rc !== runningConv) {
          if (runningConv && runningConv !== rc) evtLog[runningConv] = [];
          runningConvs = rc ? new Set([rc]) : new Set();
        }
        if (rc !== runningConv) {
          runningConv = rc;
          renderQueueBar();
        }
        // 本会话不在运行集合里就尝试补发排队消息（别的会话在跑不再阻挡）
        if (j && !runningConvs.has(curConv)) drainPending();
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
  // 断线自愈：手机切到后台、锁屏、切网络时，WS 会被系统掐断且 JS 收不到 close；
  // 回到前台后必须重连，并把「期间缺失的事件」按 seq 补齐，否则界面就停在切走前
  // 那一刻——这正是「切回来发现中间过程全没了」的根因。
  let wsReconnectDelay = 500;      // 重连退避（ms），成功后复位
  let wsReconnectTimer = null;
  let wsEverOpened = false;
  let pendingBackfill = false;     // 需要在连接就绪后补齐事件
  // evtCursor：每个会话「已经渲染/缓存到的最大 seq」。断线期间服务端继续跑，
  // 重连后带 after=cursor 拉取缺失部分即可，天然去重。
  let evtCursor = {};
  function setCursor(conv, seq) {
    if (typeof seq !== "number" || seq <= 0) return;
    const k = conv == null ? "" : conv;
    if (!evtCursor[k] || seq > evtCursor[k]) evtCursor[k] = seq;
  }
  function getCursor(conv) { return evtCursor[conv == null ? "" : conv] || 0; }

  function connectWS() {
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const sock = new WebSocket(proto + "://" + location.host + "/web/ws");
    ws = sock;
    sock.onopen = () => {
      wsReconnectDelay = 500;
      wsEverOpened = true;
      startHeartbeat(sock);
      restorePendingAsks();
      // 重连成功：先补齐断线期间的事件，再刷新一次状态/历史
      backfillEvents(curConv, true);
      refreshAfterReconnect();
    };
    sock.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === "pong") { hbLastPong = Date.now(); return; }
      if (m.type === "hello") {
        // 服务端主动告知当前运行归属；若正在跑当前会话而后台期间漏了事件，立即补齐
        if (m.running && m.running_conv === curConv) backfillEvents(curConv, true);
        return;
      }
      if (m.type !== "event") return;
      handleServerEvent(m);
      // seq 缺口检测：链路抖动丢了一条时（极少数），补偿性补齐。
      // 注意：cursor 由 pushEvt 维护（按 seq 有序插入），这里只判断缺口。
      if (typeof m.seq === "number") {
        const conv = m.conversation_id == null ? "" : m.conversation_id;
        const prev = getCursor(conv);
        if (prev && m.seq > prev + 1 && conv === curConv) backfillEvents(conv, false);
      }
    };
    sock.onerror = () => { /* onclose 会接手 */ };
    sock.onclose = () => {
      if (ws !== sock) return;   // 已被替换
      ws = null;
      stopHeartbeat();
      scheduleReconnect();
    };
  }
  // startHeartbeat 应用层心跳：移动网络/代理会静默回收空闲连接（浏览器收不到
  // close，readyState 仍是 OPEN 的「假活」）。定时 ping 能及时暴露断链，
  // 触发 onclose → 重连 → 按 seq 补齐事件。
  let hbTimer = null;
  let hbLastPong = 0;
  function startHeartbeat(sock) {
    stopHeartbeat();
    hbLastPong = Date.now();
    hbTimer = setInterval(() => {
      if (ws !== sock || sock.readyState !== WebSocket.OPEN) return;
      // 连续两个周期没有 pong：判定假活，主动关闭触发重连
      if (Date.now() - hbLastPong > 55000) {
        try { sock.close(); } catch (e) {}
        return;
      }
      try { sock.send(JSON.stringify({ type: "ping" })); } catch (e) {}
    }, 25000);
  }
  function stopHeartbeat() {
    if (hbTimer) { clearInterval(hbTimer); hbTimer = null; }
  }
  function scheduleReconnect() {
    if (wsReconnectTimer) return;
    // 记录「曾经连上过」：重连后需要补齐事件
    pendingBackfill = wsEverOpened;
    const d = wsReconnectDelay;
    wsReconnectDelay = Math.min(wsReconnectDelay * 2, 15000);
    // 加抖动，避免多标签页同时重连
    const jitter = Math.floor(d * 0.2 * Math.random());
    wsReconnectTimer = setTimeout(() => {
      wsReconnectTimer = null;
      if (document.hidden) return;   // 后台不白费电，等 visibilitychange 再连
      connectWS();
    }, d + jitter);
  }
  // 处理一条服务端事件（live 与 backfill 共用同一路径，保证渲染完全一致）
  function handleServerEvent(m) {
    const evConv = m.conversation_id == null ? "" : m.conversation_id;
    if (typeof m.seq === "number") setCursor(evConv, m.seq);
    const forCurrent = (evConv === "" || evConv === curConv);
    if (m.event === "tool_call") {
      pushEvt(evConv, { kind: "tool_call", tool: m.tool, args: m.args, seq: m.seq });
      if (forCurrent) onToolCall(m.tool, m.args);
    } else if (m.event === "tool_result") {
      pushEvt(evConv, { kind: "tool_result", tool: m.tool, result: m.result, seq: m.seq });
      if (forCurrent) onToolResult(m.tool, m.result);
    } else if (m.event === "stream") {
      pushEvt(evConv, { kind: "stream", step: m.step, delta: m.delta, skind: m.kind, seq: m.seq });
      if (forCurrent) handleStream(m.step, m.delta, m.kind);
    } else if (m.event === "llm_response") {
      // 每步结束的完整回复：作为条目收尾（避免重放后步骤一直显示光标）
      if (forCurrent && thinkStepEl) { thinkStepEl.classList.remove("cursor"); thinkStepEl = null; }
    } else if (m.event === "run_done") {
      // 服务端本轮结束。若本页没有对应的 pending 请求（页面曾在后台被杀/刷新，
      // 或任务由别的标签页发起），就刷新历史把最终答复拉下来。
      if (forCurrent) onRunDoneRemote(evConv);
    } else if (m.event === "ask_user") {
      showAsk(m.id, m.questions, m.conversation_id);
    } else if (m.event === "ask_close") {
      closeAsk(m.id);
    }
  }
  // backfillEvents 用 /web/api/events 拉取 after=cursor 之后的事件并重放。
  // quiet=true 时不提示用户（常规重连）；quiet=false 时若发现被截断会提示。
  let backfilling = false;
  async function backfillEvents(convId, quiet) {
    if (!convId) return;
    if (backfilling) { pendingBackfill = true; return; }
    backfilling = true;
    try {
      const after = getCursor(convId);
      const j = await api("/web/api/events?conversation_id=" + encodeURIComponent(convId) + "&after=" + after);
      const list = (j && j.events) || [];
      const serverLive = !!(j && j.running && j.running_conv === convId);
      // after=0 且该会话并未在跑：说明这是「刚打开页面/刚切换会话」，
      // 内容已由历史接口完整加载，不能把缓冲事件再重放一遍（否则重复）。
      if (after === 0 && !serverLive && busyConv !== convId) {
        if (j && typeof j.last === "number") setCursor(convId, j.last);
        return;
      }
      if (list.length) {
        // 当前会话且本轮仍在跑：确保有承接容器，否则重放会落到上一轮的旧容器里
        const live = (busyConv === convId || serverLive);
        if (convId === curConv && live) { if (!turnEl) newTurn(); }
        list.forEach((m) => {
          if (typeof m.seq === "number") setCursor(convId, m.seq);
          handleServerEvent(m);
        });
        scrollLog();
        flashQuick("已补齐 " + list.length + " 条后台输出");
      }
      if (j && typeof j.last === "number") setCursor(convId, j.last);
      if (j && j.truncated && !quiet) {
        flashQuick("过程过长，早期部分已省略（完整内容见历史）");
      }
    } catch (e) {
      // 401/网络问题：忽略，下次重连再补
    } finally {
      backfilling = false;
      if (pendingBackfill) { pendingBackfill = false; }
    }
  }
  // refreshAfterReconnect 重连后刷新状态与会话列表（后台期间可能有新会话/新标题）
  async function refreshAfterReconnect() {
    if (!appView || appView.classList.contains("hidden")) return;
    try {
      const j = await api("/web/api/conversations");
      convs = j.conversations || [];
      renderConvList();
    } catch (e) {}
  }
  // onRunDoneRemote 收到服务端的「本轮结束」事件时的收尾。
  // 本页正持有该轮的 pending 请求时不用管（runTurn 的 finally 会处理）。
  function onRunDoneRemote(convId) {
    if (busyConv === convId) return;   // 本页发的，走 runTurn 正常路径
    if (convId && convId !== curConv) return;
    runningConv = null;
    abortBtn.classList.add("hidden");
    evtLog[convId] = [];
    syncCurrentConv();
    renderQueueBar();
    drainPending();
  }
  // restoreDraft 兜底：事件环形缓冲已截断（超长任务）或页面整个被系统杀掉时，
  // 用服务端落盘的草稿把「已生成的正文」补出来，避免界面一片空白。
  async function restoreDraft(convId) {
    if (!convId || convId !== curConv) return;
    if (streamBuf) return;   // 已经通过事件重放拼出正文，无需草稿
    try {
      const j = await api("/web/api/draft?conversation_id=" + encodeURIComponent(convId));
      if (!j || !j.draft) return;
      if (convId !== curConv || streamBuf) return;
      if (!turnEl) newTurn();
      const body = ensureReply();
      streamBuf = j.draft;
      body.parentElement.parentElement.__raw = streamBuf;
      body.innerHTML = md(streamBuf);
      body.classList.add("cursor");
      scrollLog();
      flashQuick("已恢复后台生成中的内容");
    } catch (e) {}
  }
  // resumeConvIfRunning 打开/切到某会话时确认它是否正在服务端运行：
  // 若是（例如页面被系统杀掉前发起的长任务，用户在手机上重新打开），
  // 补一个 turn 容器并补齐事件，让用户直接看到继续增长的输出。
  async function resumeConvIfRunning(convId) {
    try {
      const j = await api("/web/api/draft?conversation_id=" + encodeURIComponent(convId));
      if (!j || !j.running || j.running_conv !== convId) return;
      if (convId !== curConv) return;
      runningConv = convId;
      abortBtn.classList.remove("hidden");
      newTurn();
      await backfillEvents(convId, true);
      await restoreDraft(convId);
      renderQueueBar();
    } catch (e) {}
  }
  // reconcileBusy 前台恢复时校正「假忙碌」：
  // 移动端页面被系统丢弃/网络中断时，runTurn 的 finally 可能没机会执行，
  // busyConv 会永远停在该会话上——表现为按钮一直转、排队消息永不发出。
  // 以服务端 running 为准，不一致就纠正并重载该会话历史。
  async function reconcileBusy() {
    try {
      const j = await api("/web/api/status" + (curConv ? "?id=" + encodeURIComponent(curConv) : ""));
      const srvList = (j && Array.isArray(j.running_convs)) ? j.running_convs : null;
      const srvSet = srvList ? new Set(srvList) : new Set(j && j.running ? [(j.running_conv || "")] : []);
      const srvConv = (j && j.running_conv) || "";
      runningConvs = new Set(srvSet);
      runningConv = srvSet.size > 0 ? srvConv : null;
      // 逐会话校正：本标签页以为在跑、服务端却没有 → 收尾那一个会话。
      // 不能像以前那样用单值 busyConv 一刀切：多会话并发时会把仍在跑的
      // 另一个会话误清掉（切回去一片空白，且排队消息提前补发）。
      let fixed = false;
      busyConvs.forEach((was) => {
        if (srvSet.has(was)) return;
        busyConvs.delete(was);
        if (busyConv === was) busyConv = null;
        evtLog[was] = [];
        fixed = true;
        if (was === curConv) {
          // 该会话服务端已结束：重载历史拿到最终答复
          loadConvQuietly(was);
        }
      });
      abortBtn.classList.toggle("hidden", !isConvRunningHere(curConv));
      // 服务端仍在跑本会话、只是本页漏了事件：确保有承接容器
      if (isConvRunningHere(curConv) && !turnEl) newTurn();
      if (fixed) drainPending();
      renderQueueBar();
    } catch (e) {}
  }
  // loadConvQuietly 重载某会话历史但不强制滚到底（用户可能正在上翻读历史）。
  async function loadConvQuietly(id) {
    const keepStick = stickBottom;
    try { await selectConv(id); } finally { stickBottom = keepStick; updateJumpBtn(); }
  }
  // syncCurrentConv 后台期间会话可能已被跑完（本标签页或其他设备/标签页）：
  // 最终答复只落在服务端历史里，没有对应「事件」可重放。这里比对消息条数，
  // 发现变化就重载当前会话，保证切回来看到的是完整结果。
  let syncing = false;
  async function syncCurrentConv() {
    if (syncing || !curConv) return;
    if (isConvRunningHere(curConv)) return;   // 本轮还在跑，不打断
    syncing = true;
    try {
      const j = await api("/web/api/conversations");
      const list = j.conversations || [];
      convs = list;
      renderConvList();
      const mine = list.find((c) => c.id === curConv);
      if (!mine) return;
      const shown = log.querySelectorAll(".msg").length;
      if (mine.count !== shown) await selectConv(curConv);
    } catch (e) {
    } finally { syncing = false; }
  }
  // 回到前台 / 网络恢复：立即重连并补齐。移动端浏览器从后台唤醒时
  // WS 往往已死但 readyState 仍是 OPEN（假活），这里主动探活。
  function resumeFromBackground() {
    if (!appView || appView.classList.contains("hidden")) return;
    if (!ws || ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING) {
      connectWS();
    }
    // 无论连接状态如何都补一次：假活连接上丢的事件只能靠 seq 补齐
    backfillEvents(curConv, true);
    refreshAfterReconnect();
    reconcileBusy();
    // 等补齐落地后再比对历史（避免把刚重放的内容误判为条数不一致而整页重载）
    setTimeout(syncCurrentConv, 500);
  }
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) resumeFromBackground();
  });
  window.addEventListener("pageshow", (e) => {
    // 移动端 bfcache 恢复（返回键回到页面）也走同一套
    if (e.persisted) resumeFromBackground();
  });
  window.addEventListener("online", resumeFromBackground);
  window.addEventListener("focus", () => { if (!document.hidden) resumeFromBackground(); });

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
  // 工具过程（历史回放用）：一段工具调用 = 模型说明 + 参数 + 结果。
  // 与实时 onToolCall/onToolResult 的视觉结构一致（🔧 参数行 + ↳ 结果行），
  // 差别是历史里把模型那句 reason 也带上，刷新后仍能看到当时的判断依据。
  function appendToolStep(sb, m) {
    if (m.reason) {
      const r = document.createElement("div");
      r.className = "pstep pstep-text";
      r.textContent = compact(m.reason, 300);
      sb.appendChild(r);
    }
    const head = document.createElement("div");
    head.className = "pstep pstep-tool" + (m.err ? " pstep-err" : "");
    const a = compact(m.args, 160);
    head.innerHTML = '<span class="pico">' + (m.err ? "✗" : "🔧") + '</span>' +
      '<span class="ptool">' + esc(m.tool || "") + '</span>' +
      (a ? '<span class="pargs">' + esc(a) + '</span>' : "");
    sb.appendChild(head);
    const body = m.err || m.result;
    if (body) {
      const step = document.createElement("div");
      step.className = "pstep pstep-res";
      step.innerHTML = '<span class="pico">↳</span><span class="pres">' + esc(compact(body, 200)) + '</span>';
      sb.appendChild(step);
    }
    bumpProc();
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
  // 排队消息持久化：手机切后台被回收/刷新后，还没发出去的排队消息不能丢。
  // 只存 "queue" 类（"steer" 已注入当前循环，重发会重复）。
  function saveQueue() {
    try {
      // 只存还没被服务端确认消费的（"steer" 已确认注入，重发会重复）。
      const keep = pendingQueue.filter((x) => x.kind !== "steer");
      if (keep.length) localStorage.setItem("mizar-queue", JSON.stringify(keep));
      else localStorage.removeItem("mizar-queue");
    } catch (e) {}
  }
  function loadQueue() {
    try {
      const raw = localStorage.getItem("mizar-queue");
      if (!raw) return;
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) {
        arr.forEach((it) => {
          if (it && it.text) pendingQueue.push({ id: ++queueSeq, text: it.text, conv: it.conv, kind: it.kind === "steer-pending" ? "queue" : (it.kind || "queue") });
        });
      }
    } catch (e) {}
    renderQueueBar();
  }
  function pushQueue(text, conv, kind) {
    const id = ++queueSeq;
    pendingQueue.push({ id: id, text: text, conv: conv, kind: kind || "queue" });
    saveQueue();
    renderQueueBar();
    return id;
  }
  function setQueueKind(id, kind) {
    const it = pendingQueue.find((x) => x.id === id);
    if (it) { it.kind = kind; saveQueue(); renderQueueBar(); }
  }
  function dropQueue(id) {
    pendingQueue = pendingQueue.filter((x) => x.id !== id);
    saveQueue();
    renderQueueBar();
  }
  // convLabelOf 取会话标题，用于提示「哪个会话正在运行」。找不到就退回 id 前缀。
  function convLabelOf(id) {
    const c = convs.find((x) => x.id === id);
    if (c && c.title) return c.title.length > 12 ? c.title.slice(0, 12) + "…" : c.title;
    return id ? id.slice(0, 8) : "其它会话";
  }
  function renderQueueBar() {
    if (!queueBar) return;
    const mine = pendingQueue.filter((x) => x.conv === curConv);
    if (!mine.length) { queueBar.classList.add("hidden"); queueBar.innerHTML = ""; return; }
    const nSteer = mine.filter((x) => x.kind === "steer").length;
    const nPending = mine.filter((x) => x.kind === "steer-pending").length;
    const nQueue = mine.length - nSteer - nPending;
    let head = [];
    if (nSteer) head.push(nSteer + " 条已注入当前任务");
    if (nPending) head.push(nPending + " 条注入中…");
    if (nQueue) {
      // 多会话并发后「别的会话在跑」不再导致排队，排队只可能因为本会话忙或服务端并发满
      head.push(nQueue + " 条等待发送");
    }
    let html = '<div class="qhead">待处理消息 · ' + esc(head.join(" · ")) + '</div>';
    mine.forEach((it) => {
      const kindCls = it.kind === "steer" ? "steer" : (it.kind === "steer-pending" ? "pending" : "");
      const label = it.kind === "steer" ? "已注入" : (it.kind === "steer-pending" ? "注入中" : "排队");
      html += '<div class="qitem" data-qid="' + it.id + '">' +
        '<span class="qbadge ' + kindCls + '">' + label + '</span>' +
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
    clearInputDraft();
    const convId = curConv;
    // 服务端当前正在跑的会话（可能不是本会话，也可能来自别的浏览器标签）。
    // 注意：busyConv 只是「本页发起的运行」的本地记录，不能代表服务端真实状态——
    // 本页发起后、服务端还没置 running 时，busyConv 已有值而 runningConv 还是 null；
    // 反过来服务端跑着别的会话时，busyConv 可能是 null。两者都要看，取非空的。
    // 多会话并发：服务端每个会话用独立 Agent 实例，B 会话可以直接跑，
    // 不需要等 A 结束。只有同一个会话正在跑时才需要打断/排队。
    // runningConvs 是服务端上报的「正在跑的会话集合」；单值 runningConv
    // 作为兼容兜底（老服务端只返回 running_conv）。
    if (isConvRunningHere(convId)) {
      // 本会话正在跑一轮。
      if (!interruptMode || mode !== "agent") {
        pushQueue(text, convId, "queue");
        flashQuick("已排队，本轮结束后自动发送");
        return;
      }
      const qid = pushQueue(text, convId, "steer-pending");
      try {
        const j = await api("/web/api/steer", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: text, conversation_id: convId }),
        });
        if (j && j.running === false) {
          // 服务端确实没有活跃循环（这一轮刚结束）：降级为排队，本轮结束后补发。
          setQueueKind(qid, "queue");
          flashQuick("本轮已结束，改为排队，稍后自动发送");
        } else {
          // 已注入当前循环：模型下一步就能看到，**不再是待处理消息**。
          // 立刻从待发队列移除（否则前端一直显示「排队/注入中」，误导用户
          // 以为还没生效、甚至以为会被重复发送），并在本轮里显示成一条用户插话。
          dropQueue(qid);
          addSteerMsg(convId, text);
          flashQuick("已注入当前任务，模型下一步即生效");
        }
      } catch (e) {
        setQueueKind(qid, "queue");
        flashQuick("注入失败，已改为排队，本轮结束后自动发送");
      }
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
    saveQueue();
    renderQueueBar();
    let stopped = false;
    try {
      const j = await api("/web/api/abort", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ conversation_id: convId }),
    });
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
    scrollLogForce();   // 用户刚发消息：无论之前翻到哪儿，都要看到这条与后续回答
    evtLog[myConv] = [];   // 新一轮开始：清掉上一轮缓存的增量事件
    busyConv = myConv;
    busyConvs.add(myConv);
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
        // 409 现在只可能两种原因：本会话已有一轮在跑，或服务端跨会话并发已满。
        // 都当排队处理，稍后自动补发；不当错误展示。
        if (turnEl) { try { turnEl.remove(); } catch (e2) {} }
        pushQueue(text, myConv, "queue");
        flashQuick("服务端正忙，已排队，稍后自动发送");
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
    // 只清本次自己那一轮的标记：多会话并发时别把别的会话的状态一起清掉
    busyConvs.delete(myConv);
    if (busyConv === myConv) busyConv = null;
    runningConvs.delete(myConv);
    evtLog[myConv] = [];   // 本轮已结束，增量事件已并入最终答复，清空缓存避免重复渲染
    // 只有当前会话确实没在跑时才隐藏中止按钮：多会话并发时别的会话可能还在跑
    abortBtn.classList.toggle("hidden", !isConvRunningHere(curConv));
    // 本轮结束后清理属于本会话的待处理项：
    //   - "steer"（已确认注入当前循环）：服务端已被消费或已落盘，不能再补发（否则重复）；
    //   - "steer-pending"（注入请求还没回话）：状态未确认，按「可能没生效」处理——
    //     宁可补发一次也不能丢用户的消息；
    //   - "queue"（排队）：本轮结束后补发为新任务。
    const mine = pendingQueue.filter((it) => it.conv === myConv);
    pendingQueue = pendingQueue.filter((it) => it.conv !== myConv);
    const resend = mine.filter((it) => it.kind !== "steer");
    // 本会话待发消息放回队首，保持顺序；随后统一 drain（也覆盖别的会话排队的消息）
    if (resend.length) pendingQueue = resend.concat(pendingQueue);
    saveQueue();
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
    // 只看本会话是否在跑：别的会话并发跑着不该挡住本会话的补发
    if (isConvRunningHere(curConv)) return;
    const items = pendingQueue.filter((it) => it.conv === curConv && it.kind !== "steer");
    if (!items.length) return;
    pendingQueue = pendingQueue.filter((it) => !(it.conv === curConv && it.kind !== "steer"));
    saveQueue();
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
  // 回到底部：恢复跟随并滚到最新
  if (jumpBottom) jumpBottom.addEventListener("click", () => scrollLogForce());

  // ---------- 输入区折叠 ----------
  // 常驻的状态栏 + 快捷参数 + 5 行输入框会吃掉太多屏幕，导致看回答的区域太小。
  // 默认收起（只留一行输入），点把手展开。选择持久化，避免每次刷新都要重设。
  const COMPACT_KEY = "mizar-composer-compact";
  function applyComposerCompact(compact) {
    if (!composerEl) return;
    composerEl.classList.toggle("compact", compact);
    if (composerToggleLabel) composerToggleLabel.textContent = compact ? "展开参数" : "收起参数";
    try { localStorage.setItem(COMPACT_KEY, compact ? "1" : "0"); } catch (e) {}
  }
  function isComposerCompact() {
    return !!(composerEl && composerEl.classList.contains("compact"));
  }
  if (composerEl) {
    let compact = true;   // 默认收起：屏幕宝贵，先给内容
    try {
      const v = localStorage.getItem(COMPACT_KEY);
      if (v === "0") compact = false;
      else if (v === "1") compact = true;
      else if (isMobile()) compact = true;
      else compact = false;   // 桌面端屏幕大，默认展开更直观
    } catch (e) { compact = !isMobile(); }
    applyComposerCompact(compact);
  }
  if (composerToggle) {
    composerToggle.addEventListener("click", () => applyComposerCompact(!isComposerCompact()));
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
  // ---------- 输入框草稿（未发送内容的本地保留） ----------
  // 手机上切走/被系统回收很常见，输入框里写了半天的长消息不能丢。
  // 按会话分别存 localStorage，发送成功或切换会话时清理。
  function draftKey(conv) { return "mizar-draft-" + (conv || ""); }
  let draftSaveTimer = null;
  input.addEventListener("input", () => {
    if (draftSaveTimer) clearTimeout(draftSaveTimer);
    draftSaveTimer = setTimeout(() => {
      try {
        if (input.value) localStorage.setItem(draftKey(curConv), input.value);
        else localStorage.removeItem(draftKey(curConv));
      } catch (e) {}
    }, 300);
  });
  function restoreInputDraft() {
    try {
      const v = localStorage.getItem(draftKey(curConv)) || "";
      if (v && !input.value) { input.value = v; autoGrow(); }
    } catch (e) {}
  }
  function clearInputDraft() {
    try { localStorage.removeItem(draftKey(curConv)); } catch (e) {}
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
