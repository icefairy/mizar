# Changelog

所有重要变更记录在此。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。

---

## [Unreleased]

### 新增

- **`mizar -c` 续接最后一次退出的会话**（`cmd/mizar/main.go` + `internal/session/last.go`）。
  退出时（TUI / 经典模式 / `-task` 单次执行）把当前会话 ID + 所在作用域写进
  **会话根目录的唯一状态文件** `~/.mizar/sessions/last-session.json`（`{id, scope}`，
  原子写）；下次启动用 `-c`（或 `-continue`）即可续接该会话，无需记 UUID。
  **`-c` 续接的是「全局最后一次退出的会话」**——一份全局状态直接给出结果，与当前
  工作路径无关，并自动切换到该会话所在作用域。无记录时回退为新建会话；仅记录已落盘
  （有消息）的会话。旧版散落在 `<scope>/last-session` 的纯文本记录会被兼容读取。

### 修复

- **提问工具不再受 30s 工具超时限制，用户不回答就一直等**（`internal/plugins/loader.go` +
  `internal/builtins/ask.go` + `internal/builtins/plan_tool.go`）。此前插件管理器 `Call`
  对所有工具统一套 `maxExec`（30s），`ask_user_question` / `exit_plan_mode` 这类阻塞等待
  人类作答的工具会被直接判"tool execution timeout"——用户离开几分钟回来，题目早已过期、
  整轮失败。新增 `Tool.NoTimeout` 声明：声明该字段的工具执行不设上限，一直等到 `Run` 返回
  （退出路径仍由 TUI 的 ESC 取消任务负责）。`ask_user_question` 与 `exit_plan_mode` 已置位。
  新增 `TestNoTimeoutTool` 覆盖。
- **TUI 内容区鼠标滚轮失效**（`cmd/mizar/tui.go`）。滚轮事件原先交给
  `TextView.MouseHandler` 处理，它只改自己的 `lineOffset`，外部状态
  `userScrolledUp` 保持 `false`；而流式回复（60ms）、bash 实时输出（80ms）、
  屏幕看门狗每次 `renderAllDirect` 都会「不在底部就 `ScrollToEnd`」，把刚滚上去的
  视图立刻拽回底部 —— 观感就是「内容区滚轮完全没用」。现在 `MouseCapture` 自己
  接管滚轮（指针在聊天区内时），统一走 `scrollChat`：一格 3 行、维护
  `userScrolledUp`、到底自动恢复跟随。
  顺带修掉 `scrollChat` 的「到底」判据错误：原先拿 `row+delta >= 总行数` 比较，
  而 `row` 最大只能到 `总行数-视口高度`，条件永远不成立 → 下滚到底也不恢复自动
  跟随。改为与 `maxScrollRow()`（总行数 − 视口高度）比较。新增 `wheel_test.go`
  以 SimulationScreen 端到端覆盖：滚轮上滚、滚后重绘不被拽回、下滚到底复位跟随、
  指针在聊天区外不劫持。

### 变更

- **等待态轮播提示语补充近期新功能**（`cmd/mizar/stream.go`）。等待模型响应时状态栏 /
  经典模式会每 4 秒轮换一条提示语，此前只讲长期特性（自举、插件热重载、会话压缩等），
  近期上线的能力一条没露。现在分成「长期特性」与「近期新增（v0.7.x）」两段，
  新增：`mizar -c` 续接全局最后退出的会话、交互提问、计划模式 `/plan`、会话目标 `/goal`、
  定时提醒、子代理委派 `delegate_task`、长期记忆、知识库 `kb_*`、状态栏项目行、
  鼠标滚轮平滑滚动 / Ctrl+L 清屏残留、Web UI + Server 多端接入、内嵌 Zig 工具链。
  新增 `stream_test.go`：断言提示语单行非空不重复，且覆盖这些近期功能关键词
  （新功能上线时同步更新该表，避免提示语长期停留在老特性上）。

## [v0.7.3] — 2026-11-09

> 主题：**让 ask 端到端可用 + 状态栏两行化**。此前的 ask_user_question 连续踩了三坑：
> 参数形状不符就整轮失败、题目与选项压根没画出来、回完编号按回车又**彻底卡死**
> （键盘全哑）。这一版把三处一并修掉，并给状态栏加了一行项目信息
> （路径 / 分支 / 改动 / 会话 / 运行时长）。

### 修复

- **回答 ask_user_question 后 TUI 卡死**（`cmd/mizar/tui.go`）。`submitInput` 整体运行在
  tcell 事件循环线程（InputCapture → Enter → submitInput），而回答分支用了
  `m.app.QueueUpdateDraw(...)`。tview 的 `QueueUpdate` 语义是「投递后**阻塞等待**回调在
  事件循环里执行完」，事件循环线程正在这里等自己 → 自死锁，表现就是「输入 1 回车后
  啥操作也不能做」。改为事件循环内直接 `addChatLine` / `renderAllDirect`。
  与 v0.7.2 的 Ctrl+L 自锁是同一个模式。
- **ask_user_question 此前两处「看不见 / 答不了」的修复也一并进入本版**（均在本版
  之前的开发提交里完成，二进制与 v0.7.3 同源）：
  1. **提问必失败：参数形状不匹配**（`internal/builtins/ask.go`）。工具 Description 里
     没写 `Args` 段，`ArgsSchema()` 兜底成 `{args: string}`，模型只能猜参数形状，一次
     不符就整轮失败。现在补全 Description 的 Args 说明，并新增 `parseQuestions` 宽容
     归一化（裸数组 / `{questions:…}` / `{args:…}` / 字符串化 JSON / 双层包装都能吃下），
     缺 `id` 自动补序号、`title`/`text` 兼容为 `question`；报错文案改为给出正确调用示例，
     不再只说「不合法」让模型原样重试。
  2. **题目与选项不显示 + 5 分钟固定超时**（`cmd/mizar/tui.go`）。提问块被拼在
     `m.textView.SetText()` **之后**，追加的内容永远不会被绘制，用户看不到题目自然
     无法作答 —— 移到 `SetText` 之前并抽成 `renderAskPrompt` 纯函数便于单测；同时去掉
     `askViaChat` 的 5 分钟 deadline（超时会静默返回空答案让模型自行决策，人在忙时几分钟
     才回来看一眼，题目刚被看到就已过期）。退出路径只剩「作答」与「ESC 取消任务」。

### 新增

- **状态栏第一行：项目信息**。显示 `📁 codes/mizar  ⎇ main ✱3  │ 会话 a1b2c3d4  │ 已运行 12m`：
  - 路径只取末两级，家目录前缀折叠为 `~`；
  - git 分支（detached 显示短 SHA）+ 未提交改动条数（干净时不显示）；
  - 会话短号、进程运行时长；
  - 路径放不下时**只对目录段做横向滞动**，分支/会话/时长固定不动；窗口末端对齐路径末尾
    并停留，保证**项目名（末端）始终能被完整看清**；
  - git 命令只在启动与任务结束时各跑一次，渲染只读缓存（状态栏高频重绘不能跑命令）。
- `cmd/mizar/applyAnswer` 抽成纯函数承载「推进答案 / 生成 payload」，便于单测覆盖多问题逐问推进。

### 测试

- `ask_deadlock_test.go`：AST 扫描断言 `submitInput` **同步路径**不得出现
  `QueueUpdate`/`QueueUpdateDraw`（`go func` 内允许），把这次事故的不变式锁进 CI；
  另覆盖单问/多问/重复作答的答案推进。
- `project_info_test.go`：项目行渲染、`shortDir` 末两级、时长格式、真实 git 仓库采集，
  以及 `windowPath` 的「每帧不超宽 + 末端一定完整可见 + 滚动时带 … 前缀」。
- `internal/builtins/ask_test.go`：`parseQuestions` 的各种宽容形状（裸数组 / 包装 /
  字符串化 JSON / 缺 id 补序号）。
- `cmd/mizar/ask_render_test.go`：`renderAskPrompt` 渲染出编号选项、`questions` 原始
  JSON 为空时回退用已解析的 `qs` 渲染（避免单问题场景渲染成空白）。
- `version` 提升到 **v0.7.3**（`cmd/mizar/main.go`）。

## [v0.7.2] — 2026-11-06

> 主题：**Windows 交互四连修**。v0.7.1 的两处修复在 Windows 实机上仍能复现，
> 这一版把根因补全：滚轮仍然会误取消（泄漏序列被 ConPTY 拆批到达，90ms 窗口等不到）、
> 粘贴多行文本时输入框「一个字一个字地蹦」（每个字符都触发一次全文扫描 + 全屏重绘），
> 以及**方向键变成 `[D` / `[A`**（终端把原始 VT 序列逐字符送上来，tview 只认 Key）、
> **聊天区残留文字擦不掉**（绕过 tcell 的直写 + CellBuffer 宽度落后）。

### 修复

- **Windows 滚轮仍会误取消任务（v0.7.1 遗留）**。v0.7.1 用「Esc 挂起 90ms，等紧跟的
  坐标序列」来识别泄漏，但 ConPTY 把一次滚轮的泄漏序列拆成多条 win32 输入记录分批
  flush，批次间隔可达几十到几百毫秒，90ms 内等不到 `[` 就当成「用户真按了 Esc」→
  照样取消任务。现在三层兜底（`cmd/mizar/esc_guard.go`）：
  1. Windows 常规确认窗口 90ms → **300ms**；
  2. 只要最近 3 秒内见过泄漏序列（`escLastJunkAt`），确认窗口自动放宽到 **1.2s**，
     且到点时若仍在泄漏就直接放弃取消——宁可让用户再按一次，也不误杀正在跑的任务；
  3. Windows 上 Esc 取消改为**两段式**：第一次 Esc 只提示「再按一次 Esc 取消任务」，
     2.5 秒内再按一次才真正取消。滚轮即使漏判成 Esc，最多出现一行提示，绝不停任务。
     可用 `MIZAR_ESC_CONFIRM=0` 关掉（退回单键取消）。
