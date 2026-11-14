# Mizar 能力差距分析：vs Pi Agent / CodeBuddy

> 撰写日期：2026-11-14（同日更新：并入 v0.8.7/v0.8.8 子任务机制改动后的现状核对）
> 对比对象：Pi Agent（`@earendil-works/pi-coding-agent`，对照 `docs/pi-comparison.md` 快照 + CHANGELOG 0.85→1.4.x 已移植项）、CodeBuddy Code（Claude Code 系 CLI）
> 方法：逐文件核对 Mizar 当前源码（`internal/agent/subagent.go` 等以实际代码为准），与两者公开能力清单逐项比对
> 关联文档：`docs/pi-comparison.md`（2026-08 快照，部分项已修复，以本文档为准）、`docs/todo.md`（openclaude 14 项已全部完成）
> 版本锚点：subagent 相关现状对应 commit `528577f`（v0.8.8 收尾），21 项 subagent 测试全绿

---

## 〇、结论概要

Mizar 的子任务机制骨架完整、架构选择正确（子 agent = 独立 `Agent` 实例复用原生 `Run()` 主循环）。v0.8.7/v0.8.8 后已具备：**按任务换模型、有限嵌套委派、运行中 Steer、role 行为预设、排队/执行超时分离、委派时机指引（DelegationGuide）**。

当前欠缺集中在三个层面：

1. **子任务治理层**：用户自定义子代理类型（role 行为预设+工具白名单已有）、权限门禁延伸、运行中进度流式上报、结果落盘——CodeBuddy 成熟、Mizar 空白
2. **对 Pi**：扩展事件拦截（deny/rewrite/intercept）、分支树会话两项核心未移植
3. **零碎**：per-subagent token/成本计量缺失

> 2026-11-14 晚补充：并发实测暴露并已修复两类问题——模型把工具参数包一层 `{"args":...}` 导致 `get_subagent: id required`（四个委派工具已加宽容剥壳，`unwrapArgsJSON`）；win32api 插件 sleep 走 powershell.exe 在 Linux 上 exit 127（根因是 `host.Sleep` 从未赋值，原生 `sleep(ms)` 宿主函数一直没注册，已补上）。同日交付：todo 依赖（blocked_by + 自动解锁，C-2）、`/subagents` 内部命令。

---

## 一、子任务机制现状（internal/agent/subagent.go）

### 1.1 已实现能力（2026-11-14，commit 528577f 核对）

