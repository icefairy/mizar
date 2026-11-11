# Mizar (开阳)

> 北斗第六星 · 双星系统 —— 主星与辅星，互为陪伴，缺一不可

[![License](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](LICENSE)

**开阳 (Mizar)** 是一个极简、零依赖、单文件、可自举的 AI Agent 框架。

- **核心**：单个静态 Go 二进制（未压缩 ~74MB，发布包 tar.gz ~23MB，无运行时依赖，无需 npm / Python / Docker）
- **插件**：TypeScript / JavaScript 文件 —— 内嵌 esbuild 编译 TS，JS 免编译直跑 goja，支持热重载
- **技能**：纯文本 SKILL.md 文件（兼容 Anthropic/Hermes 第三方技能生态）
- **工具**：MCP 协议 —— 接入任意语言实现的 MCP 服务器
- **可自举**：Agent 自行编写插件并扩展自身能力

**适用场景**：内网 / 离线 / 私有化部署 —— 拷一个文件到目标机器即可运行，零依赖、零 npm、零外网。

```
安装  = 下载一个文件
插件  = 一个 .ts/.js 文件
技能  = 一个 .md 文件
工具  = 一个 MCP 服务器
```

---

## 为什么是"开阳"

北斗七星：天枢、天璇、天玑、天权、**开阳**、玉衡、瑶光。

开阳是北斗第六星，肉眼可见的分光双星，伴星名为"辅"（Alcor），古人以能否辨出辅星测试视力。这个双星结构恰是本项目的灵魂：

| 星 | 角色 | 项目对应 |
|---|---|---|
| 开阳（主星） | Agent 本体 | Go 核心二进制 |
| 辅（伴星） | 扩展能力 | TS 插件 / SKILL.md 技能 / MCP 工具 |

主星负责运转，辅星负责扩展；主星定义了辅星的接口，辅星让主星不断变强——这正是"自举"的星象学表达。

> 🎧 项目介绍语音版：[docs/mizar-intro.ogg](docs/mizar-intro.ogg)（TTS 合成，约 20 秒）

姊妹项目 [璇玑 (Xuanji)](https://github.com/icefairy/xuanji) 取北斗一二星（枢纽之意），负责 AI 网关汇聚与路由；Mizar 取北斗第六星（辅星相伴之意），负责 Agent 本体与自举扩展。一枢纽，一自举，同源同构。

---

## v0.7.5 亮点：动手前先问清楚 + 拖选即复制 + 状态栏自适应

三件事分别对应「怎么想」「怎么用」「怎么看」。

### 一、`/grill` 拷问模式

`/plan` 解决「怎么做」，`/grill` 解决「到底要做什么」。进模式后模型先复述理解，再逐层追问：

- 每轮**最多 3 问**，每题给 2~4 个编号选项，走 `ask_user_question`（点编号即答）；
- 只问**能改变结论**的问题：目标/验收 > 非目标 > 约束 > 失败回滚 > 权限；措辞命名类禁止问；
- **拷问期间禁止改代码**（只允许只读探查），避免「问着问着顺手写了一半」；
- 收敛条件：能写出**可验证的完成定义** / 用户说够了 / 已问 3 轮；
- 收敛后输出「确认清单 + 风险与默认假设 + 下一步建议」，然后停下等指令。

与 `/plan` 可叠加，注入顺序固定为「拷问 → 计划」。另有 `/grill status`、`/grill off`。

顺带修掉一个隐性坑：**模式开关原先不让 system prompt 缓存失效** —— 退出模式后模型可能
仍按「先出方案」的老提示运行。现在 `PlanMode`/`GrillMode` 带版本号，`SystemPrompt()`
按版本命中缓存。退出参数统一由 `agent.IsModeExitArg` 收口（`exit/off/stop/quit/end/close/
退出/关闭/结束/取消`），不会「又进入了一次」。

### 二、鼠标直接拖选即复制

不再需要 Shift+拖拽，也不再依赖终端原生选择：

| 行为 | 说明 |
|---|---|
| 左键拖动 | 选中即高亮，**松开自动复制**到剪贴板 |
| 双击 | 选中整行 |
| Esc | 只清选区（不取消任务） |
| 拖出边界 | 自动滚动 |
| `Shift+拖拽` | 一律放行给终端原生选择（鼠标上报模式下唯一的逃生通道） |

实现要点：选区按**屏幕坐标**记录并按**屏幕单元格**取字（tview 的 uniseg 折行 + CJK 双宽
自己再算必然差一列，差一列就少选一个字）；高亮用 `AfterDrawFunc` 反色叠加
（流式输出每 60ms 全量重绘，塞进文本会被冲掉）；内容滚动时按 `GetScrollOffset` 差值补偿选区。
Windows 仍默认不开鼠标上报（ConPTY 会把鼠标事件误编码成字符），需 `MIZAR_MOUSE=1`。

### 三、状态栏跟着终端宽度走

不再写死两行：`measureRows` 借 tview 自己的排版结果量行数（避免自己实现 CJK 双宽/单词边界
折行对不上），`syncBarHeight` 只在行数变化时 `ResizeItem`；终端缩放由 `SetBeforeDrawFunc`
检测后重算；单条最多 3 行且不超过屏高 1/6。

第一行新增**常驻模式指示**（正常编码 green / 计划 aqua / 拷问 yellow / 叠加 fuchsia）——
模式会让模型行为整体改变，没有常驻指示时用户只能从模型话术猜自己卡在哪个模式里。
同时新增 **MCP 统计** `MCP 2/3`（已连/配置，连接是惰性建立的）与 `/mcp list`。

### 四、顺带修掉的真实缺陷

- **拖选抬起鼠标时死锁**：`MouseCapture` 本身跑在 tview 事件循环内，早期实现用
  `QueueUpdateDraw` 请求重画会「自己等自己」。现返回 consumed 交给 tview 自行重画。
- **外部剪贴板工具可能挂住事件循环**：`xsel`/`xclip`/`powershell.exe` 在无显示服务
  （SSH / headless / WSL 未配 X 转发）时可能既不报错也不返回，而复制是同步调用 → 界面冻死。
  现所有调用带 **2s 超时 + `cmd.WaitDelay`**（后者是关键：工具 fork 的子进程持有 stdin
  管道时，光 kill 主进程 `Wait` 仍会挂住）。超时只是这次不写系统剪贴板，内部缓冲与
  OSC52 通路照旧可用。

## v0.7.4 亮点：工具失败不再拖垮进程 + 读不了就自己造工具

起因很具体：一份 WPS 导出的中文简历 PDF 读不出来，结果**整个 mizar 进程直接退出**。
排查下来不是 PDF 一个点的问题，而是整条工具链缺少崩溃隔离 —— Go 里有两类错误
`recover()` 根本救不了：**goroutine 内 panic** 和**栈溢出**。

### 一、堵住所有崩溃面

| 位置 | 原风险 | 处理 |
|---|---|---|
| `plugins.CallCommand` / RPC 调用 | 命令/RPC 实现 panic | 新增 `recover` |
| `jobs.Registry.Start` | 后台任务 panic → 进程退出 | 新增 `recover` + 状态置 `errored` |
| `mcp.StdioTransport.readLoop` | 畸形 MCP 响应 panic | 新增 `recover` |
| `agent.Hooks.fire` | 插件 hook panic 中断整个循环 | 单 hook 隔离，转 error 继续 |
| `agent.Agent.Run` | 任意内部 panic | 唯一收口点兜底 + 落盘堆栈 |

PDF 解析器里还藏着 6 个真·杀手：

| 缺陷 | 后果 | 修复 |
|---|---|---|
| `readArray` 把 `io.EOF` 当普通元素 | **死循环 + 内存无限增长**（未闭合数组 `[1 2 3` 即可触发） | 显式处理 EOF |
| 对象递归无深度限制 | 深层嵌套 → 栈溢出（**fatal，recover 无效**） | `maxObjectDepth=64` |
| predictor 用 goroutine + `io.Pipe` | 生产者 panic 无法被 recover | **改同步实现** + 行宽/字节上限 |
| `/Kids` 自引用 | 页面树无限递归 | 深度 64 / 节点 20 万上限 |
| `Value.Reader()` 未知滤镜直接 panic | 一条坏流拖垮整份文档 | 返回错误 reader |
| `/W`、`/Size`、`/Length` 可伪造 | 巨量内存分配 | 宽度 0~8 校验 + 条目/字节上限 |

DOCX/XLSX 同时补了 `recover`、zip 炸弹 64MB 上限、超大表格输出截断。

**实测**：10 类恶意 PDF + 200 份随机变异样本，全部「要么返回文本、要么返回 error，
30s 内必须返回」；那份让测试进程卡死 8 秒以上的样本现在 249µs 返回。

### 二、重写 PDF 解析（双引擎）

中文 PDF 读不出来/乱码的根因有 5 条，逐条修：

1. `ToUnicode` 在 PDF 里是 **Stream**，原实现只认 `Dict` → 中文全乱码
2. `Identity-H`（双字节 CID）没有解码器 → 输出原始字节
3. CMap 只解析 `bfrange` 不解析 **`bfchar`** → WPS 导出的文件等于无映射
4. `/Contents` 是**流数组**时按单流读 → 报 `stream not present`
5. 不递归 **Form XObject** → 文字藏在表单里时整页为空

现在两条独立实现互为后备：

```
MIZAR_PDF_ZIG=/path/to/pdftext   （或放在 mizar 二进制同目录）
        ↓ 命中
  tools/pdftext-zig  纯 Zig 单文件，2100 行，256KB 静态二进制，0.04s/份
        ↓ 未配置 / 失败 / 空输出
  internal/pdftext   rsc.io/pdf 修复分支，无外部依赖，1.27s/份
```

Zig 版从零实现词法/语法分析、xref 表与 xref 流、ObjStm 压缩对象、页面树、
Flate+PNG/TIFF predictor、Form XObject、ToUnicode CMap、CID 字宽、水印 OCG 过滤。
`zig build-exe pdftext.zig -OReleaseSmall` 一行构建，可交叉编译到
`aarch64-linux`（244KB）/ `x86_64-windows`（PE32+）。

4 份中文简历实测：3.0k~4.9k 字符，**零乱码、零水印残留**；TeX 生成的英文 PDF
与 `pdftotext` 逐行一致。

### 三、能力不足时有明确阶梯

系统提示词新增「能力不足时：换语言 / 造工具」：

1. 先看错误本身，修参数重试一次
2. 换系统里已有的语言 —— `python3` / `node` / `awk` / `jq` / `pdftotext`（没装先尝试装）
3. 用内置 Zig 工具链自己造工具（`zig build-exe tool.zig -OReleaseSmall`）
4. 用 `skill_manage` 沉淀成技能，下次直接命中

硬性要求：**禁止因为某个工具报错就宣称「做不到」**，至少试过 2、3 条再说；
仍失败须说明「试了什么、卡在哪、还差什么」。

配套落了两个技能：`pdf-text-extract`（四级降级 + 常见坑对照表）、
`build-tool-with-zig`（造工具流程 + Zig 0.15+ API 陷阱表 + 防崩溃写法）。

## v0.7.3 亮点：ask 真正能用 + 状态栏两行化

这一版先让 `ask_user_question` 端到端可用，再给状态栏补上「我在哪个项目里」。

| 问题 | 根因 | 修复 |
|---|---|---|
| 一提问就整轮失败 | 工具 Description 没写 `Args` 段，schema 兜底成 `{args:string}`，模型只能猜参数形状 | 补全 Args 说明 + `parseQuestions` 宽容归一化（裸数组/包装/字符串化 JSON/缺 id 都能吃下），报错改为给出正确调用示例 |
| 提问后界面没反应，等一会自己走了 | 提问块拼在 `SetText()` **之后**，永远不被绘制；`askViaChat` 还有 5 分钟固定超时 | 移到 `SetText` 之前并抽成 `renderAskPrompt`；**去掉 5 分钟超时**，退出路径只剩「作答」或「ESC 取消」 |
| 回完编号按回车**彻底卡死**（键盘全哑） | `submitInput` 跑在 tcell 事件循环线程，回答分支却用 `QueueUpdateDraw`（投递后阻塞等回调在事件循环执行完）→ 自死锁 | 事件循环内直接 `addChatLine`/`renderAllDirect`；AST 扫描测试锁住「`submitInput` 同步路径不得出现 `QueueUpdate`」 |

同一死锁模式在 v0.7.2 的 **Ctrl+L / `/redraw`** 上先出现过一次，本版一并把它按「事件循环内同步重绘 / goroutine 投递队列」两个入口显式拆开，根治这一类问题。

状态栏新增项目行：`📁 codes/mizar  ⎇ main ✱3  │ 会话 a1b2c3d4  │ 已运行 12m` —— 路径只取末两级、家目录折叠 `~`；带 git 分支（detached 显示短 SHA）与未提交改动数；路径超宽时只对目录段横向滞动，分支/会话/时长固定，窗口末端对齐路径末尾并停留，**项目名始终看得全**；git 命令只在启动与任务结束时各跑一次，渲染只读缓存。

## v0.7.2 亮点：Windows 交互再修一轮

v0.7.1 的两处修复在 Windows 实机上仍能复现，这一版把根因补全：

| 问题 | 根因 | 修复 |
|---|---|---|
| 滚轮**仍然**会提示「任务已取消」 | ConPTY 把泄漏的鼠标序列拆成多条输入记录**分批** flush，间隔可达几百毫秒，90ms 窗口等不到 `[` 就当成真按了 Esc | Windows 确认窗口 90ms→300ms；最近见过泄漏时放宽到 1.2s 且到点仍在泄漏就不取消；**两段式 Esc**（第一次只提示，2.5s 内再按一次才取消） |
| 粘贴多行文本「一个字一个字地蹦」 | 没有 bracketed paste，每个字符都是独立按键：`replace()` → `changedFunc` 全文扫描 → 一次全屏重绘 | **攒批**（间隔 <8ms 判定粘贴）→ 空闲 30ms 一次性 `SetText`；期间跳过 `root.Draw()` 并吞掉屏幕输出，只重画一次 |
| 聊天区留下**擦不掉的残留文字**（右侧/上半区） | ① `log`/`fmt.Printf`/OSC52 绕过 tcell 直写终端；② CellBuffer 宽度落后于终端实际宽度，右侧永不重绘 | TUI 下 log 改道诊断日志、提醒改走聊天区、OSC52 不直写；看门狗每 2s 比对尺寸变化即整屏重画 + 空闲兜底；`Ctrl+L` / `/redraw` 手动强制重绘 |
| 按左方向键出现 `[D`（同理 `[A`/`[H`/`[3~`） | 终端把原始 VT 序列逐字符送上来，tview 只认 Key，`[` 与 `D` 被当成普通文本 | 新增 raw-VT 解码层：`ESC[A/B/C/D`、`ESC[1~`…`ESC[24~`、`ESC[1;5C`、`ESC O A`、`ESC`+字符→Alt+字符；认不出/半截超时的序列按原文补回，绝不吞输入 |

Unix 有 bracketed paste，粘贴本来就是整段处理，不受影响。

## v0.7.1 亮点：手感修复

| 问题 | 根因 | 修复 |
|---|---|---|
| Windows 聊天区滚一下滚轮就提示「任务已取消」 | ConPTY 把泄漏的鼠标事件还原成 `ESC[<35;66;61M`，tcell 把开头 `ESC` 当成真 Esc | Esc 先挂起并等待坐标序列（v0.7.2 补全为多批兜底） |
| 长任务跑着时输入框发涩 | 每 60~100ms 全量重绘，Markdown 渲染 52.7ms/次、状态栏重算 4.8ms/次 | 渲染/状态栏缓存 + 重绘合并 → 0.49ms / 1.4µs |

另外：**发布资产一律 xz 压缩**（126/119/168 MB → 74/68/110 MB），因为未压缩包在本地上传链路上几乎传不完。

## v0.7.0 亮点：把「计算」和「编译」都装进一个文件

这一版只有两件事，但都指向同一个目标——**内网离线机器上，除了拷一个二进制，什么都别装**。

| 能力 | 形态 | 解决什么 |
|---|---|---|
| **Zig→wasm 计算内核** | 进程内 wasm（wazero 纯 Go 运行时） | 需要一段可跨平台分发的「计算字节码」，且绝不能引入 cgo |
| **内嵌 Zig 工具链** | 官方归档 `go:embed`，`/zig init` 释放 | 目标机没有编译器/编译器太老，却要**现场编 C/Zig 并 exec 运行** |

两条路径都不破坏 `CGO_ENABLED=0`、纯静态单文件、跨发行版（CentOS 7 / glibc 2.17）部署：

- **wasm 内核**：`internal/wasmkern/`，`.wasm` 用 `go:embed` 随二进制走，惰性编译 + `CompilationCache` + `sync.Pool` 实例池，二进制增量约 2.6 MB。已落地 `count_tokens`（token 估算）与 `bigram`（分词）两个内核。**但实测纯 Go 更快，所以默认仍走 Go**——内核的价值是沙箱与字节码分发，不是性能（详见 [docs/zig-wasm-eval.md](docs/zig-wasm-eval.md)）。
- **内嵌 Zig 工具链**：`zig cc` 本身就是完整的 C 编译器，单包自带各版本 glibc stub，`-target x86_64-linux-gnu.2.17` 直接产出能在 CentOS 7 上跑的产物。产物是**独立二进制**，Mizar 只通过 `bash`/`exec` 调用，进程边界通信（stdin/stdout/exit code），与 cgo 完全无关。
- **一包一架构**：`go:embed all:zigdist` 是目录级的，没法按 `GOARCH` 排除。所以发版前用 `scripts/zigdist-select.sh` 重排目录，**每个发布二进制只带自己那份归档**（linux/amd64→x86_64、linux/arm64→aarch64、windows/amd64→x86_64-windows），不多背 50 MB。
- **不污染系统**：只释放到 `~/.cache/mizar/zig/<平台>/`，绝不写 `/usr/local/bin`、不建系统级软链接。

---

## 核心特性

### 1. 交互式 TUI
基于 tview 构建的终端界面，支持：
- 鼠标滚轮滚动 + 点击不窃取焦点（Windows 下鼠标上报默认关闭，见 `platform_input.go`）
- 状态栏两行：第一行项目信息（路径 / git 分支与改动数 / 会话短号 / 运行时长），第二行模型名、思考等级、上下文占用等（详见 v0.7.3 亮点）
- 输入框多行编辑 + Tab 自动补全
- **本地 bash 直执行**：`!` 开头的输入直接在本地执行 bash 命令，命令与输出都纳入对话历史（详见 [快速开始](#快速开始)）
- 执行中消息排队，Alt+↑ 取回
- 聊天区翻页：`PgUp`/`PgDn`，vim 风格 `Ctrl+U`/`Ctrl+D`、`Alt+K`/`Alt+J`（上/下 10 行）、`Alt+G`/`Alt+Shift+G`（到顶/底）
- 流式输出 + 等待进度指示（LLM SSE → Agent 流式回调 → TUI/Server 增量渲染）

### 2. 内置工具（30+ 个）
基础工具 `bash` / `grep` / `find` / `read` / `write` / `edit` / `ls` / `skill_manage`，+ dsh 复刻新增：
- **`todo`**：会话待办清单（动作式 API：list / add / toggle / clear）。状态随会话持久化——内嵌在工具返回文本的标记段中，会话恢复/分支时按历史回放重建（对齐 pi 的 `examples/extensions/todo.ts`）；用 `/todos` 查看
- **`ask_user_question`**：向用户提问并等待回答。问题以文本编号选项呈现，用户回复编号即完成选择（TUI / 经典 / `-task` 模式均可用，多选 `1,3`）；无交互界面时自动降级为「选项写进正文 + 用户下一条回编号」
- **`skill`**：按名加载技能全文（渐进式披露，避免系统提示过长）
- **`job_list` / `job_output` / `job_kill`**：后台任务管理（bash 支持 `run_in_background`，任务完成后自动注入通知）
其他能力：文件模糊搜索（`files_fuzzy`）、大文件有界分段读取（`fs_read_range`，单次 4MB）、文档格式解析（PDF/DOCX/XLSX）、Bash 详细截断警告、原生 OpenAI function calling 工具调用。

### 3. 宿主函数（Host Functions）
通过 Go 暴露给插件使用的内置能力：
- **文件系统**：`fs_read_range` 大文件分片读取
- **数据库**：`db_query` / `db_exec_batch` / `db_close`（SQLite3 / MySQL / PostgreSQL）
- **网络**：`tcp_server` / `tcp_client` / `ftp_client` / `ws_client`
- **HTTP**：`http_request` 统一请求
- **中文分词**：`seg_cut` / `seg_pos`
- **纯函数**：`time` / `uuid` / `base64` / `sha256` / `path` / `url`
- **Token 计数**：`count_tokens`
- **MCP 调用**：`mcp_call` 调用外部 MCP 服务器

### 4. 插件系统
- **热重载**：修改 `.ts` / `.js` 文件后自动生效，无需重启
- **生命周期钩子**：`plugin_init` / `plugin_cleanup`，文件删除时自动卸载
- **JS 插件免编译直跑**：`.js` 文件直接由 goja 解释执行
- **LSP 插件扩展**：JS 插件可注册 Provider，支持诊断 / 定义跳转 / 引用查找 / 补全
- **启用 / 禁用 / 重载**：`/list_plugins` 查看（✅ 已加载 / ⛔ 已禁用）、`/disable_plugin <名>` 卸载并持久化、`/enable_plugin <名>` 启用、`/reload_plugins` 重载全部。禁用状态存于 `~/.mizar/extensions/.disabled.json`，重启后保持。用于切换插件后端（如用 HMEM 记忆插件替代内置 SQLite 版，与其它 agent 共用记忆）。
- **内置资产自动释放**：常用插件（数据库 CRUD、图片识别、记忆）与技能（交接 / 记忆 / 数据库）用 `go:embed` 内嵌，**首次运行自动释放**到 `~/.mizar`，**只补缺不覆盖**（保护用户修改），`--no-assets` 可关闭。

### 4.1 记忆后端（本地 SQLite / HMEM 服务端，二选一）

内置两个记忆插件，导出同名 `mem_*` 工具，**同时只启用一个**：

| 插件 | 后端 | 特点 | 默认 |
|------|------|------|------|
| `memory.ts` | 本地 SQLite | 零外部服务、零依赖，开箱即用；中文 bigram 分词检索 | ✅ 启用 |
| `memory_hmem.ts` | [HMEM Server](https://github.com/icefairy/hmem_hermes_agent) | 向量+关键词混合检索、反思引擎（经验→洞察）、知识图谱，**多 agent 共用一套记忆** | ⛔ 禁用 |

```bash
# 切换到 HMEM 版（需先部署 HMEM Server）
/disable_plugin memory.ts
/enable_plugin memory_hmem.ts
```

HMEM 连接配置：环境变量 `MIZAR_HMEM_API_URL` / `MIZAR_HMEM_API_KEY` / `MIZAR_HMEM_NAMESPACE`，或 `/mem config set apiUrl http://localhost:8000`。

### 5. 技能系统
- SKILL.md 纯文本技能，frontmatter 解析后注入上下文
- 技能索引注入（缓存友好，正文变化不破坏 System prompt 前缀缓存）
- 技能使用统计 + 周报 + 禁用功能
- `skill_manage` 工具自动沉淀经验

### 6. Server 模式（持久运行 + 三种接入协议）
```bash
./mizar --serve --addr :3003 --token your-token \
  --base-url http://127.0.0.1:3002/v1 --model deepseek-v4-flash
```

| 接入方式 | 端点 | 用法 |
|---|---|---|
| OpenAI 兼容 | `POST /v1/chat/completions` | 任意 OpenAI SDK 改 `base_url` 即可 |
| JSON-RPC 2.0 | `POST /rpc` | `agent.run` / `agent.steer` / `agent.abort` / `tools.list` / `system.ping` |
| WebSocket | `GET /ws` | 实时事件流 + 中途快速纠正（steer） |
| Web UI | `GET /web/` | 浏览器界面：多会话 / 模型切换 / 对话参数 / 流式输出 |

> 📖 **完整 API 文档（所有端点、请求/响应、示例）：[`docs/api.md`](docs/api.md)**

运行期动态开关服务：
```bash
curl -X POST localhost:3003/admin/switch -d '{"service":"ws","enabled":false}' \
  -H "Authorization: Bearer your-token"
```

### 7. 会话记忆（Path-Scoped）
- 按工作目录自动恢复/创建会话（类似 Pi 的 path-scoped session memory）
- JSONL 格式会话日志：追加 / 加载 / 列表 / 删除 / 续接
- **退出记账**：每次退出（TUI/经典模式）自动把当前会话 ID 记到会话根目录的
  唯一状态文件 `~/.mizar/sessions/last-session.json`（含 `{id, scope}`）；下次用
  **`mizar -c`**（或 `-continue`）即可直接续接「**全局**最后一次退出的会话」，
  自动切到该会话所在的作用域，无需记住 UUID。没有记录时回退为新建会话。
- 每条消息带 `timestamp`（RFC3339 毫秒），落盘即打点，无需调用方关心
- AbortReason 归一化与分类（8 种原因）

#### 7.1 时间感知（Time Awareness）
长会话经常会"隔一天再回来继续"，但模型此前看不到任何时间信息，容易把隔天的续接当成刚才的连续对话。现在：

- **当前实时时间**每轮注入一条 system 消息作为准确时间基准（不写进系统提示词——那里是前缀缓存区，逐轮变动会打穿缓存）
- **间隔提醒**：历史最后一条消息距现在超过 **1 小时**时，插入一条【时间提示】，显式说明已过去多久（"3 天 2 小时"），并提示不要假定上下文仍然连贯、涉及时效/文件状态/服务状态先重新核实
- 短间隔静默：一问一答是常态，逐条提醒纯属噪声
- 旧会话（无时间戳）不编造提示，行为不变

效果示例（隔 3 天续接）：

```
[system] 当前实时时间：2026-10-21 11:31:26（周三，+08:00）。请以此为准确时间基准；往轮消息时间仅为历史参考。
[user]   【时间提示】注意：距会话上一条消息（2026-10-18 09:00）已过去 3 天 2 小时（周日），
         用户可能已切换任务或环境状况已变。请勿假定上下文仍然连贯；如涉及时效、文件状态、
         进程/服务状态等，先重新核实再作答。
```

#### 7.2 会话压缩与持久化（Compaction）

上下文接近窗口上限时自动压缩旧消息为结构化摘要（Goal / Progress / Decisions / Next Steps），释放上下文但保留关键信息。

- **压缩记录落盘**：压缩发生时向会话文件追加一条 `{"type":"compaction","summary":...,"first_kept_index":N}` 记录（对齐 pi 的 `CompactionEntry`）。加载时重建为「摘要 + 保留范围 + 压缩后新增消息」——压缩成果跨进程有效，不会每次续接都重新压一遍。
- **原始对话不丢**：压缩是**追加记录**而非改写历史，JSONL 每一行原始消息都保留。导出/查看仍是完整历史，`Store.LoadRaw()` 可取全量。
- **摘要输出预算跟随模型**：取模型 output limit 的一半（默认 1500、封顶 8000），避免推理型模型把预算耗在 reasoning 上导致摘要失败（`generation hit the token cap`）。
- **旧会话兼容**：无 `type` 字段的行按普通消息解析，存量会话无需迁移。

**压缩必须真的有效**——三种「假成功」都已堵掉，宁可放弃也不写有害记录：

| 情况 | 以前 | 现在 |
|---|---|---|
| 无合法切点（保留预算内全是不可切消息） | 原样返回，日志仍打 `result=` 且钩子报 `Compacted:true`，token 完全没变 | 报 `compact_skip reason=no-cut-point`，`Compacted` 如实为 `false` |
| 摘要为空（模型只产出 reasoning 或被输出上限截断） | 压缩记录照写，加载后等于**静默丢弃前面所有消息**且无摘要补偿 | 直接报错放弃本次压缩 |
| 压缩后 token 反而变大（小窗口下 `keep` 与固定开销叠加所致） | 照写记录，白花一次 LLM 调用 | 校验「压缩后必须变小」，退化即放弃 |

> 判据不是「保留了多少 token」，而是**压缩后是否真的变小**——任何比例启发式在小窗口或大 system prompt 场景都会失效，结果校验才是可靠的不变量。

#### 7.3 上下文自愈（Self-Healing）

最常见的故障是 `context_window` 配得比模型真实上限大（比如配 128000 但模型实际只支持 32K）。此时本地估算看不出问题，但**发任何消息都会持续撞 400**（最大上下文超限），此前只能手工删会话。

现在会自动自愈：

| 阶段 | 动作 |
|---|---|
| 1 | 强制压缩（`CompactForce`），保留最近约一半历史 |
| 2 | 仍失败 → 按窗口 1/4 硬截断 |
| 3 | 仍失败 → **递进裁剪**：10% → 20% → 40% → 60%，每档重发一次请求 |

裁剪优先级：

1. **超大工具结果**（>4000 token）—— 一条日志顶几十条聊天，置占位符保留调用配对
2. **最老的消息** —— 从头部按需丢弃
3. `system` 与**最新消息永远保留**

裁剪结果**落盘**（`{"type":"compaction","emergency":true,...}`），用户退出再进来直接从保留起点重建，不会重新撞 400。界面上明确提示：

```
⚠ 上下文超限：已临时裁剪 40% 历史消息（保留 12 条），原始记录仍完整保存在会话文件中
```

#### 7.4 裸聊（`/chat`）

有时你只想直接问模型一句话，不想要系统提示词、工具定义、技能索引、会话历史任何干扰：

```
/chat 你好，用一句话介绍你自己
```

- **零注入**：不注入 system prompt / 工具 / 技能 / 历史，只发这一句话
- **沿用当前设置**：模型与思考等级与你平时用的完全一致
- **纯旁路**：不走 Agent 主循环、不读写会话历史、不落盘，聊完即忘
- **典型用途**：临时问答、模型自检、对照「有无提示词」时的原始回答差异

实测可以验证注入确实为零：问「你知道自己是什么系统吗」，模型回答的是它自己的原生身份，而不是 Mizar。

#### 7.5 版本检查（`/checkupdate`）

```
/checkupdate
```

从项目主页读取最新版本与更新内容，与当前版本比对：

```
🔔 发现新版本: v0.7.1（当前 v0.5.9）

  更新内容: 交互修复：滚轮不再误取消任务 + 长任务输入不再发涩
  ...
  本命令不会自动更新。请自行获取新版本后替换二进制：
    详情/下载: https://icefairy.github.io/mizar/
```

- **只提醒，不自动更新**：命令不下载、不改任何文件，是否升级由你决定
- **三种结论**：发现新版 / 已是最新 / 本地版本更新（开发中会比站点新）
- **失败不打扰**：网络不可达或站点改版，给出提示并附站点地址，不影响使用

#### 7.6 本地 bash 直执行（`!` 前缀）

有时你只是想跑一条 shell 命令，看一眼结果、再让模型基于输出继续推理，而不想让模型自己调 `bash` 工具绕一圈：

```
!ls -la
!git status
!cat config.json | head -20
```

- **输入框直接以 `!` 开头**：命令在本地 `bash -c` 下执行，30 秒超时，stderr 合并到 stdout
- **CLI 模式**：命令与输出直接打印到终端，同时写入会话历史与内存 `Initial`，下一轮模型能看到
- **TUI 模式**：命令以 `user` 行立即显示，输出以 `cmd` 角色异步显示（保留原始换行，不按 markdown 重排），同步写入会话历史与内存
- **会话续接**：命令与输出都会落盘，会话重启后仍可续接
- **典型用途**：查环境状态（`!df -h`、`!ps aux`）、快速读文件（`!cat x.json`）、git 查询（`!git log -5`）
- **空命令**：输入单独的 `!` 会提示用法，不执行任何东西

注意：`!` 命令不经模型，也不会触发工具调用；需要模型基于输出推理时，直接在下一条消息里发任务即可。

### 8. LSP 支持
- **作为客户端**：通过 stdio 启动语言服务器（gopls/tsserver），提供 `lsp_diagnostics` / `lsp_definition` / `lsp_references` / `lsp_completion` 工具
- **作为服务器**：`--lsp-server` 模式，通过 stdio 接收 JSON-RPC 请求
- **RepoMap 工具**：`repo_map` 注册，帮助 Agent 理解项目结构

### 9. 网络层
- TCP 服务端 / 客户端
- FTP 客户端
- WebSocket 客户端
- Panic 隔离 + 执行超时，提升稳定性

### 10. 循环卫生守卫（Loop Guard）
复刻 deepseek-harness 的 **repeat-tool-reminder**：同一工具 + 规范化参数（JSON deep key-sort）连续重复时，在阈值 [3, 5, 8] 处渐进注入提醒（gentle → detailed，点名工具/次数/参数预览），最后才终止任务。相比原 WeakModelTuner 直接 kill 的死循环检测，更能给弱模型纠偏机会，提高任务完成率。

### 11. 会话标题（Session Titles）
会话列表 `/sessions` 展示自动推导标题（首条用户消息截断 60 字符）。标题持久化到 `~/.mizar/sessions/<scope>/titles.json`，支持用户重命名钉住。

### 12. 计划模式（Plan Mode）
复刻 deepseek-harness 的 plan-mode：`/plan` 进入计划模式，模型先探索设计方案并通过 `exit_plan_mode` 提交计划，用户审批后继续执行；`/plan off` 直接退出。系统提示词中注入规划引导语（可自定义），TUI 模式下弹出审批界面。

### 13. 会话目标（Goal）
复刻 deepseek-harness 的 goal 工具 + pi-goal 的 `/goal` 斜杠命令：
- 工具：`get_goal`/`create_goal`/`update_goal`（edit/pause/resume/complete/blocked）
- 命令：`/goal <目标>` 创建、`/goal status` 查看、`/goal pause`/`resume`/`complete`/`blocked <原因>`/`clear`
- create/edit/pause/resume 需人类直接消息权限；complete/blocked 可由模型自动报告（blocked 需满足连续阈值）
- 适用于长期多步骤任务的目标管理

### 14. 定时提醒（Schedule）
复刻 deepseek-harness 的 schedule：`schedule_create`/`schedule_list`/`schedule_delete`。支持三种模式：`after <n>秒`（延迟提醒）、`at <RFC3339>`（绝对时间）、`every <n>秒`（固定间隔，最小 5 分钟）。到期后以终端输出 + 日志方式通知。

### 15. 100 场景验证框架
开发 70 场景 + 运维 30 场景，支持 task / interactive / rpc 三种模式，全量日志 + Pi 对比测试。

---

## 快速开始

```bash
# 内网服务器，无外网、无 Node、无 Python
curl -O http://192.168.1.10:8080/mizar-linux-amd64   # 或 scp 一个文件
chmod +x mizar

# 直接启动，进入交互式 TUI 后配置供应商与模型
./mizar
  # /provider <baseURL> <apiKey>   一次设置 base URL 与 API Key（自动保存到 ~/.mizar/config.json）
  # /model <name>                   切换模型（自动列出供应商可用模型）
  # /think <level>                  设置思考等级（auto/off/low/medium/high）
  # /config                        查看当前配置（max_steps / context_window / user_agent）
  # /config user_agent MyApp/1.0   自定义 User-Agent，立即生效并持久化到配置
  # /mcp list                      列出已配置的 MCP server 及其连接状态（别名 /mcp ls）
  # /mcp add <name> <url|command>  添加 MCP server（http 或 stdio）
  # /mcp test <name>               测试连接并列出工具
  # /templates                     列出可用 prompt 模板及其可填占位符（$1 / ${1:-默认} / ${@} …）
  # /template <name> [args]        展开模板（直接在对话里输入 /<name> [args] 亦可）
  # 鼠标拖选即复制（双击选整行，Esc 取消；Shift+拖拽用终端原生选择）
  # /grill                         拷问模式：动手前用 ask_user_question 把需求问到没歧义（/grill off 退出）
  # /plan                          计划模式：先出方案再动手（模型调 exit_plan_mode 提交；/plan off 强制退出）
  # /compact                       立即压缩上下文（可附摘要指引）
  # /chat <内容>                    裸聊：不注入任何提示词直接问模型
  # !ls -la                         本地 bash 直执行：!开头直接运行，命令+输出纳入对话历史
  # /checkupdate                   检查是否有新版本（只提醒，不自动更新）
  # /zig                           查看内嵌 Zig 工具链状态（版本/是否内嵌/是否已释放）
  # /zig init                      释放内嵌 Zig 到 ~/.cache/mizar/zig/，之后可现场编 C/Zig
  # /zig path                      打印 zig 可执行文件路径
  # /zig new-mcp ./myserver        生成 Zig MCP server 骨架（形态B：独立二进制子进程）
  # /zig new-wasm ./mykernel       生成 Zig wasm 内核骨架（形态A：内嵌计算内核）

# 也可通过命令行 flag 一次性指定（跳过交互配置）
./mizar -base-url http://127.0.0.1:3002/v1 -api-key sk-xxxx -model deepseek-v4-flash
```

```bash
# Agent 自举：让它写一个插件
./mizar "写一个插件：每天 9 点检查磁盘占用，超过 80% 发飞书告警"

# 插件已生成并热加载
ls extensions/
#   disk_watch.ts
```

### WebSocket 快速纠正示例
看到输出不对立刻纠正（类似 Pi 的 steer）：

```python
import websocket, json
ws = websocket.create_connection(
    "ws://localhost:3003/ws",
    header=["Authorization: Bearer your-token"]
)
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "r1",
    "method": "agent.run",
    "params": {"task": "..."}
}))
# 收到事件流，发现不对 →
ws.send(json.dumps({
    "jsonrpc": "2.0", "id": "s1",
    "method": "agent.steer",
    "params": {"message": "方向错了，改成..."}
}))
# ack 秒回，纠正注入下一轮循环
```

---

## 项目结构

```
mizar/
├── cmd/mizar/              # 入口
├── internal/
│   ├── agent/              # Agent 循环（任务规划/工具调用/错误修复/压缩自愈）
│   ├── assets/             # go:embed 内置插件与技能（首次运行自动释放）
│   ├── builtins/           # 内置工具实现（文件/搜索/任务/goal/schedule/GUI）
│   ├── config/             # 配置加载（~/.mizar/config.json）
│   ├── context/            # 上下文估算与裁剪
│   ├── diag/               # 诊断日志（query.* 结构化事件）
│   ├── engine/             # goja 内嵌 JS 引擎 + esbuild 编译管线
│   ├── jobs/               # 后台任务管理（job_list / job_output / job_kill）
│   ├── lifecycle/          # 生命周期钩子管理
│   ├── llm/                # LLM 客户端（OpenAI 兼容协议）
│   ├── lsp/                # LSP 客户端 + 服务器
│   ├── mcp/                # MCP 客户端（stdio/HTTP）
│   ├── needle/             # needle 协议支持
│   ├── plugins/            # 插件加载器（扫描 .ts → 编译 → 注册工具）
│   ├── prompts/            # Prompt 模板加载与展开
│   ├── repo/               # 仓库地图（repo_map 工具）
│   ├── schedule/           # 定时提醒
│   ├── server/             # HTTP/RPC/WebSocket 服务器
│   ├── session/            # 会话管理（JSONL 持久化 + 压缩记录）
│   ├── skills/             # SKILL.md 加载器（解析 frontmatter → 注入上下文）
│   ├── utils/              # 通用工具函数
│   ├── wasmkern/           # Zig→wasm 计算内核（wazero 纯 Go 运行时，无 cgo）
│   │   ├── *.zig           # 内核源码
│   │   ├── *.wasm          # 编译产物（已提交，构建不依赖 zig）
│   │   └── wasmkern.go     # 加载/实例池/两段式调用 API
│   └── zigboot/            # 内嵌 Zig 工具链（/zig 释放到本地缓存，供内网现场编译）
│       ├── zigboot.go      # 内嵌归档的定位/原子解压/状态
│       ├── templates.go    # /zig new-mcp、/zig new-wasm 的脚手架生成
│       ├── templates/      # 内嵌骨架（zig-mcp/server.zig、zig-wasm/kernel.zig）
│       └── zigdist/        # 归档目录（默认空，.gitignore 忽略，--embed 生成）
├── extensions/             # 插件目录（Agent 自写的 .ts 文件）
├── skills/                 # 技能目录（SKILL.md）
├── scenarios/              # 验证场景（开发 70 / 运维 30）
├── docs/                   # 设计文档
└── go.mod
```

详细设计见 [docs/architecture.md](docs/architecture.md)。

> 🧪 **Zig→wasm 内核的实测开销**（单次调用、内存、GC 尖刺、可调试性）见
> [docs/zig-wasm-eval.md](docs/zig-wasm-eval.md)：wasm 的收益是**沙箱与跨平台
> 字节码**，不是性能——性能上它是净损失，因此内核默认走纯 Go。

---

## 构建与发布

```bash
# 构建（默认 strip，不压 UPX）
make build

# 发布版（strip + UPX 压缩）
make release

# 单文件可执行，无需任何运行时依赖
./mizar --help
```

**可选：重编 wasm 计算内核**（仅当修改 `internal/wasmkern/*.zig` 时需要）

```bash
make zig-kernels     # 需要 zig；未安装则跳过，仍用仓库里已提交的 .wasm
```

内核产物已提交到仓库，因此**正常构建与发布不依赖 zig**。
wazero 是纯 Go 实现的 wasm 运行时，所以 `CGO_ENABLED=0`、纯静态、
Windows 交叉编译等前提完全不受影响（实测二进制增量约 2.6 MB / +3.9%）。

内核开关（默认全部走纯 Go）：
- `MIZAR_WASM_KERNEL=force` 强制 `count_tokens` 走 wasm，`=0` 全部禁用
- `MIZAR_WASM_BIGRAM=force` 让 bigram 分词走 wasm（默认关，实测纯 Go 更快）
- `MIZAR_WASM_VERIFY=1` 内核结果与 Go 参考逐 token 交叉校验

> 实测结论：这两种算法的**纯 Go 实现都比 wasm 内核快**（详见
> `internal/engine/memscore_bench_test.go`）。内核保留为可复用基础设施与
> 对照基准，默认不接管热路径——诚实的默认值比「看起来用了黑科技」更重要。

真正带来数量级收益的是**把循环搬出 goja**：记忆检索的 2000 条打分从
goja 逐条的 102 ms / 372k 分配，改为宿主函数 `mem_score` 批量计算后
10.8 ms / 4.2k 分配（**9.4×**）。

---

## 内嵌 Zig 工具链（内网现场编译 C/Zig）

内网离线场景下，目标机不一定自带编译器。Mizar 可以把官方 Zig 工具链
**内嵌进二进制**，目标机拷一个文件即可现场编译 C/Zig（`zig cc` 也是 C 编译器），
产物是独立可执行文件，再由 `bash` 工具以 `exec` 方式运行——**不涉及 cgo，
不破坏「纯静态单文件」的部署模型**。

### 两种「用 Zig 扩展 Mizar」的形态

| | 形态 A：wasm 计算内核 | 形态 B：MCP server 子进程 |
|---|---|---|
| 产物 | `.wasm`，编译期 `go:embed` 进 Mizar | 独立静态二进制，由 Mizar `exec` 拉起 |
| 插件里怎么用 | `wasm_invoke(kernel, fn, "文本")` | `mcp_call(server, tool, args)` |
| 能力边界 | 纯计算、沙箱、无系统调用 | 全部系统能力（IO/网络/多线程） |
| 改代码后 | 重新构建 Mizar | 重新 `zig build-exe` + `/mcp reload`（无需重启 Mizar） |

一条命令生成可编译的骨架：

```bash
# 出现提示符后
/zig new-wasm ./mykernel     # 形态A：kernel.zig（通用 ABI：in_ptr/in_len → 响应头）
/zig new-mcp  ./myserver     # 形态B：server.zig（JSON-RPC 2.0 按行协议，含 echo/hostname 示例）
```

Zig 二进制本身**不能**像 .ts 插件那样在进程内热替换（Linux 上运行中的可执行文件被覆盖，
旧进程仍跑旧 inode）；但 `/mcp reload` 会杀掉旧子进程按同路径重启，因此是「重编译 + reload」
级别的近似热加载。完整解释与逐步教程见 [docs/zig-plugin-forms.md](docs/zig-plugin-forms.md)。

### 发版侧（有网的机器）

```bash
make zig-fetch      # 按当前架构下载到 ~/.cache/zig（缓存优先，可复用）
make zig-embed      # 下载并复制到 internal/zigboot/zigdist/，随后 make build-zig 内嵌
make zig-clean      # 清理缓存
```

等价于直接调用脚本（支持 `--arch` / `--version` / `--offline-check`）：

```bash
scripts/fetch-zig.sh --arch x86_64-linux        # 内网主力
scripts/fetch-zig.sh --arch aarch64-linux       # arm64（aarch64 与 arm64 是同一架构）
scripts/fetch-zig.sh --arch x86_64-windows      # Windows 版（.zip 归档）
```

**注意 `arm64` 与 `aarch64` 是同一架构的两个叫法**（Linux/Go 叫 arm64，
Zig/LLVM 叫 aarch64），官方归档名统一是 `zig-aarch64-linux-*.tar.xz`，
不需要下载两份。真正不同的只有 32 位 ARM（`arm-linux`）。

### 一包一架构：每个二进制只嵌自己那份（重要）

`go:embed all:zigdist` 是**目录级**的，无法按 `GOARCH` 条件排除。若把三个
归档都扔进 `zigdist/`，那不管编哪个平台，二进制都会平白增大 ~200 MB。
因此发版流程用 `scripts/zigdist-select.sh` 在**每次构建前重排目录**：

```bash
scripts/zigdist-select.sh x86_64-linux      # 只留 x86_64 归档（缓存没有再下）
scripts/zigdist-select.sh aarch64-linux     # 只留 aarch64 归档
scripts/zigdist-select.sh x86_64-windows    # 只留 Windows 归档
scripts/zigdist-select.sh none              # 清空，不嵌任何 Zig
scripts/zigdist-select.sh all               # 全放进去（做通用大包时用）
scripts/zigdist-select.sh host              # 只留本机当前平台（按 uname 自动判断）
# 也支持简写 amd64 / arm64
```

**本地构建默认不嵌 Zig**：`make build` 会先 `zigdist-select.sh none`，产物约 71 MB；
需要「单文件自带 Zig、目标机离线 `/zig init`」时才用 `make build-zig`（产物约 126 MB）。
这样即使之前跑过 `make release`，本地日常构建也不会被 200 MB 的归档撑大。

> **体积异常（本地二进制 270+ MB）怎么查**：先看 `du -sh internal/zigboot/zigdist/`。
> `make build` 不嵌 Zig 时应为 0；只有在跑 `build-zig` / `release-*` 期间才会临时放归档，
> 随后自动恢复为 `none`。历史上 `make release` 结尾会把三个平台全量 `select all`，
> 导致之后每次 `make build` 都吞下 201 MB 归档（71 + 201 ≈ 272 MB），现已改为 `none`。

`make release` 已自动串好这套流程：

```bash
make release           # 一次编三个平台，各自内嵌对应 Zig
make release-xz        # 同上，并额外生成 dist/*.xz（发布上传用这个）
make release-amd64     # 只编 linux/amd64（内嵌 x86_64-linux）
make release-arm64     # 只编 linux/arm64（内嵌 aarch64-linux）
make release-windows   # 只编 windows/amd64（内嵌 x86_64-windows）
make release-xz-arm64  # 只编 arm64 并压成 dist/mizar-linux-arm64.xz
```

> **发布到 GitHub 的资产一律压缩**：release 上只放 `.xz`，不放未压缩的裸二进制。
> 未压缩 126/119/168 MB 在本地上传链路（经常被 RST 或触发「60 秒无数据」超时）
> 几乎传不上去；`xz -T0 -6` 后约 74/68/110 MB，成功率高得多。
> 下载后解压：`xz -d mizar-linux-amd64.xz && chmod +x mizar-linux-amd64`。

实测产物（0.17.1，Zig 只嵌自己那份）：

| 产物 | 大小 | 内嵌归档 | 下载体积（xz -6） |
|---|---|---|---|
| `mizar-linux-amd64` | 126 MB | `zig-x86_64-linux-0.17.1.tar.xz`（55 MB） | 74 MB |
| `mizar-linux-arm64` | 119 MB | `zig-aarch64-linux-0.17.1.tar.xz`（51 MB） | 68 MB |
| `mizar-windows-amd64.exe` | 168 MB | `zig-x86_64-windows-0.17.1.zip`（96 MB） | 110 MB |

归档目录 `internal/zigboot/zigdist/` 已被 `.gitignore` 忽略（属构建产物）。
**未内嵌时二进制照常构建**，只是 `/zig` 会提示未内嵌。

### 目标侧（内网机器）

```
/zig            # 状态：版本 / 是否内嵌 / 是否已释放 / 缓存路径
/zig init       # 释放内嵌归档到 ~/.cache/mizar/zig/<平台>/（幂等，原子解压）
/zig init -f    # 强制重新释放
/zig path       # 打印 zig 可执行文件路径
/zig clean      # 删除运行时释放目录
```

释放后即可在任务里直接让 Agent 用 `bash` 调 zig：

```bash
~/.cache/mizar/zig/x86_64-linux/zig cc -O3 -march=native calc.c -o calc
./calc 1000
```

**释放路径与软链接**（明确一下，避免误解）：

- 释放目录：`~/.cache/mizar/zig/<平台>/`，如 `~/.cache/mizar/zig/x86_64-linux/`。
  可用环境变量 `MIZAR_ZIG_CACHE` 整体改到别处（例如放到空间更大的分区）。
- 平台子目录名与官方归档一致：`x86_64-linux` / `aarch64-linux` / `x86_64-windows`。
- Windows 上 zig 可执行文件是 `zig.exe`，其余平台是 `zig`。
- **不会写 `/usr/local/bin` 或 `/usr/bin`，也不会建任何系统级软链接。**
  原因：内网机器常是多用户/无 sudo/受限容器，污染系统 PATH 风险大且难清理；
  而且同一台机器可能同时存在多个 Mizar 版本。需要 PATH 里直接敲 `zig` 时，
  自己在 shell 里加即可：

  ```bash
  export PATH="$HOME/.cache/mizar/zig/x86_64-linux:$PATH"
  ```

  （归档内的 `lib/`、`doc/` 是 zig 自己按相对路径查找的，跟着 `zig` 二进制走，
  所以复制/移动整个 `<平台>` 目录仍然可用。）

### 实测（x86_64-linux，0.17.1）

| 项目 | 数值 |
|---|---|
| 内嵌归档 | 54.8 MB（sha256 `3581863e…b17a`，官方校验通过） |
| 二进制增量 | 86 MB → 141 MB（+55 MB，仅当 `--embed` 时） |
| `/zig init` 首释 | 12 s（纯 Go `ulikunitz/xz` 解压，`CGO_ENABLED=0`，不依赖系统 `xz`） |
| 释放后体积 | 181 MB（`zig` 单文件 177 MB + `lib/std` 与 C 头文件） |
| `zig cc` 编 C | 5 s，产物 7.3 KB，`GLIBC_2.2.5` 起可用（CentOS 7 无障碍） |
| 原生 Zig 编译 | `zig build-exe -O ReleaseSmall -fstrip` → 134 KB，**静态、无任何动态依赖** |
| 交叉编译 | `-target aarch64-linux-gnu.2.17` 出 arm64 动态产物；`-target aarch64-linux` 出 arm64 静态产物 |

> **下载慢的坑**：ziglang.org 对单连接限速极狠（实测 ~15 KB/s，57 MB 要一小时），
> 但对**并发 Range 请求**宽松得多。`scripts/fetch-zig.sh` 因此默认用 32 路分片
> 下载（`--jobs N` 可调，`--single` 回落单连接），实测 15 s 拿完 17 MB 等效速率，
> 约 1 分钟下完整个归档。

> **为什么不是 gcc / 为什么不 cgo**：内网机器自带的 gcc 往往很老（CentOS 7 是
> 4.8.5），且 glibc 版本与编译机不一致会直接跑不起来；而「产物是独立进程、
> 用 exec 调用」的方案让 glibc 天然一致（现场编现场跑），也没有 cgo 的
> `CGO_ENABLED=1` 依赖。Zig 单包自带 C 编译器与各版本 glibc stub，
> 一个 55 MB 压缩包就能覆盖全平台交叉编译（`zig cc -target x86_64-linux-gnu.2.17`）。

---

## 与 Pi 的对比

Mizar 参考了 [Pi Agent](https://github.com/mariozechner/pi) 的设计理念，但采用 Go 语言重写，目标是在**零依赖离线环境**中实现同等能力。

| 特性 | Pi | Mizar |
|---|---|---|
| 核心语言 | TypeScript (Node.js) | Go |
| 运行时依赖 | npm + node_modules | 零依赖 |
| 插件格式 | TS 扩展 | TS / JS（goja + esbuild） |
| 技能格式 | SKILL.md | SKILL.md（兼容） |
| 工具协议 | 内置 + 扩展 | MCP 协议 |
| 离线部署 | ❌ | ✅ |
| 会话记忆 | 全局 JSONL | Path-Scoped JSONL |
| LSP | 内置 | 客户端 + 服务器双模式 |
| 循环卫生 | — | RepeatGuard（渐进提醒，复刻 dsh） |
| 会话标题 | 自动推导 | 首条用户消息 fallback |
| ask_user | — | TUI 模式支持暂停等回答 |
| 后台任务 | — | job_list/job_output/job_kill |
| todo | — | 会话待办清单（动作式 API，状态随会话持久化） |
| 计划模式 | /plan + exit_plan_mode | /plan + exit_plan_mode（TUI 审批） |
| 会话目标 | create/get/update_goal | get_goal/create_goal/update_goal（权限约束） |
| 定时提醒 | schedule_create/list/delete | schedule_create/list/delete（after/at/every） |
| 验证框架 | — | 100 场景验证 |

完整对比分析见 [docs/pi-comparison.md](docs/pi-comparison.md)。

---

## 关联项目

开阳生态下的配套项目，可独立部署或组合使用：

| 项目 | 仓库 | 说明 |
|------|------|------|
| **开阳 Agent** | [icefairy/mizar](https://github.com/icefairy/mizar) | 本项目：终端 AI 编码助手，单个静态二进制，零依赖离线运行 |
| **璇玑网关** | [icefairy/xuanji](https://github.com/icefairy/xuanji) | API 网关，为开阳 Agent 提供多供应商模型路由、鉴权与流量管理 |
| **飞鹤输入法** | [icefairy/feihe](https://github.com/icefairy/feihe) | 开源输入法，支持自定义词库与插件扩展 |
| **HMEM 记忆系统** | [icefairy/hmem_hermes_agent](https://github.com/icefairy/hmem_hermes_agent) | 分布式长期记忆后端，为开阳 Agent 提供 HMEM 服务端记忆模式（见 §4.1） |