- **粘贴多行文本「一个字一个字地蹦」**。Windows 的 tcell `EnablePaste` 是空实现
  （没有 bracketed paste），粘贴的每个字符都是独立 `KeyRune`；tview 对每个字符都要
  `TextArea.replace()` → 触发 `changedFunc`（本程序里是 `GetText()` 全文 + `Fields`）
  → 事件循环再 `draw()` 一次全屏（逐行 `WriteConsole`）。几百字就是几百次全文扫描
  加全屏重画，长文本要等很久。现在新增 `cmd/mizar/paste_burst.go`：
  - 与上一按键间隔 < 8ms 判定为粘贴，开始**攒批**；攒批中 < 40ms 的后续字符
    （含粘贴来的换行）继续并入，用户手打（≥30ms）不会误并；
  - 空闲 30ms 后把整段**一次性** `SetText`——`changedFunc` 只跑一次；
  - 攒批期间用 `pasteSkipScreen`（包一层 tcell.Screen）吞掉 `Show()`，并用
    `SetBeforeDrawFunc` 跳过 `root.Draw()`，每键只剩一次内存层 `Clear`；
  - 落盘后用 `QueueUpdateDraw` 重画一次；单次攒批上限 1MB / 2s 兜底防冻结；
  - 快捷键（Alt+K/J、Ctrl+U/D 等）到达时先落盘再立即生效，不会被并入缓冲。
  Unix 有 bracketed paste（一次 `EventPaste` 整段送），默认不启用攒批；
  `MIZAR_PASTE_BURST=1/0` 可强制开关。

- **输入框里方向键变成 `[D` / `[A`（Windows）**。部分 Windows 终端/ConPTY 组合
  不会把方向键翻译成 tcell 的专用 Key，而是把原始 VT 序列逐字符送上来：
  左箭头 = `ESC [ D`。tcell 拆成三个事件（KeyEsc、`'['`、`'D'`），tview 只认 Key，
  于是 `[D` 被当成普通文本塞进输入框；Home/End/Delete/F 键、Ctrl+方向键、
  Alt+字母也一并失效。现在新增 `cmd/mizar/esc_seq.go`：在 InputCapture 最前面
  把 ESC 开头的字符按 ≤120ms 的间隔拼回序列，命中已知编码就合成真正的按键
  （`ESC[A/B/C/D`、`ESC[H/F`、`ESC[1~`…`ESC[24~`、`ESC[1;5C` 带修饰键、
  `ESC O A`…、小键盘 `ESC O q`→`1`、`ESC` + 单字符 → Alt+字符）；
  命中 ConPTY 泄漏的鼠标序列（`ESC[<…M`、`ESC[M…`、`ESC[…M`）则整段吞掉并记录
  「最近在泄漏」；**认不出或半截超时的序列一律按原文补回输入框，绝不吞用户输入**。
  单击 Esc 行为不变（任务运行中仍走两段式确认；未运行时窗口到期后照常交给 tview
  收提示条，代价是 ~130ms 延迟）。Windows 默认开启，`MIZAR_ESC_SEQ=0` 可关闭。

- **聊天区出现「擦不掉的残留文字」**（Windows 尤其明显，屏幕右半边/上半区多出
  几行旧文字）。两类成因都是「有东西绕过 tcell 写终端」，tcell 认为那些格子还是
  自己画的，增量重绘永远不去覆盖：
  1. **绕过 tcell 的直写**：标准库 `log`（插件 hook 报错、供应商持久化失败、
     定时提醒……）写 stderr；`fmt.Printf`（定时提醒横幅）写 stdout；以及 OSC 52
     剪贴板序列 `\x1b]52;c;…` 直写 stderr。现在进入 TUI 后（`cmd/mizar/tui_output.go`）：
     标准 `log` 全部改道诊断日志（不丢信息、不上屏）；定时提醒改走聊天区；
     OSC 52 在 TUI 下不再写（由内部缓冲 + 系统剪贴板承担）。
  2. **CellBuffer 宽度落后于终端实际宽度**（窗口调整后 ConPTY 没送 resize 记录、
     tview 未重排）：此后每次 draw 只覆盖左侧，右侧永远保留上一次更宽窗口的内容。
     新增屏幕看门狗（`cmd/mizar/screen_watchdog.go`）：每 2s 比对 `screen.Size()`，
     变化即整屏强制重画；另在「空闲 ≥5s 且距上次重画 ≥10s」时也重画一次兜底
     （任务运行中/流式输出/拷贝模式一律不打扰）。
  3. **手动兜底**：`Ctrl+L` 或 `/redraw` 随时强制整屏重画（`resyncScreen` =
     重建内容 + `ForceDraw` + `Screen.Sync()`）。任务结束时也会自动 Sync 一次。
- **Ctrl+L / `/redraw` 触发整屏重绘时自锁，TUI 彻底无响应**（`cmd/mizar/tui.go`，
  与 v0.7.3 那次 ask 自死锁同一模式，此处是它第一次出现）。tview 的
  `Application.QueueUpdate` 语义是「投递后**阻塞等待**回调执行完」（`updates <- …` 后
  `<-ch`）。Ctrl+L 的 InputCapture 与 `/redraw` 的 `submitInput` 本身就跑在事件循环
  线程里，它们经 `tuiRequestResync` 再调 `QueueUpdate` —— 事件循环正卡在该回调内，
  永远回不到 `select` 消费队列，双方互等即成死锁：键盘全哑，只能杀进程。
  修复：按调用来源拆成两个入口，语义显式化 —— `resyncRequest()`（事件循环线程内同步
  重绘，tview 允许在直接事件处理与 queued update 中调用 `ForceDraw`）与
  `requestResyncFromGoroutine()`（非事件循环线程，如看门狗，才投递更新队列），
  重绘/排队入口做成可替换字段（`resyncScreenFn` / `queueUpdateFn`）便于测试注入。

### 测试

- `TestSizeChanged` / `TestResyncScreenRebuildsContent` / `TestResyncScreenNilSafe` /
  `TestPasteSkipScreenSyncPasses` / `TestTUIOutputDisciplineRedirectsLog` /
  `TestResyncScreenWatchdogStops`
- `resync_deadlock_test.go`：证明事件循环内请求重绘立即返回、goroutine 路径仍投递队列
  （锁住「Ctrl+L 自锁」这条不变式）。
- `TestEscSeqArrowLeft`（复现 `[D` 问题）、`TestEscSeqAllArrowsAndNav`（16 种编码）、
  `TestEscSeqCtrlArrowModifiers`、`TestEscSeqAltLetter`、`TestEscSeqLeakSwallowed`、
  `TestEscSeqUnknownKeepsInput`、`TestEscSeqArrowWhileIdle`、
  `TestEscSeqTruncatedFlush`、`TestEscSeqDecodeEnabledFor`
- `TestPasteBurstMergesIntoOneWrite` / `TestPasteBurstNewlinesKept` /
  `TestPasteBurstHotkeyNotSwallowed` / `TestPasteSkipScreenSuppressesShow` /
  `TestPasteBurstEnabledFor` / `TestPasteBurstGapWindows`
- `TestEscLeakBroadensWindowOrSuppresses` / `TestEscConfirmTwoStageWindows` /
  `TestEscConfirmExpires` / `TestEscConfirmEnabledFor`；
  原 `TestEscJunkWindowConstant` 改为按平台断言（Windows 窗口必须宽于 Unix）。

## [v0.7.1] — 2026-11-06

> 主题：**手感修复**。不改功能，只让两个天天会碰到的交互不再烦人：
> Windows 滚轮不再误取消任务，长任务跑着的时候输入框不再发涩。

### 版本

- `version` 提升到 **v0.7.1**（`cmd/mizar/main.go`），主页 `index.html` 版本徽章与
  最新动态同步（`/checkupdate` 读的就是这两处）。

### 变更

- **发布资产一律 xz 压缩**：内嵌 Zig 工具链后未压缩产物 126/119/168 MB，
  上传到 GitHub 时经常被 RST 或触发「60 秒无数据」超时。现在 `make release-xz`
  构建后自动 `xz -T0 -6`（→ 74/68/110 MB），CI workflow 也只上传 `.xz` 并清理
  release 上同名的裸二进制。下载后 `xz -d` 即可。

### 修复

- **Windows 滚轮不再误取消任务**：ConPTY 会把泄漏的鼠标事件还原成字符流
  （`ESC[<35;66;61M`），tcell 把它开头的 `ESC` 当成真正的 Esc —— 于是在聊天区滚
  一下滚轮就显示「任务已取消（当前步骤结束后停止）」，观感像按了 Esc 暂停。
  现在 Esc 先挂起 90ms 确认：紧跟坐标序列（含 tcell 合成的 `Alt+[`）则整段吞掉、
  任务继续跑；确认没有序列才真正取消（`cmd/mizar/esc_guard.go`）。
- **长任务运行时输入框卡顿**：密集的后台输出会让聊天区每 60~100ms 全量重绘一次。
  现在 ① 最近 30 条消息的 Markdown 渲染结果按内容缓存，不再每次重跑 glamour；
  ② 状态栏「上下文占用」按消息条数缓存，不再每次遍历全部消息重算 token；
  ③ bash 实时输出重绘合并，队列里最多一次待处理刷新。
- 模型选择器 `j`/`k` 移动方向与注释/测试不一致（j 向下、k 向上）。

### 性能（长任务运行时输入框不再发涩）

- **Markdown 渲染结果按内容缓存**：任务运行中聊天区每 60~100ms 全量重绘，
  此前每次都重跑 glamour。基准实测 30 条 2KB 消息 **52.7ms/次 → 1.28ms/次**，
  稳态（缓存命中）整体 `renderAllDirect` **0.49ms**。