| 能力 | 实现位置 | 说明 |
|---|---|---|
| 派发 | `DelegateTool` subagent.go:541 | 单任务 `{goal,...}` + 批量 `{"tasks":[...]}` fan-out，异步返回句柄 |
| 执行 | `runSubagent` subagent.go:260 | 独立 `Agent` 实例跑原生 `Run()`，自带解析/纠错/压缩/步数预算 |
| 按任务换模型 | `modelDeriver` subagent.go:82 | `req.Model` 非空且 LLM 实现 `WithModel` 时派生新实例（`internal/llm/openai.go` 逐字段构造，不浅拷贝 Mutex）；不支持时回退主模型并在句柄标注 |
| 有限嵌套 | subagent.go:278 | depth < maxDepth 的子 agent 拿到受限 `delegate_task`（深度 +1），达上限不再下发；`Depth/maxDepth` 真正生效 |
| role 行为预设 | subagent.go:204-212, 334-353 | 归一化到 `leaf/explore/implement/review` 四种（未知值归 leaf，防提示注入），按角色注入行为边界：explore/review 只读、implement 动手、leaf 独立交付 |
| role 工具白名单 | `roleWriteBlocked` subagent.go:404, 接入 269-283 | explore/review 的写类工具（edit/write）从子 agent 工具清单**强制摘除**，「不许改任何文件」从提示词约定升级为工具层强制；bash 保留（只读检索依赖），进程级写闸门归权限门禁 |
| 排队/执行超时分离 | subagent.go:242-256 | 排队窗口独立（`max(30s, TimeoutSecs)`），执行预算从拿到并发槽起重新计时，排队不再吃掉执行预算 |
| 查询/取消/纠偏 | subagent.go:422/445/467 | `get_subagent` / `list_subagents` / `cancel_subagent`（真 context cancel，幂等）/ `steer_subagent`（运行中注入纠正，仅 RUNNING 有效）——**五个工具全部已在 main.go:716-720 注册** |
| 并发控制 | sem 信号量 | 上限 10（`defaultMaxConcurrent`），排队不丢任务 |
| 结果回灌 | `notify` subagent.go:386 | 借 `Jobs.DrainDone()` 通知通道自动注入主对话（main.go:673） |
| 委派时机指引 | `DelegationGuide` subagent.go:641 | 注入主 system（main.go:723）：何时该拆/怎么拆/何时不该拆；带抑制条件——只有 2+ 个互相独立、各有实质工作量、并行明显更快才拆；单文件修改/强依赖链/一两步能完的事禁止委派（防简单任务乱拆白烧 token） |
| 句柄回显 | subagent.go:59-60 | `Model`/`WorkingDir` 实际生效值写进句柄快照，`get_subagent` 可见 |
| 参数宽容剥壳 | `unwrapArgsJSON` subagent.go | 模型把参数包一层 `{"args":...}` 时自动剥壳（并发实测回归：曾致 `get_subagent: id required` 死循环）；四个委派工具全部接入 |
| /subagents 命令 | main.go + `RenderSubagentsForHuman` | 内部命令查看子任务列表/状态/批次/结果摘要（TUI/WebUI/interactive 通用） |
| todo 依赖 | internal/builtins/todo_pi.go | `blocked_by` 前置依赖 + 完成自动解锁后继 + list 阻塞标记，随会话回放持久化；delegate_task 描述与 DelegationGuide 引导先落依赖再 fan-out |

### 1.2 子任务机制差距清单

#### 🟡 GAP-S1：无用户自定义子代理类型（~150 行）

v0.8.8 已让 `role` 生效（行为边界注入，见 1.1）；同日第一层工具白名单也已落地——explore/review 的写类工具（edit/write）从子 agent 工具清单**强制摘除**（`roleWriteBlocked` subagent.go:404，接入 childPM 组装 subagent.go:269-283），「不许改任何文件」从提示词约定升级为工具层强制，内置四角色闭环。剩余差距是**用户自定义类型**：无法按项目定义"测试运行型"（bash 但禁 edit/write）等预设，也无法给类型配独立 system 附加层与默认模型——CodeBuddy 支持项目目录自定义 agent 定义文件。

**引入方案**：`~/.mizar/agents/*.md`（frontmatter: name/description/tools/model，正文为该类型 system 附加层），`SubagentManager` 启动时加载，`delegate_task` 描述列出可用类型。与 SKILL.md 生态同构；per-type model 直接复用已生效的 `Model` 派生路径。

#### 🟡 GAP-S2：权限门禁不延伸到子任务（~80 行）

主循环有 plan 审批、`ask_user_question` 确认、goal gate；但子 agent 拿到全量工具（bash/edit/write）**不带任何审批**——`delegate_task` 描述里只对 explore/review 摘除了写类工具，leaf/implement 与 bash 侧仍是直通。

**引入方案**：`SubagentConfig` 增加 `PermissionFn func(tool, args string) bool`，由 main.go 注入与主循环一致的审批逻辑；GAP-S1 的工具白名单本身也是一层软权限，两者叠加。

#### 🟡 GAP-S3：实时推送缺失（进度缓冲与查询已落地）

已实现（OnStep 回调链路）：Agent 实例级 `OnStep`（loop.go）在每步开始与每次工具调用时触发 → 子代理管理器写进句柄 `progress` 缓冲（最近 20 条，`sm.mu` 保护）→ `get_subagent` 的 `progress` 字段与 `/subagents` 的「进度：step N: tool(args)」行可见，运行中即生效、终态后保留最后一批。

