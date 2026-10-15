# Changelog

所有重要变更记录在此。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。

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