- **状态栏「上下文占用」按消息条数缓存**：500 条消息下 **4.82ms → 1.4µs**（3455x）。
- **全量重绘请求合并**（`renderPending` 原子标记）：钩子/流式/bash 三路来源不再各自
  排队，事件循环里最多一个待处理重绘；spinner 也不再与重绘争队列。
- **性能回归测试**：`TestRenderPerfBudget`（稳态 <10ms 硬门槛）、
  `TestStatusBarCacheEffect`（断言缓存生效并打印倍数）、
  `TestRenderRequestCoalescing`，以及 `BenchmarkRenderAllDirectCold/Cached`。

## [v0.7.0] — 2026-11-05

> 主题：**把「计算」和「编译」都装进一个文件**。两条新路径（Zig→wasm 计算内核、
> 内嵌 Zig 工具链）指向同一目标——内网离线机器上，除了拷一个二进制，什么都别装。
> 全程保持 `CGO_ENABLED=0`、纯静态单文件、跨发行版（CentOS 7 / glibc 2.17）部署。

### 版本

- `version` 提升到 **v0.7.0**（`cmd/mizar/main.go`）。主页 `index.html` 的版本徽章同步
  （`/checkupdate` 读的就是这个 badge）。

### 发版矩阵（每个二进制只带自己那份归档）

| 产物 | 大小 | 内嵌 Zig 归档 |
|---|---|---|
| `mizar-linux-amd64` | 126 MB | `zig-x86_64-linux-0.17.1.tar.xz`（55 MB） |
| `mizar-linux-arm64` | 119 MB | `zig-aarch64-linux-0.17.1.tar.xz`（51 MB） |
| `mizar-windows-amd64.exe` | 168 MB | `zig-x86_64-windows-0.17.1.zip`（96 MB） |

不内嵌 Zig 的裸二进制为 **74 MB**（tar.gz 23 MB）。

### 升级提示

- Zig 归档属构建产物，`internal/zigboot/zigdist/` 已被 `.gitignore` 忽略；
  **未内嵌时二进制照常构建**，只是 `/zig` 会提示未内嵌。
- 发版请用 `make release`（自动按平台重排 `zigdist/`），不要手工往目录里塞三个包——
  否则任何架构的二进制都会多背 100 MB。
- 运行时只释放到 `~/.cache/mizar/zig/<平台>/`，**不写 `/usr/local/bin`、不建系统软链接**；
  需要 PATH 里敲 `zig` 自行 `export PATH=...`。

### 详细变更

### ✨ 新增（形态 A 补齐 + 形态 B 脚手架）

- **形态 A 补齐**：插件现在可以通过宿主函数 `wasm_invoke` / `wasm_invoke_b64` /
  `wasm_kernels` 调用任意符合「通用 ABI」的 Zig wasm 内核（`internal/engine/wasmhost.go`），
  不再局限于内置的 `count_tokens`/`bigram`。搭配 `extensions/wasm_demo.ts` 示例插件与
  `internal/plugins/wasm_demo_test.go` 端到端测试（含旧版宿主缺失时的降级验证）。
- **形态 B 脚手架**：`/zig new-mcp <dir>` 生成零依赖 Zig MCP server 骨架
  （按行 JSON-RPC 2.0，已实现 `initialize`/`tools/list`/`tools/call`，含 `echo` 与
  `hostname` 示例工具）；`/zig new-wasm <dir>` 生成 wasm 内核骨架。两者内嵌在
  `internal/zigboot/templates/`，离线可用。
- **文档**：`docs/zig-plugin-forms.md` 对比两种形态，并逐层解释「Zig 二进制为什么不能
  像 .ts 插件那样热加载」以及 `/mcp reload` 为何是「重编译 + reload」级别的近似热加载。

### ✨ 新增（内嵌 Zig 工具链 + `/zig` 命令：内网现场编译）

- **`internal/zigboot`**：把官方 Zig 归档用 `go:embed` 内嵌进二进制，运行时按需释放到 `~/.cache/mizar/zig/<平台>/`。目标机拷一个二进制即可**现场编译 C/Zig**（`zig cc` 也是 C 编译器），产物是独立可执行文件，再由 `bash` 工具以 `exec` 调用——**不涉及 cgo，`CGO_ENABLED=0` 与纯静态单文件部署模型不变**。
- **原子解压**：先解到 `<目标>.tmp`，校验 `zig` 可执行文件存在且置可执行位后再 `rename` 覆盖。避免中途失败留下半个 zig 被幂等短路误判为「已就绪」（实测踩过：EOF 中断后残留残次文件）。
- **`/zig` 命令**：`/zig` 看状态（版本/是否内嵌/是否已释放/缓存路径）、`/zig init` 释放（幂等，`-f` 强制重放）、`/zig path` 打印路径、`/zig clean` 清运行时缓存。
- **`scripts/fetch-zig.sh`**：按架构下载官方归档，**缓存优先**（`~/.cache/zig`，命中且 shasum 校验通过则不联网，内网友好）。支持 `--embed`（复制进仓库供内嵌）、`--arch`、`--version`、`--offline-check`、`--clean`。
- **`make zig-fetch / zig-embed / zig-clean`**：构建期辅助目标。归档目录 `internal/zigboot/zigdist/` 被 `.gitignore` 忽略（构建产物，约 55~60 MB）；**未内嵌时二进制照常构建**，只是 `/zig` 提示未内嵌。
- **一包一架构：每个二进制只嵌自己那份 Zig**。`go:embed all:zigdist` 是目录级的，无法按 `GOARCH` 条件排除，两个归档同时存在会让任何架构的二进制都多背 50 MB。新增 **`scripts/zigdist-select.sh`**（`make zig-select PLAT=...`）在每次构建前重排 `zigdist/`；`make release` 自动串好：linux/amd64 只嵌 `x86_64-linux`、linux/arm64 只嵌 `aarch64-linux`、windows/amd64 只嵌 `x86_64-windows`。另加 `make release-amd64 / release-arm64 / release-windows` 单平台目标。
- **Windows 也支持**：`.zip` 归档走 `archive/zip` 解压（与 `.tar.xz` 同一套剥顶层目录逻辑，`zig.exe` 落在解压根）。顺手修掉 `zip.NewReader(strings.NewReader(string(data)))` 白拷一份 95 MB string 的浪费，改用 `bytes.NewReader`。
- **不污染系统 PATH**：只释放到 `~/.cache/mizar/zig/<平台>/`，**绝不写 `/usr/local/bin`、不建系统级软链接**（内网常多用户/无 sudo/受限容器，且可能并存多个 Mizar 版本）。需要直接敲 `zig` 时自己 `export PATH=...` 即可。释放目录可用 `MIZAR_ZIG_CACHE` 整体改址（也用于测试隔离——早前 `go test` 并行跑不同包时，`Clean` 会撞上正在解压的 `.tmp` 报 `directory not empty`）。
- **为什么不是随包带 gcc**：内网机器自带 gcc 常很老（CentOS 7 为 4.8.5），且交叉编译产物会绑定编译机 glibc 版本；「产物为独立进程、exec 调用」让 **glibc 天然一致**（现场编现场跑）。Zig 单包自带 C 编译器与各版本 glibc stub，一个包覆盖全平台交叉（`zig cc -target x86_64-linux-gnu.2.17`）。

### ✨ 新增（Zig→wasm 计算内核基础设施）

- **`internal/wasmkern`**：在进程内运行 Zig 编译的 WebAssembly 计算内核。运行时用 wazero（**纯 Go，无 cgo**），因此 `CGO_ENABLED=0`、纯静态单文件、跨发行版部署、Windows 交叉编译等前提**全部不变**。实测二进制增量 +2684 KB（67.85 → 70.47 MB，+3.86%），Windows/arm64 交叉编译均正常。
- **内嵌 + 惰性编译 + 实例池**：`.wasm` 用 `go:embed` 随二进制发布，运行时无需外部文件；编译结果进 `CompilationCache` 多实例共享；模块实例放 `sync.Pool`（wazero 实例非并发安全）。冷启动实测 2–4ms，单次调用固定开销约 3µs。
- **两段式调用 API**：`Load(data)` 一次写入线性内存 + `CallLoaded(fn, ptr, len)` 反复计算，避免大输入重复拷贝。线性内存按 64KiB 页自动扩容，已测 4MB 级输入。
- **`kernels/count_tokens.zig`**：与 Go 参考实现逐字节同口径的 token 估算内核（CJK 按字符、其余 4 字符/token），含 UTF-8 解码与边界码点处理，产物 6.3 KB。
- **宿主函数接入**：`count_tokens` 现在优先尝试 wasm 内核，失败自动回退纯 Go，功能零风险。
- **`make zig-kernels`**：重编内核的辅助目标。产物已提交，**正常构建/发布不依赖 zig**。
- **开关**：`MIZAR_WASM_KERNEL=force` 强制走 wasm、`=0` 全部禁用。

### ✨ 新增（`bigram` 分词内核 + 记忆检索批量打分）

- **`internal/wasmkern/bigram.zig`**：零分配 bigram/整词分词内核（11.9 KB）。token 以 `(offset,len)` u32 对输出，指回线性内存里的输入缓冲区，不复制内容；去重 + 停用词过滤在内核内的「代际戳」哈希表完成（每次调用只 `cur_gen++`，不清表），语义与 `memory.ts` 的 `tokenize()` 逐 token 一致。
- **修复内核静态数据边界 bug**：`kernel_data_end()` 原先只覆盖两张哈希表，漏算了排在后面的 `cur_gen` / `inited`，导致 Go 侧写输入时把它们踩掉——表现为**第一次调用正常、之后每次返回 0**（并发场景全失败）。现改为对所有全局变量取最大值，并加了回归测试。
- **`internal/engine/memscore.go`**：新增两个纯函数宿主函数
  - `mem_tokenize(text)`：按 memory 插件规则分词并用空格拼接（CJK unigram+bigram、ASCII 整词、停用词过滤、去重）
  - `mem_score(query, docs)`：对一批记忆的 tokens 列**批量**打分，一次跨边界返回分数数组