剩余差距：**推送式**可见性——运行中进度不会主动推到 TUI 状态栏/WebUI（`/ws`），需要轮询才可见。

**引入方案**：SubagentManager 加 OnProgress 订阅回调（main.go 接线到 WebUI 的事件广播）；TUI 状态栏滚动显示运行中子任务最近一步。

#### 🟡 GAP-S4：子任务记录纯内存（~60 行）

`subs` map 不落盘，进程重启后 `get_subagent`/`list_subagents` 全部丢失，长任务的中间产物（子 agent 跑过的结论）无法跨会话引用。

**引入方案**：句柄快照随会话 JSONL 追加落盘（照抄 todo 的 `<!--todo-state:-->` 内嵌回放模式，`RebuildTodos` 同款）；或独立 `subagents.jsonl`。推荐前者，与现有回放机制一致。

#### 🔵 GAP-S5：token/成本计量缺失（~50 行）

`StepsUsed` 有，但无 per-subagent token/cost 记账。多模型混跑（Model 字段已生效）后无法核算各子任务的消耗，也无法做"给子任务默认配便宜模型"这类策略的收益评估。

**引入方案**：LLM 响应处已有用量统计（`agent/stats.go`），转发进 `SubagentHandle`（`TokensIn/TokensOut` 字段）即可。

> 备注：v0.8.8 已修复的两项（role 死字段、排队/执行共用超时）与 steer 注册缺失已从本清单移除，见 1.1 现状表。

---

## 二、与 Pi Agent 的差距（pi-comparison.md 快照后的残留项）

下表只列**当前仍未解决**的项；`✅` 的项见 pi-comparison.md 第二节，不再重复。

| # | 功能 | Pi 实现要点 | Mizar 现状 | 建议 |
|---|---|---|---|---|
| P-1 | **扩展事件拦截** | tool_call 事件可 `deny/rewrite/intercept/allow`，32+ 事件类型 | Hooks 有 10 挂载点但无 Decision 枚举，工具调用前后不能改写/否决 | 🟢 ~30 行：`Hooks` 加 `OnToolCallDecision(ctx) Decision` 回调，Agent 循环响应。这是 Pi 生态扩展性的核心，也顺带为 C-6（用户级 hooks）打地基 |
| P-2 | **分支树会话** | `/tree` `/fork` `/clone`，JSONL + 分支摘要，交互回溯 | 线性 JSONL + `--session` 续接；`agent.Clone()` 只用于子代理克隆（clone.go） | 🟡 简化版：`/fork` 从指定消息位置派生新 session ID 重放前缀。todo 已有按历史回放重建的机制（todo_pi.go），可直接复用 |
| P-3 | **thinking level 完整档位** | off/minimal/low/medium/high/xhigh/max | auto/off/low/medium/high，缺 xhigh/max | 🟢 ~10 行枚举扩展 |
| P-4 | **模型轮换/多 provider** | Ctrl+P / `/scoped-models` / models.json 多模型切换 | `/model` 单切换；子任务已可按任务指定模型（Model 字段生效），但主对话仍单模型 | 🟡 结合 GAP-S1 自定义类型（per-type model），价值大于 UI 级轮换 |
| P-5 | **Project Trust** | 项目级资源加载确认 + trust.json | 无 | 🔵 简化版：首次加载项目级 config/技能时 TUI 确认一次 |
| P-6 | **export HTML** | `--export` + highlight.js 归档 | 无 | 🔵 Server 模式已有 `/rpc` 数据出口，HTML 导出优先级低 |
| P-7 | **图片读取** | read 工具 MIME 检测转 image 附件 | read 只出文本 | 🔵 多模态网关可用时再做 |

**已优于 Pi、不引入**（详见 pi-comparison.md 第六节）：技能索引注入原生实现、系统提示词强冻结、禁用走工具层不破缓存、MCP 客户端 + WS 桥、bash 默认超时、技能统计/周报/自动沉淀、CacheStats、Prompt Templates。