- **`memory.ts` 改造**：`mem_write` 的 tokens 生成走 `mem_tokenize`，`mem_search` 的打分循环走 `mem_score`；宿主函数缺失时自动降级回本地 `tokenize()`/`similarity()`，老宿主上行为不变。
- **开关统一**：`MIZAR_WASM_BIGRAM` 的判定收敛到 `wasmkern.BigramAvailable()`，宿主与上层只有一个真相来源。新增 `MIZAR_WASM_VERIFY=1` 做内核/Go 交叉校验。

### 📊 实测结论（记忆检索打分）

2000 条真实记忆（每条 tokens 约 120 字符），查询「中文分词记忆检索的性能测试」（i7-10700）：

| 路径 | 耗时 | 分配 |
|---|---|---|
| goja 里逐条 `similarity()`（改造前） | **102 ms** | 372k allocs |
| 宿主函数 `mem_score` 批量打分（改造后） | **10.8 ms** | 4.2k allocs |

**9.4×，且收益来自「把循环搬出 goja」而不是 wasm。**

分词两条路径对比（同一算法，纯 Go vs wasm 内核）：

| 输入规模 | 纯 Go | wasm 内核 |
|---|---|---|
| 1 段（约 120 字） | 9.5 µs | 37.6 µs |
| 3 段 | 28 µs | 69 µs |
| 10 段 | 79 µs | 127 µs |

**任何尺寸下内核都更慢**——要把整段输入写进线性内存再读回 span，两次拷贝的开销超过 Go `for range string` 的切分。因此 `memTokenize` 默认走纯 Go，内核仅在 `MIZAR_WASM_BIGRAM=force` 时启用（用于对照与压测）。

### 📊 实测结论（避免误判，记录在此）

「把纯计算下沉到 wasm 一定更快」是错觉。同一算法三方对比（i7-10700）：

| 实现 | 100KB 混合文本 | 说明 |
|---|---|---|
| goja 里 JS 循环（插件现状） | **95.4 ms** | 831556 allocs，真痛点 |
| wasm 内核 | **368 µs** | 比 goja 快 **259×** |
| 纯 Go 原生 | **137 µs** | 比 wasm 还快 2.7× |

- 跨边界调用 + 线性内存拷贝的固定成本（约 5µs/次）在小输入上完全吃掉语言差距：9 字节输入纯 Go 13.8ns vs wasm 5.2µs（差 370×）。
- 因此 `count_tokens` **默认走纯 Go**（`wantKernel()` 默认 false），wasm 通路仅在 `force` 模式下用于验证与压测。
- 真正的收益场景是「goja 里的重计算」——那是 **259×** 的差距。后续候选内核（`seg_cut` 大词典分词、严格 JSON 校验）须先用 `bench_test.go` 量化 Go 侧确实慢再下沉。

### 🧪 补充实测（wasm 除启动外的影响面，详见 [docs/zig-wasm-eval.md](zig-wasm-eval.md)）

回答「独立 wasm 是不是只慢在启动」这个问题——**不是**。逐项实测：

| 影响面 | 实测 | 严重度 |
|---|---|---|
| 单次调用 | 纯 Zig 原生 1.42 µs / 纯 Go 7.70 µs / **wasm 16.5 µs** | 🔴 慢 2–5× |
| 边界成本占比 | 空调用 132 ns + `mem.Write` 30B 9 ns，只占 16.5 µs 的 **0.8%** | — |
| 批量 2000 条 | wasm 96.6 ms vs Go 56.1 ms（**1.72× 慢**），但分配 18.3k vs 338k（**少 18×**） | 🔴/取tradeoff |
| 内存 | 单实例线性内存 **1152 KB**；64 并发池化峰值 ≈ 70 MB | 🟡 |
| 延迟稳定性 | 稳态 23 µs → `runtime.GC()` ×2 → **首次 2.06 ms** → 回 15 µs | 🟡 |
| 实例化 | 全新 runtime+实例化 **2.45 ms/次**（`CompilationCache` 省编译但省不了实例化） | 🟡 |
| 可调试性 | 无 Go 栈、pprof 全归 `wasmcall`、dlv 不能单步、改逻辑须重编 | 🔴 |
| 能力边界 | `wasm32-freestanding` 无 IO/无线程/栈受限/无 SIMD | 🟡 |
| 安全 | 越界写触发 trap（不再静默踩内存） | ✅ 唯一硬收益 |

**关键结论：16.5 µs 里只有 0.14 µs 是跨边界，剩下全是 Go 侧封装搬运成本**——
为安全封装付的胶水钱比 wasm 调用本身贵 100×。所以换内核写法救不了，
要么接受、要么别在这条热路径上用。

因此 `bigram` / `count_tokens` 内核**默认走纯 Go** 的决策经得起数据检验：
wasm 该用在「不可信计算 + 低频 + 单次 >10ms」的窄交集里（如用户上传的
沙箱计算逻辑、大词典分词），而不是每次记忆检索都跑的热路径。

### 📝 备注

- `cmd/mizar` 的 `TestModelPickerMoveKeysHandled` 在改动前后**均失败**（`k 后选中 = "m3", want m1`），与本变更无关，属既有问题。

---

## [v0.5.9]

### ✨ 新增（`!` 本地 bash 直执行）

- **输入框以 `!` 开头即本地 bash 直执行**：命令不经模型、不触发工具调用，`bash -c` 运行（30 秒超时，stderr 合并到 stdout），结果与错误都展示出来。
- **命令与输出纳入对话历史**：写入 `agent.Initial` 与会话文件（user=`!cmd`、assistant=`$ cmd\n<输出>`），下一轮模型能看到这次操作，可以基于结果继续推理。CLI / TUI 两条路径都覆盖。
- **TUI 异步渲染**：命令立即以 user 角色显示，输出以 `cmd` 角色异步追加（`cmd` 角色保留原始换行，不按 markdown 重排）。
- **空命令保护**：单独输入 `!`（或 `!   `）提示用法，不执行任何东西。
- **典型用途**：`!ls -la`、`!git status`、`!cat config.json | head -20`、`!df -h` 等快速查环境的命令；需要模型基于输出推理时直接在下一条消息里发任务即可。
- **banner 提示同步**：TUI 启动提示新增「本地 bash：!开头直接执行」一行。
- **实现**：`cmd/mizar/helpers.go` 新增 `runUserBash`（`bash -c` + 30s 超时 + env 继承）与 `buildBashEchoContent`（命令回显统一格式）；`cmd/mizar/interactive.go` 经典 readline 分支 + `cmd/mizar/tui.go::submitInput` 顶部拦截（在 `!` 判断里 `return`，避免被后续路径当成任务或 steer 注入）。

---

## [v0.5.8]

### ✨ 改进（Alt+C 拷贝模式重做）

- **支持跳行选不连续行**：`↑/↓` 移光标 → `v/空格` 标记/取消当前行（`✓` 显示）→ 跳到另一行再 `v` 累加，`y` 复制所有标记行。之前必须连续区间。
- **范围选区显式激活**：`r` 进入范围模式（锚点=当前行），`↑/↓` 扩展连续选区，`y` 复制。`y` 不再误触发范围复制——必须显式按 `r`，避免一按就把整屏复制走。
- **状态栏立即刷新**：复制成功后前后各调一次 `statusBarDirect()`，不再等下一帧。
- **↑↓ 不再误改输入框**：拷贝模式下 ↑/↓ 交给 `copyMove`，不触发历史浏览。
- **Ctrl+Q 统一复制**：输入框有选区时复制选区，无选区时复制最近一条 AI 回复（与 Alt+L 同路径）。
- **banner 提示更新**：复制粘贴行改为「Alt+C 拷贝模式 / Alt+L 复制最近回复 / 输入框 Ctrl+Q 复制 Ctrl+V 粘贴」。

---

## [v0.5.7]

### 🐞 修复（空工具名导致上游 11133）

- **根因**：模型丢失 `tool` 字段且 args 里无法猜出工具时，旧实现仍继续执行并把 `ToolCallRef{ID, Name:"", Args}` 写进历史；编码层原样发出 `tool_calls[].function.name == ""`，上游一律拒绝——workbuddy 回通用 `11133`（根因被网关吞掉）、stepfun/agnes 回 `tool_calls.function.name is required`。
- **源头拦截**（`agent/loop.go runOneToolCall`）：空工具名且猜不出时不再往下走，直接注入「第 i/n 个工具调用缺少工具名」纠正提示让模型重发。与既有空参数兜底同形态（普通 assistant 文本 + user 纠正，不标 `KindToolCall`，避免触发另一类 `tool calls and tool results do not match` 400）。
- **编码层兜底**（`llm/openai.go`）：`encodeToolCallMsg` 过滤空名调用（无法表达则不声明）；`pairedCallIDs` 同步排除空名调用，否则其结果会以孤立 `role:"tool"` 发出。
- **信息不丢**：空名调用对应的工具结果降级为普通 user 文本，历史仍可读。
- **测试**：`internal/llm/empty_toolname_test.go`（空名不发 / 混合场景只保留合法调用 / 结果降级不丢失）、`internal/agent/empty_toolname_test.go`（循环内不写空名 `ToolCallRef` 且注入纠正提示）。

### ✨ 新增（自定义 User-Agent）