---

## 三、与 CodeBuddy 的差距（主要欠账）

### 3.1 差距总表

| # | 维度 | CodeBuddy | Mizar 现状 | 等级 |
|---|---|---|---|---|
| C-1 | 子代理类型系统 | 具名类型（Explore/Plan/自定义），每类独立工具集+描述，定义在 `.codebuddy/agents/` | role 行为预设 + 只读角色工具白名单已有；无用户自定义类型 | 🟡 |
| C-2 | 结构化任务清单 | TaskCreate/List/Get/Update：依赖（blockedBy）、owner、状态机、activeForm，多代理共享 | `todo` 已有 blocked_by 依赖 + 自动解锁 + 会话回放；无 owner/认领语义 | 🟢 基本持平 |
| C-3 | 多代理团队 | TeamCreate + 共享任务列表 + 代理间消息协议（SendMessage），互相认领/阻塞 | 无 | 🟡 |
| C-4 | 后台代理 | 代理级后台运行 + 完成通知协议，主代理可继续其他工作 | delegate_task 异步 + DrainDone 回灌已近似；缺通知粒度（GAP-S3） | 🟢 基本持平 |
| C-5 | 权限模式 | plan/default/acceptEdits/bypassPermissions 多档 + 逐工具审批 + 子代理继承 | plan 审批 + goal gate，无通用权限分级，子任务无审批（GAP-S2） | 🔴 |
| C-6 | 用户级 Hooks | 用户 settings 配置 shell 命令响应工具事件（PreToolUse 等），可阻塞并回注反馈 | hooks 是插件内部机制（agent/hooks.go），用户不可配 shell 命令 | 🟡 |
| C-7 | 结构化记忆 | 四类记忆（user/feedback/project/reference）+ 索引每会话自动加载 | memory 插件（SQLite/HMEM），非自动加载的结构化记忆 | 🟡 |
| C-8 | 工作树隔离 | git worktree 一键隔离实验性修改 | 无 | 🔵 |
| C-9 | 延迟工具加载 | ToolSearch 按需加载工具 schema，未用工具不占上下文 | 工具定义全量注入；有技能渐进披露（SkillInjectMode: index）但工具层无 | 🔵 |
| C-10 | 联网检索 | WebSearch/WebFetch 内置 | 无（离线定位使然，可经 MCP 接入） | ⚪ 定位外 |
| C-11 | 计划模式深度 | 计划文件可迭代修订 + 子代理 plan 审批流 | `/plan` + `exit_plan_mode` 基本闭环已对齐 | 🟢 基本持平 |

### 3.2 重点展开

#### C-1 子代理类型系统（GAP-S1 的完整形态）

CodeBuddy 视角的完整形态 = 行为预设（**已有**）+ 工具白名单（**已有**，explore/review 强制摘除 edit/write）+ 用户自定义类型（缺）：

```
~/.mizar/agents/explorer.md
---
name: explorer
description: 只读探查：找文件、搜索代码、回答代码库问题
tools: [read, grep, find, ls, lsp_diagnostics]
model: 主模型    # 可省略，继承父级
---
你是探查型子代理，只做检索与阅读，禁止修改任何文件。
```

`delegate_task` 描述追加"可用类型：explorer(只读)、tester(跑测试)、…；不传 role 则通用"。内置 role → 工具白名单映射已落地（见 GAP-S1），剩余工作就是自定义类型文件本身。

#### C-2 任务清单带依赖（✅ 已实现）

`todo` 已升级为 `{id, text, done, blocked_by[]}`（internal/builtins/todo_pi.go）：

- `todo_add` 支持 `blocked_by`（校验存在性、剔除自身/重复/越界引用）
- `todo_toggle` 完成前置时自动解锁后继（返回"已解锁 #2"）；跳序完成提示未完成的前置；re-open 前置后继自动恢复等待（就绪状态是派生的）
- `list` 与 `/todos` 显示（等待 #n）标记
- 状态随会话回放持久化（与原机制同一条 `<!--todo-state:-->` 通道）
- delegate_task 描述与 DelegationGuide 引导：串行依赖先落 todo，tasks[] 只放互相独立的

剩余差距：owner/认领语义（多代理团队 C-3 时再做）。

#### C-5 权限分级（离线部署场景同样需要）

内网/私有化不等于免审批：bash 的 `rm -rf`、edit 生产配置仍需要闸门。方案：

- `PermissionMode`: default（危险工具需确认）/ acceptEdits（edit/write 放行）/ plan（只读工具放行）/ auto
- 落点在 `plugins.Manager.Call` 前 + Agent 循环 tool 执行前，一次实现主/子代理同时受益（子代理经 GAP-S2 的 PermissionFn 继承）

---

## 四、优先级路线图

| 优先级 | 项 | 工作量 | 对应缺口 |
|---|---|---|---|
| 🔴 P0 | 子代理权限门禁（PermissionFn + PermissionMode） | ~80 行 | GAP-S2、C-5 |
| 🟡 P1 | 子任务进度推送（OnProgress 订阅 → /ws 广播 + TUI 状态栏） | ~100 行 | GAP-S3 收尾、C-4 |
| 🟡 P1 | 扩展事件拦截 Decision（对齐 Pi） | ~30 行 | P-1 |
| 🟡 P1 | 子任务句柄随会话落盘回放 | ~60 行 | GAP-S4 |
| 🔵 P2 | 用户自定义子代理类型（agents/*.md，含 per-type model） | ~150 行 | GAP-S1 第二层、C-1、P-4 |
| 🔵 P2 | `/fork` 会话分支派生（复用 todo 回放机制） | ~100 行 | P-2 |
| 🔵 P2 | thinking level xhigh/max | ~10 行 | P-3 |
| 🔵 P2 | 用户级 hooks（shell 命令响应工具事件） | ~150 行 | C-6 |
| 🔵 P2 | 结构化记忆四分类 + 自动加载索引 | ~200 行 | C-7 |
| 🔵 P3 | per-subagent token/cost 计量 | ~50 行 | GAP-S5 |
| 🔵 P3 | worktree 隔离 | ~100 行 | C-8 |
| 🔵 P3 | 延迟工具加载 | ~120 行 | C-9 |
| 🔵 P3 | Project Trust 简化版 / export HTML / 图片读取 | 各~80 行 | P-5/6/7 |

> 已完成移出路线图：role 生效 + 行为预设、只读角色工具白名单强制（explore/review 摘除 edit/write）、排队/执行超时分离、steer_subagent 注册、委派时机指引（DelegationGuide）、按任务换模型、有限嵌套委派（v0.8.7/v0.8.8 交付）；todo 依赖 blocked_by + 自动解锁（C-2）、`/subagents` 内部命令、委派工具参数宽容剥壳、原生 `sleep(ms)` 宿主函数注册修复、子任务进度缓冲与查询（OnStep → Progress → get_subagent//subagents，2026-11-14 晚交付）。

---

## 五、涉及文件索引

| 文件 | 内容 |
|---|---|
| `internal/agent/subagent.go` | 子代理管理器（本文档第一节全部引用） |
| `cmd/mizar/main.go:716-723` | 五个委派工具注册 + DelegationGuide 注入 |
| `internal/llm/openai.go` | `WithModel`（modelDeriver 的实现侧） |
| `internal/builtins/todo_pi.go` | 平铺 todo（C-2 升级对象，回放机制复用点） |
| `internal/agent/hooks.go` | Hooks 挂载点（P-1 Decision 扩展点） |
| `internal/agent/clone.go` | Agent 克隆（子代理复用） |
| `internal/agent/steer.go` | 主循环 steer（子代理 Steer 对齐参考） |
| `internal/agent/loop.go:655` | Jobs.DrainDone 回灌通道挂载点 |
| `docs/pi-comparison.md` | Pi 快照对比（本文档第二节的前置） |