- **`/config user_agent <字符串>`**：设置自定义 User-Agent，立即生效并持久化到 `~/.mizar/config.json` 的 `user_agent` 字段（支持含空格，如 `/config user_agent MyApp/1.0 (contact: me@example.com)`）。
- **清除**：`/config user_agent -`（或 `reset`/`default`）恢复 Go 默认 UA，并同步落盘。
- **全路径覆盖**：`internal/llm` 的 6 处出站请求统一经 `applyUA` 注入（ChatWithTools 非流式/流式、AIChat、AIChatStream、Chat、SummarizeMessages），未设置时不覆盖 Go 默认。
- **加载/同步**：启动时从配置注入；`/reload` 重新应用；`/save`、`/provider`、`/model`、`/apikey` 的持久化段同步 `user_agent`，不会因改其它项而丢失。

### 🐞 修复（压缩阈值）

- **`/config context_window` 改小窗口后压缩阈值不同步**：补齐与启动一致的 `KeepRecentTokens` 下限兜底（≥500），避免 keepRecent 过小导致压缩后仍超阈值、反复触发。

---

## [v0.5.6]

### ✨ 新增（`/checkupdate` 版本检查）

- **`/checkupdate`**：从项目主页 `https://icefairy.github.io/mizar/` 抓取最新版本号与更新内容，与当前版本比对后给出结论。
- **只提醒不自动更新**：明确告知用户需自行替换二进制，命令本身不下载、不修改任何文件。
- **三种结论都有明确输出**：发现新版（含标题 + 正文摘要 + 下载地址）、已是最新、本地版本更新（开发版）。
- **降级安全**：网络不可达、HTTP 非 200、站点结构变更导致解析不出——一律返回友好提示并附站点地址，绝不静默也不影响其他功能。
- 实现零新依赖：仅用 `regexp` 解析站点固定的两个位置（`class="badge"` 徽章、最新动态首条 `<h3>`+`<p>`），20 秒超时保护。

---

## [v0.5.5]

### ✨ 新增（`/chat` 裸聊）

- **`/chat <内容>`**：不注入系统提示词、工具定义、技能索引、会话历史，只把这句话直接发给当前模型，沿用当前 **模型 + 思考等级**。适合临时问答、模型自检、对照「无提示词」下的原始回答。
- **不污染会话**：不走 `Agent.Run()`、不读也不写 `a.Initial`、不落盘。纯旁路，聊完即忘。
- 实测对照：`/chat 你知道自己是什么系统吗？` → 模型回答自身原生身份（混元 / 腾讯），而非 Mizar，确认提示词注入为零。

---

## [v0.5.4]

### ✨ 新增（上下文自愈）

- **上下文溢出自动自愈**：当 `context_window` 配置得比模型真实上限大时，任何消息都会持续撞 400（最大上下文超限），此前只能手工删会话。现在检测到溢出后按 **10% → 20% → 40% → 60%** 递进裁剪并自动重试，最多四档；裁剪顺序为「超大工具结果（>4000 token，置占位保留调用配对）→ 最老的消息」，`system` 与最新消息永远保留。
- **自愈结果落盘**：裁剪后写入一条 `{"type":"compaction","emergency":true,...}` 记录，下次 `Load` 直接从保留起点重建上下文——用户退出再进来不会重新撞一次 400。原始消息一行不删，导出/查看仍是完整历史。
- **界面明确提示**：裁剪时输出「⚠ 上下文超限：已临时裁剪 N% 历史消息（保留 M 条）」，不静默降级。

### ✨ 新增（压缩持久化）

- **压缩记录落盘（对齐 pi 的 CompactionEntry）**：此前压缩摘要只存在于进程内存，会话文件仍是全量历史，导致**每次续接都重新压缩一遍**、压缩成果跨进程归零。现在压缩发生时追加一条 `{"type":"compaction","summary":...,"first_kept_index":N,"tokens_before":M}` 记录，加载时重建为「摘要 + 保留范围 + 压缩后新增消息」。
- **原始对话不丢**：压缩是**追加记录**而非改写历史，JSONL 中每一行原始消息都保留；新增 `Store.LoadRaw()` 供导出/诊断读取完整历史。
- **旧会话向后兼容**：无 `type` 字段的行仍按普通消息解析，存量会话无需迁移。

### 🔧 工具链

- **`--no-mcp`**：跳过 MCP registry 初始化，`mcp_call` 不可用（对齐 pi 的 `--no-mcp`）。
- **工具禁用支持 glob**：`SetDisabledTools` 接受 `mcp__redis__*`、`*` 等通配符，一次关掉某个 MCP server 的全部工具。
- **摘要输出预算跟随模型上限**：`EffectiveSummaryTokens` 取代硬编码 1500（默认 1500、封顶 8000、取模型 output limit 一半），修掉推理型模型把预算耗在 reasoning 上导致 `generation hit the token cap` 的问题；新增 `config.max_output`。

---

## [v0.5.3]

### ✨ 新增（时间感知）

- **会话消息带时间戳**：`~/.mizar/sessions/**/*.jsonl` 每条消息新增 `timestamp`（RFC3339 毫秒，本地时区），在 `Store.Append` 单点自动补写，所有调用方（TUI / interactive / `-task`）零改动；已设置则不覆盖。Web UI 会话（`~/.mizar/webui.json`）同步增加 `ts`，旧数据加载时继承会话创建时间兑底，消息气泡展示时间（当天只显时分，跨天带日期）。
- **模型能感知时间间隔**：长会话常常隔一天才回来继续，此前模型看不到任何时间信息，容易把隔天续接当成刚才的连续对话。现在 `Agent.Run` 每轮注入一条 system「当前实时时间」作为准确基准；历史最后一条消息距现在超过 **1 小时**时额外插入【时间提示】，显式说明已过去多久（如「3 天 2 小时」）并提示不要假定上下文连贯、涉及时效/文件状态/服务状态先重新核实。

### 🔧 设计取舍

- 当前时间**不进** `SystemPrompt()`：那里有系统提示词缓存且属于 prefix cache 区，逐轮变动会打穿缓存。改为作为增量追加在 system 之后。
- 注入只作用于本次请求的 msgs 副本，**不写回** `a.Initial`，避免时间提示逐轮累积。
- 短间隔（< 1h）静默：一问一答是常态，逐条提醒只是噪声。
- 无时间戳的旧会话不编造提示，行为与之前完全一致（存量会话无需迁移即可续接）。

---

## [v0.5.2]

### 🐛 修复（工具调用配对断裂 → 上游 400）

- **长会话中途 400「tool calls and tool results do not match」**：部分上游（实测 codebuddy，错误码 11148）严格要求每个 `tool_calls` 都有 `role:"tool"` 回应。触发路径是两条**纠正提示**分支——工具参数为空（`args` 为 `""`/`{}`）、`respond` 缺 `text`——它们注入的 assistant 消息被误标为 `tool_call` 却**没有调用记录、也没有工具结果**，编码层又从内容里还原出一个调用，于是发出「有调用无结果」的非法序列。
- **根因修复**：这两处纠正提示不再标记为工具调用（它们是提示，不是调用）。
- **编码层兜底不变式**：新增配对预扫描，**只有确实会收到结果的调用才写入 `tool_calls`**；反向孤儿调用降级为纯文本，绝不发出孤儿调用。旧会话（工具结果缺 `tool_call_id`）按出现顺序回退配对，不再整段降级丢上下文。
- **兼容存量会话**：26 个历史会话文件存在同类残留，修复后无需手工清理即可正常续接（57 个会话全部校验通过）。

### 🐛 修复（/model 选择器按键）

- **按 Enter 无反应、清单不消失**：选择器按键判断排在「Enter 发送」之后，而选择器打开时输入框已被清空 → 发送空消息被直接忽略，既不确认也不关闭。现已前置到所有 Enter/↑↓ 处理之前（含粘贴换行防护与历史浏览），`↑↓` 不会再被历史浏览吃掉，`Esc`/`q` 也能正常关闭。
- 选择器确认时命令注册表不可用不再 panic，改为明确报错。

---

## [v0.5.1]

### ✨ 新增（/model 上下键选择器）

- **TUI 下 /model 直接弹清单**：旧版只打印清单、还要手打一遍模型全名（长名字错一个字符就是 404）。现在输入 /model 后台拉取清单并弹出选择器，**↑/↓ 或 j/k 移动、Enter 确认、Esc/q 取消**；当前模型标 `*`，光标初始就落在当前模型上，超长清单有滚动提示。
- **唯一前缀匹配**：/model <名称> 支持少打几个字符的模糊匹配；歧义时宁可不切。
- 经典 readline / Web UI / `-task` 模式行为不变（无参仍打印清单）。

### 🐛 修复（TUI 渲染）

- **/help 换行丢失**：命令输出是纯文本清单却被塞进 markdown 渲染（glamour），被按段落重排合并换行、把 `<name>` 当 HTML 标签吞掉。命令输出改用独立 `cmd` 角色渲染（不走 glamour）。
- **末尾几行丢色**：正文里的 `[...]`（如描述里的 `[args]`）被 tview 当颜色标签解析，重置颜色状态后后续行全部变默认色（清单是随机序，所以丢色范围不固定）。user/bot/cmd/err/tool 正文统一 `tview.Escape` 后再上色。
- **两个非法 tview 色名**：`cyan`/`magenta` 不是合法色名，工具行/状态栏分隔符/等待进度一直渲染为默认色，已改为等价的 `teal`/`fuchsia`。

### 🔧 改进

- **token 估算非 CJK 从 4 字符/token 收敛到 3.5**（对齐 pi）：4 会低估 ASCII 密集内容（日志/JSON/源码）的 token，导致压缩触发偏晚、请求撞上下文上限。
- **/model 配置写入失败不再静默**：原来只 log 仍返回成功（切换成功但没落盘，重启静默回退），现在返回错误。

---

## [v0.5.0]

### ✨ 新增（插件启用/禁用/重载）

- **插件级启用/禁用**：新增 `/list_plugins`（列出插件及状态：✅ 已加载 / ⛔ 已禁用 / ⚠️ 加载失败）、`/disable_plugin <名>`（立即卸载并持久化到 `~/.mizar/extensions/.disabled.json`）、`/enable_plugin <名>`（取消禁用并加载）、`/reload_plugins`（重载全部，已禁用的保持禁用）。
- **持久化禁用清单**：禁用状态写入插件目录下的 `.disabled.json`，重启后依然生效；`/list_plugins` 支持省略扩展名（`memory` → `memory.ts`）。
- **用途**：一个二进制可同时满足多种记忆后端——内置本地 SQLite 版开箱即用，需要与其它 agent 共用记忆时，`/disable_plugin memory.ts` 换成自己的（如 HMEM 版）即可。

### ✨ 改进（记忆插件自托管化 + HMEM 版内置）

- **`memory.ts` 改为自托管 SQLite**：数据存在本地单文件 `~/.mizar/memory/mem.db`，**零外部服务、零依赖**，随二进制发布即用。此前是 HMEM C/S 版，离不开独立部署的 HMEM Server，不适合随包发布。
- **内置 HMEM 服务端版 `memory_hmem.ts`**：连接 [HMEM Server](https://github.com/icefairy/hmem_hermes_agent)（向量+关键词混合检索、反思引擎、知识图谱），实现**多 agent 共用一套记忆**。默认**禁用**（需自建服务），用 `/disable_plugin memory.ts` + `/enable_plugin memory_hmem.ts` 切换。两个插件导出同名 `mem_*` 工具，同时启用会冲突——加载器会打印冲突警告。
- **中文分词检索**（SQLite 版）：中文按**二元组（bigram）**切分 + 单字，英文/数字按整词，过滤停用词；用「覆盖率 75% + 精确度 25%」加权打分（按 token 长度加权），返回相似度最高的若干条（**默认 3 条**）。工具：`mem_write`/`mem_search`/`mem_list`/`mem_get`/`mem_delete`/`mem_stats`/`mem_clear`。
- **新增宿主函数 `env(name)`**：插件可读环境变量定位 `HOME`，不再硬编码 `/root`（`db.ts`、`image_vision.ts`、`memory_hmem.ts` 均用）。

---

## [v0.4.9]

### ✨ 新增（Markdown 渲染补全）

- **表格**：支持 GFM 表格（`| a | b |` + 分隔行），含左/中/右对齐（`:---`/`:--:`/`---:`）、表头样式、斑马纹、横向滚动。
- **删除线** `~~x~~`、**高亮** `==x==`、**图片** `![alt](url)`。
- **任务列表** `- [ ]` / `- [x]`（渲染为勾选框）。
- **嵌套列表**：无序/有序列表支持缩进嵌套（含混合嵌套）。

### ✨ 新增（内置插件与技能随二进制发布）

- **内置资产自动释放**：`assets/` 下的默认插件与技能用 `go:embed` 内嵌进二进制，**首次运行时自动释放**到 `~/.mizar/extensions` 与 `~/.mizar/skills`。**只补缺、不覆盖**（已存在的文件一律跳过，保护用户修改）；`--no-assets` 可关闭。
- **内置数据库插件 `db.ts`**：PostgreSQL / MySQL / SQLite 常用增删改查——`db_conn`（连接配置管理）/`db_tables`/`db_schema`/`db_query`/`db_exec`/`db_insert`/`db_update`/`db_delete`/`db_count`/`db_close`。连接信息由用户或其他技能提供（`~/.mizar/db_connections.json` 或内联 dsn）。`db_update`/`db_delete` 强制 `where`（防全表误操作）。
- **内置图片识别插件 `image_vision.ts`**：多模态模型识别图片（通用版，从 `~/.mizar/config.json` 读配置）。
- **内置技能**：`handoff`（交接）、`memory`（全局记忆）、`database`（数据库操作）。

### ✨ 改进（技能支持子目录）

- **技能加载支持子目录布局** `<name>/SKILL.md`（可携带 `references/` 等附属文件），与顶层单文件 `<name>.md` 兼容；同名时子目录优先。此前 `skill_manager` 工具按子目录写入，加载器却只认顶层文件——现在两者一致。

---

## [v0.4.8]

### ✨ 改进（Agent 模式过程展示）

- **工具调用分组折叠**：Agent 模式一轮对话现在组织为「用户提问 → 工作过程（默认折叠）→ 最终答复」三部分。思考、工具调用、工具结果全部收进「工作过程」分组，摘要栏显示步数（如「工作过程 · 7 步」），点击展开查看。最终答复独立成气泡，便于快速定位。
- **紧凑单行展示**：工作过程内每条工具调用压缩为单行（工具名 + 参数预览，超长省略），结果也只显示单行预览，不再产生大片空白。
- **去除空气泡**：工具返回空白内容时不再渲染空的「工具」「结果」气泡。

### 🐛 修复

- **多行内容挤成一行**：用户发送的带换行消息在气泡内被折叠为空格挤在一起；现用户消息气泡 `white-space: pre-wrap`，换行正常保留。

---

## [v0.4.7]

### ✨ 新增（Web UI 体验增强）

- **侧栏锁定常驻**：侧栏新增 📌 按钮，锁定后 PC 端侧栏常驻不被自动收起（状态存 localStorage）。
- **暗黑 / 明亮主题切换**：顶栏 🌙/☀️ 按钮一键切换，CSS 变量实现；首次访问跟随系统偏好，选择持久化。
- **快捷参数行**：输入框上方新增「模型 + 思考等级」快捷行，改完即时保存生效，无需进设置面板。
- **最大历史条数设置**：新增 `max_history`（默认 50），限制**普通对话模式**最多发送的历史消息条数，避免长会话无限追加导致出错；**Agent 模式不受此限制**（走完整工具循环）。
- **Markdown 渲染**：对话条目支持各级标题（h1-h6）、代码块（带语言标签 + 独立复制按钮）、行内代码、粗体/斜体、列表、引用、链接、分隔线。
- **逐条复制**：每条消息（用户与 AI）均有「复制」按钮；代码块另有独立复制按钮。

### 🎨 改进

- **消息左右对齐**：代表用户的「我」靠右，代表 AI 的「开阳」靠左。
- **输入框 5 行高度**：输入框默认 5 行，超出纵向滚动；全站滚动条美化（细圆角、主题配色）。

### 🐛 修复

- **思考框光标一直闪烁**：有思考的模型输出时，思考块的光标在正文开始后仍持续闪烁，且发新对话也不消失。现在正文开始即停止思考块光标，本轮结束清除全部光标状态。

---

## [Unreleased]

### 💥 破坏性变更

- **移除 `todo_write`，保留 pi 式 `todo`**：两者此前**同时注册**（`todo, todo_write`），语义不同（整表替换 vs 动作式 API）、状态各自独立、`/todos` 只读后者——模型随机选一个，造成待办状态与实际展示不一致。现只保留 `todo`（list/add/toggle/clear，状态随会话持久化并回放重建）。依赖 `todo_write` 整表替换语义的场景请改用逐项 `add`/`toggle`。

### ✨ 新增

- **诊断日志（排查「任务莫名中止/卡死/退出」）**：新增 `internal/diag` 包，把关键事件落盘到 `~/.mizar/logs/mizar-YYYYMMDD.log`（单文件超 8MB 自动停写）。默认开启（TUI 下 stderr 就是终端，诊断信息不能写那儿）；`MIZAR_DIAG=0` 或 `config.json` 的 `"diag_log": false` 可关闭。日志标签与埋点：
  - `[tui ]` 启动参数/日志路径、TUI 进入与退出（含 `app.Run()` 的 error）、**ESC 取消的按键到达**
  - `[agent]` 每次 `Run` 的开始与结束（ok / aborted / err）、循环内两处 abort 检查点命中
  - `[llm ]` 请求失败（含 `aborted=` 标志）、流结束状态（`chunks` / `finish_reason` / content 长度）
  - `[panic]` agent 任务 goroutine 的 panic 全栈（此前 TUI 下 panic 会被 tview 恢复屏幕后重新抛出，终端上完全看不出发生了什么）
  排查「任务突然停住」的三步：看 RUN 开始后有无 `run end`（无则进程/gstack 卡住或崩溃）；看 `[tui ] ESC 收到`（用户/终端误发取消）；看 `[llm ] 流结束` 的 `finish_reason`（提前断开=上游/网络问题）。

- **非 `stop` 结束的 LLM 回复不再让循环空转**：`finish_reason` 为 `length`/`content_filter`/缺失时，不再直接返回裸文本，而是与正常路径一样包装为控制协议 JSON（或原样透传已提取的工具调用）。此前这种回复会被 agent 循环的 `parseCallJSON` 判为「无法识别的控制指令」→ 注入纠正提示、白耗一轮（弱模型/输出被截断时常见，表现为任务莫名停顿）。这条不变式（`wrapReply=true` 且内容非空 → 输出必须可被 `parseCallJSON` 消化）现收敛到单一共享函数 `wrapReplyJSON`，四条路径（流式 / `ChatWithToolsCtx` / `ChatWithTools` / `ChatStream`）共用，避免再次漂移；`finish_reason` 退化为纯诊断信息写入 `[llm ]` 日志。`wrapReply=false`（插件直连 `AIChat`/`AIChatStream`）与裸 `Chat`（AuxLLM 的 GoalLoop judge / BackgroundReview 需直接 `json.Unmarshal`，插件 `llm_chat`）一律不包装，保持原样返回。另补上两个漏写 `finish_reason` 的流式测试夹具字段（真实 SSE 必带，此前它们实际跑在「无 finish_reason」的异常分支上）。

### 🐛 修复

- **`/handoff` 交接文档信息太少（连当前任务都没写）**：`writeHandoff` 原先只写 git 分支/状态/提交记录 + 用户备注，**完全不包含会话内容**——新会话读完只知道「代码处于什么状态」，不知道「刚才在干什么、试过什么、下一步做什么」——而这恰恰是交接文档的核心。现新增会话上下文层（放在文档最前）：
  - **当前任务**（用户反馈缺失的那项）：`Agent` 新增 `currentTask` + `SetCurrentTask`/`CurrentTask`，`Run` 开始时记录——此前 `a.Initial` 只在 Run **结束后**才追加，运行中执行 `/handoff` 无从得知当前在做什么
  - **最近对话与工具轨迹**：最近 40 条消息，含工具调用（`🔧 工具 \`参数\``）与结果（`↳ 摘要`），新会话能看到「试过什么、结果如何」
  - **待办清单**（pi 式 todo）与**会话目标**（存在时）
  - 长工具输出折叠为单行并按 600 字符截断，避免单条消息撑爆文档

- **模型在长会话后退化为纯文本（发「继续」一条就结束）**：这是根因最深的一处。mizar 把工具交换历史一律压成 **普通文本** 发给 LLM：
  ```text
  assistant content={"action":"tool",...}     ← 文本，不是原生 tool_calls
  user      content=工具结果: {...}              ← 文本，不是 role:"tool"
  ```
  模型看到的历史是「有人一直在念 JSON」，它会**模仿这种模式**——用文字描述而不真的发 tool_call（实测：长会话后 `finish="stop"`、`toolCalls=0`、内容里写 `<tool name=...>` 或「我用 bash 直接写入」）。
  现对齐 pi agent（其 `convertToLlm` 原样透传 `toolResult`，provider 层再转成 `role:"tool"` + `tool_call_id`）：新增 `llm.encodeMessages`，**全程用 OpenAI 原生字段**：
  - assistant + `KindToolCall` → `content`(仅前导说明文字) + `tool_calls[]`(id/type/function)
  - `KindToolResult` → `role:"tool"` + `tool_call_id` + 结果正文（剥掉 `工具结果: {...}` 包装）
  - **孤儿结果保护**：历史被 `-history N` 截断时开头可能落在一条工具结果上，而 OpenAI 严格要求 `role:"tool"` 必须回应前面的 `tool_calls`（否则 400）。这类孤儿（含旧会话缺 `ToolCallID` 的）**降级为普通 user 文本**——保留信息又不违反协议。实测截断后的真实会话，修复前会发出 11 条 `role=tool`（全部孤儿，必然 400），修复后为 0。
  - 旧会话兼容：缺 `ToolCalls` 字段时从 `ToolName`/`ToolArgs` 或 `Content` 里的 JSON 回退，并分配稳定的合成 ID。

- **`-session` 恢复会话时历史不显示（要发一条消息才出现）**：`newTuiModel` 已把 `a.Initial` 的历史填进 `m.lines`（恢复逻辑本身正确），但 `Run()` 的初始渲染只 `SetText(banner())`，把 `m.lines` 抹掉了。于是用户必须发一条消息、触发 `renderAllDirect` 后才看到历史。现 `Run()` 改为调用 `renderAllDirect()`（它不访问 `m.app`，在事件循环启动前调用是安全的）。

- **模型输出「未闭合 XML 工具调用」时任务提前结束**（实测于 Windows 会话 idx134/136）：模型的真实输出是 `<tool name="bash">{"command":"…"}` —— **只写开标签、没有 `</tool>`**，JSON 参数本身完整，尾部甚至串出 `</system>`。旧模式1 正则 `<tool\s+name="([^"]+)">([^<]+)</tool>` 有两个硬条件不满足：① 必须出现 `</tool>`；② args 内**不能含 `<`**（`[^<]+`）。而真实 args 是 bash 命令，必含 `<<'EOF'` / `<#` 等。结果解析 0 个 → XML 被当纯文本 → 循环视为「任务完成」→ 一条回复就结束。
  现改为**逐候选提取 + JSON 完备性校验**：从头找 `<tool name="x">`，用 `json.Decoder` 吃下第一个完整 JSON 对象（`InputOffset` 即边界），成功即成调用——无需 `</tool>`，也不受 args 内 `<` 影响。同时保留 arg_key/arg_value 与 reply 分支。

- **模型输出 XML 工具调用时任务提前结束（一条回复就停）**：`IsToolCallText` / `ExtractActionJSON` 只认 JSON，完全不知道 XML 的存在，于是 LLM 层的 `wrapReplyJSON` 把 `<tool name="read">…</tool>` **包成了 `reply`** → 循环当成「任务完成」→ 立即返回，用户看到 XML 标签原文、工具根本没执行。实测于 Windows 会话（模型原生 tool_calls 与 XML 文本协议混用时会触发）。
  - `IsToolCallText` 增加 XML 形态识别，**复用 `extractActionXML` 而非另写正则**（两套判定必然漂移，正是本 bug 的根因）
  - `ExtractActionJSON` 增加 XML 分支，把 `<tool>` / `<reply>` **归一化为等价协议 JSON**，使 `wrapReplyJSON` 与循环共用同一套解析
  - 顺带修：`<reply>答案</reply>` 此前会被包成 `{text:"<reply>答案</reply>"}`，标签原样显示给用户

- **模型一轮发的多个工具调用被静默丢弃**：`toolCallsToText` 只取 `tcs[0]`（`callRequest` 是单数结构），其余工具调用直接丢弃且无任何痕迹——**模型的写操作会凭空消失**。OpenAI 原生 `tool_calls` 本就是数组，模型一轮并行发多个调用是合法行为。现已端到端支持按出现顺序依次执行：
  - **解析层**：新增 `ExtractAllActionJSON`（提取文本里全部控制 JSON，保序）与 `parseCallJSONs`（批量解析）；`ExtractActionJSON`/`parseCallJSON` 保留为「取首个」的兼容入口。
  - **LLM 层**：`toolCallsToText` 输出 `calls` 数组携带全部调用（按顺序），并**冗余保留** `tool`/`args` 单数字段携带首个——只认旧字段的读取方（如 subagent）不受影响。
  - **循环层**：`Run` 把工具执行段抽为 `runOneToolCall`，按序执行 `calls`；`respond` 与终止型工具出现时立即收尾并**跳过后继调用**，参数校验失败/guard 提醒则中断本批进入下一轮。单数字段（旧协议 / XML / bare）仍走兼容路径。

- **`ChatWithToolsCtx` 漏用 `wrapReplyJSON`**：上一轮收敛时漏了这一处，它仍手写包装（且注释引用已改名的 `parseCallJSON`）。现统一走 `wrapReplyJSON`。

- **模型“只宣布不执行”导致任务停滞**：实测连续三轮（会话记录 idx 116/118/122）模型只输出“我重写为纯注册表清理方式”这类预告就结束本轮——`action=reply` 在循环里就是“任务完成”，于是用户只能反复发“继续”，长任务实际停滞。根因是系统提示把“汇报进展”和“宣布任务完成”归到了同一个 `respond` 出口。现提示词明确区分：① 要动手就先把工具调用发出去；② `respond` 只用于回答提问或真正完成时交付；③ 明确反例。

- **`reason`（边做边说）从未被提示词告知**：mizar 的协议其实早就支持“文本 + 工具调用同一轮”——`toolCallsToText` 把模型同一轮的 `content` 写入协议 JSON 的 `reason` 字段，TUI 也会展示（`ToolIntro()` / `ctx.Reason`），但系统提示从未提及，模型无从知晓。现补上说明，并明确“只有说明文字、没有工具调用的那一轮会被当成任务结束”。

- **`reason` 无长度上限**：`reason` 接收模型整轮文本且不限长，既浪费 token，又让弱模型更容易在这个转义字符串里写畸形 JSON。现按 rune 截断到 500 字符（不切汉字）。

- **Windows 上「没按键却突然任务已取消」+ 输入框乱码**：根因是 `EnableMouse(true)` 在 Windows 上要求 conhost 向宿主终端请求鼠标上报，而 ConPTY 的 win32-input-mode 存在双重编码缺陷（microsoft/terminal#15083、#16343），鼠标移动/滚轮被还原成字符流灌进控制台输入缓冲；tcell 的 ConsoleScreen（`ReadConsoleInputW`，无 VT 序列解析）把它们当普通按键交给 tview → 输入框出现 `[<35;66;61M...` 乱码，且序列以 ESC 开头，被误判为用户按下 ESC → 无辜触发「任务已取消（当前步骤结束后停止）」。现 Windows 下默认不启用鼠标上报，整条链路断开；复制粘贴改用终端原生能力（拖选 + `Ctrl+Shift+C/V`、右键菜单），键盘侧 `Alt+L` / `Alt+C` 拷贝模式不受影响，聊天区滚动仍可用 `PgUp`/`PgDn`。需要鼠标的用户可设 `MIZAR_MOUSE=1` 强制开启。
- **Windows 粘贴多行文本被逐行提交**：tcell 在 Windows 上 `EnablePaste` 是空实现（无 bracketed paste），粘贴多行时换行符变成裸 Enter，导致半截内容被发送。现增加粘贴换行防护：毫秒级连发的 Enter 判定为粘贴换行（插入换行而非提交），人手打字间隔（≥8ms）仍正常提交；`MIZAR_PASTE_GUARD=0` 可关闭。
- **粘贴带 `@` 的文本被误解析成文件引用**：`resolveAtRef` 原先取行内最后一个 `@` 并把其后整段文字当路径，粘贴密码（`p@ssw0rd`）、邮箱（`user@example.com`）会被改写成「读取文件 xxx」。现对齐 pi 的边界规则：① `@` 必须处于 token 边界（行首或前一字符是空白/标点），词内 `@` 一律不解析；② 裸路径需真实存在或带路径形态（含 `/`、`./` `../` `~/` 开头）才解析；③ 显式前缀 `@cmd:` `@tool:` `@file:` `@read:` `@dir:` 表达明确意图，照旧解析；④ 带空格路径用 `@"my file.txt"` 引号形式；⑤ 引用后的自然语言不再被吞掉。补全侧同步只在边界处触发，粘贴密码不再弹候选。

---

## [v0.4.1] — 2026-09-04

### ✨ 新增

- **TUI 拷贝模式（任意区域复制）**：`Alt+C` 进入/退出，↑↓/PgUp/PgDn 移动光标，`v`/空格 标记选区起点，`y` 复制到剪贴板（内部缓冲 + OSC52 + xsel/xclip），`q`/Esc 退出。进入后暂停流式重绘避免闪烁。
- **一键复制最近一条 AI 回复**：`Alt+L`。
- **剪贴板输入框桥接**：`Ctrl-Q` 复制 / `Ctrl-V` 粘贴 / `Shift+Insert` 粘贴，右键输入框＝粘贴（跨 SSH 终端，对齐 pi）。
- **弱模型畸形工具调用重建**：识别“只剩 bash 参数对象 `{"command":...}`”的裸调用（无 action/tool/args 包裹）并重建为 `{"action":"tool","tool":"bash","args":"{...}"}` 真正执行，容忍 command 内字面换行，timeout 解析为 int。

### 🐛 修复

- 移除了聊天区边框竖线（`SetBorder(false)`），多行复制不再被竖线污染，风格对齐 pi。
- **TUI 排队消息无法快速介入**：任务运行中输入的消息立即 `Steer()` 进当前循环（下一次 LLM 调用前注入为 user 纠正，打断工具链），而非等整串工具调用跑完才发送；新增 `PendingSteer()` 兜底避免收尾瞬间的纠正消息丢失。
- **`tool "" not found`**：标准形态缺 tool 字段但 args 含 command 时齐底推断为 bash 执行。

---

## [v0.4.0] — 2026-08-28

### ✨ 新增

- **计划模式（Plan Mode）**：`/plan` 进入规划模式，模型先探索设计方案并通过 `exit_plan_mode` 工具提交计划，TUI 模式下弹出用户审批界面；`/plan off` 退出。系统提示词中注入规划引导语（可自定义）。
- **会话目标（Goal）**：`get_goal` / `create_goal` / `update_goal` 三个工具，支持生命周期 `pending → in_progress → completed/blocked/paused`；create/edit/pause/resume 需人类直接消息权限；complete/blocked 可由模型自动报告（blocked 需满足连续 3 轮阈值）。
- **定时提醒（Schedule）**：`schedule_create` / `schedule_list` / `schedule_delete` 工具，支持三种模式：`after <n>秒`（延迟）、`at <RFC3339>`（绝对时间）、`every <n>秒`（固定间隔，最小 5 分钟）。后台 ticker 每 10 秒检查到期项，到期后终端输出 + 日志通知。
- **会话标题（Session Titles）**：会话列表 `/sessions` 展示自动推导标题（首条用户消息截断 60 字符），持久化到 `~/.mizar/sessions/<scope>/titles.json`，支持用户重命名钉住。
- **循环卫生守卫（Loop Guard）**：复刻 deepseek-harness 的 repeat-tool-reminder，JSON deep key-sort 参数规范化后比较，同一工具+参数连续重复时在阈值 [3, 5, 8] 处渐进注入提醒（gentle → detailed，含工具名/次数/参数预览），超 8 次终止任务。比原 WeakModelTuner 直接 kill 更宽容。
- **todo_write 工具**：结构化任务清单，支持 `pending/in_progress/completed` 状态，whole-list replacement 语义，单次调用替换整个列表。
- **后台任务（Jobs）**：`job_start` / `job_list` / `job_output` / `job_kill` 工具 + `bash run_in_background`，进程退出后自动注入完成通知到对话上下文。
- **ask_user_question 工具（文本编号选项）**：问题渲染为带编号的文本选项（`1) xxx`），用户直接回复编号即完成选择。TUI、经典 readline、`-task` 单次任务三种模式均可用（多选 `1,3`；直接回车 = 第 1 项）；无交互界面（Server/管道）时降级为「把选项写进正文，用户下一条消息回编号」，而非只说“请自行决策”。同时修复 `SetAskUser` / `SetPlanAskUser` 零调用导致 TUI 提问也走降级、`exit_plan_mode` 静默 approve 的问题。
- **借鉴 pi agent v1.1.0（5 项）**：
  - **压缩摘要保真**：工具调用序列化为单行（`edit(path="/x", edits=[2 item(s)])`）、工具结果截断、累计 `<read-files>`/`<modified-files>` 清单；`SummaryPrompt` 补 `## Constraints & Preferences` 节。入口统一为 `agent.BuildSummaryInput`。
  - **压缩切点修复**：去掉压缩后插入的空 `system` 占位（白占 prompt 且干扰缓存前缀）；切点落在单个 user span 内部时额外生成「span 前缀摘要」与历史摘要合并（对齐 pi split user-message span）。
  - **显著 cache miss 提示**：命中率从高位下跌 ≥30 个百分点且落到 50% 以下时在聊天区提示一行，说明「上下文前缀已失效，本轮更慢更贵」；prompt < 5000 tokens 不判定（噪音），恢复后可再次提示。
  - **bash 增量输出流式**：命令运行中在聊天区尾部实时显示最近 12 行（80ms 节流，缓冲上限 64KB）；超时/结束时 Flush 保证尾部输出不丢；无回调（Server/CI）时退化为原行为。
  - **通用工具 terminate**：`plugins.Tool.Terminate` 声明终止语义，执行成功后以返回值收尾、省一次 follow-up LLM 调用；失败时忽略声明让模型纠正。
- **skill 按需加载工具**：通过 `skill <name>` 一次性返回 SKILL.md 全文，替代原有的索引注入 + read 文件方式。
- **插件直连 LLM**：`ai_chat` / `ai_chat_stream` 宿主函数，插件可直接调用 LLM 而无需经过 Agent 循环（v0.3.0 引入）。
- **流式输出**：LLM SSE → agent 流式回调 → TUI/Server 增量渲染，支持实时打字机效果。
- **100 场景验证框架**：开发 70 场景 + 运维 30 场景，支持 task / interactive / rpc 三种模式，全量日志 + Pi 对比测试。
- **Path-Scoped 会话记忆**：按工作目录自动恢复/创建会话（类似 Pi 的 workspace-scoped memory）。
- **中文分词宿主函数**：`seg_cut` / `seg_pos`（结巴分词 Go 移植）。
- **文件模糊搜索**：`files_fuzzy` 宿主函数，基于路径子串匹配。
- **弱模型宽容调优**：死循环检测 + 解析失败降级 + LLM 故障重试三合一策略。

### 🐛 修复

- TUI 小键盘数字键映射错误（多个提交修复，含 `numpad` 专用处理）。
- TUI 输入框高度异常（bracketed paste 支持后修复）。
- /help 输出颜色统一为绿色。
- bash 异步命令完成后结果未正确合并（修复合并逻辑）。
- edit 工具多编辑偏移错配（倒序应用 edits）。
- bash 进程树击杀平台兼容（unix Setpgid / windows taskkill /T）。

### 📝 文档

- `docs/architecture.md` 补充深度复刻对比章节（Section 15，含 dsh vs mizar vs Pi 对比表）。
- README.md 同步更新新增功能描述及 Pi 对比表。
- 添加项目介绍语音版（`docs/mizar-intro.ogg`，TTS 合成）。
- README 双语化（英文简介适配 GitHub 开源展示）。
- 添加 Apache License 2.0。

### 🔧 重构

- 插件架构：JS 插件注册 Provider 接口，支持 LSP/Diagnostic/Completion 扩展。
- `/sessions` 命令支持 UUID 会话 ID 和标题展示。
- `build.sh` → `Makefile`（支持 `make build` / `make release` / `make upx`）。

---

## [v0.3.0] — 2026-08-12

### ✨ 新增

- 插件直连 LLM：`ai_chat` / `ai_chat_stream` 宿主函数，插件可绕过 Agent 循环直接调用 LLM。
- 网络层：TCP server/client、FTP client、WebSocket client。
- Panic 隔离 + 执行超时（提升稳定性）。

### 🐛 修复

- 插件工具可用性全链路修复。
- bash 异步命令结果合并。

---

## [v0.2.x] — 2026-08-12 ~ 08-13

### ✨ 新增

- 初始化向导 + 斜杠命令注册表 + 配置持久化。
- 技能使用统计 + 周报 + 禁用（不破坏 System prompt 缓存）。
- 技能索引注入（缓存友好，对齐 Pi pi-cache-guardian）。
- WebSocket 事件能力（rpc_* + ws_emit）。
- .js 插件免编译直跑 goja + readline 交互升级（Tab 补全/退格/历史）。
- @ 路径自动补全。
- Sequential / CircularBuffer / StableJSON / RaceAbort 工具库。

### 🐛 修复

- P0: edit 多编辑倒序应用（防 offset 错配）+ bash 进程树击杀（Setpgid 防孤儿化）。
- P1: read 双限截断（2000 行/50KB/续读提示/10MB 预检防 OOM）+ bash 输出截断（50KB 落盘 temp）。
- 全代码内存风险扫描（FSRead 宿主函数 10MB 预检 + HTTP 响应体 LimitReader + toolRead 10MB 预检）。
- fs_read_range 宿主函数（插件大文件有界 seek 读，单次 4MB，循环分片）。
- bash 进程树击杀平台抽象（exec_unix / exec_windows）。
- TTS 语音改为中文 voice（zh-CN-XiaoxiaoNeural）。
- TUI 小键盘数字键支持。
- /help 输出颜色统一。
- 输入框高度修复。
- bracketed paste 支持多行粘贴。

---

## [v0.1.x] — 2026-08-12

自举闭环骨架：goja 引擎 + esbuild 编译 + 插件加载器 + Agent 循环（文本 JSON 工具调用协议），全部测试通过 + 真实 LLM 端到端验证（17×23=391 场景）。

---

*注：版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。*
