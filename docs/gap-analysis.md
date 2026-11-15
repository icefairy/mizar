# Mizar 能力差距分析：vs Pi Agent / CodeBuddy

> 撰写日期：2026-11-14（同日更新：并入 v0.8.7/v0.8.8 子任务机制改动后的现状核对）
> 2026-11-15 晚更新：交付 C-7/C-8/C-9/P-5/P-6/P-7（结构化记忆、worktree 隔离、延迟工具加载、项目信任、HTML 导出、图片读取）
> 2026-11-15 晚更新：交付 C-7 结构化记忆（internal/memory 四分类 MEMORY.md 自动注入 + memory 工具/`/memory` 命令）
> 2026-11-15 更新：交付 P-1 扩展事件拦截（ToolCallDecision）、GAP-S4 子任务句柄随会话落盘回放、P-3 thinking level xhigh/max、GAP-S1 第二层用户自定义子代理类型（~/.mizar/agents/*.md）——四项已移出缺口清单，见路线图尾部
> 对比对象：Pi Agent（`@earendil-works/pi-coding-agent`，对照 `docs/pi-comparison.md` 快照 + CHANGELOG 0.85→1.4.x 已移植项）、CodeBuddy Code（Claude Code 系 CLI）
> 方法：逐文件核对 Mizar 当前源码（`internal/agent/subagent.go` 等以实际代码为准），与两者公开能力清单逐项比对
> 关联文档：`docs/pi-comparison.md`（2026-08 快照，部分项已修复，以本文档为准）、`docs/todo.md`（openclaude 14 项已全部完成）
> 版本锚点：subagent 相关现状对应 commit `528577f`（v0.8.8 收尾），21 项 subagent 测试全绿

---

## 〇、结论概要

Mizar 的子任务机制骨架完整、架构选择正确（子 agent = 独立 `Agent` 实例复用原生 `Run()` 主循环）。v0.8.7/v0.8.8 后已具备：**按任务换模型、有限嵌套委派、运行中 Steer、role 行为预设、排队/执行超时分离、委派时机指引（DelegationGuide）**。

当前欠缺集中在三个层面：

1. **子任务治理层**：运行中进度流式上报与权限门禁已补齐；剩 per-subagent token/成本计量与 WebUI 前端渲染收尾——CodeBuddy 成熟、Mizar 接近补完
2. **对 Pi**：分支树会话未移植（扩展事件拦截已补）
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

#### ✅ GAP-S1：用户自定义子代理类型（已实现，2026-11-15）

v0.8.8 已让 `role` 生效（行为边界注入，见 1.1）；只读角色工具白名单（第一层）与用户自定义类型（第二层）均已落地：

- **定义文件**：`~/.mizar/agents/*.md`，frontmatter（name/description/tools/model）+ 正文 = 该类型 system 附加层（`internal/agent/subagent_types.go`，与 SKILL.md 生态同构；解析失败的文件跳过并记日志，不阻断其余类型）
- **三层工具收窄**（runSubagent，互斥）：自定义类型 tools 白名单 > 只读内置角色摘写类 > 全量共享工具；白名单不约束嵌套 delegate_task（深度闸门另行控制）
- **per-type model**：req.Model 未指定时用类型默认 model，复用已生效的 modelDeriver 派生路径；req.Model 显式指定仍优先；实际生效值写回句柄
- **派发入口**：delegate_task 描述自动追加「可用自定义子代理类型」清单（启动加载后构建）；未知 role 仍归 leaf
- 句柄新增 `Role` 字段（get_subagent / /subagents「角色:」行可见）

#### 🟡 GAP-S2：权限门禁不延伸到子任务（~80 行）

主循环有 plan 审批、`ask_user_question` 确认、goal gate；但子 agent 拿到全量工具（bash/edit/write）**不带任何审批**——`delegate_task` 描述里只对 explore/review 摘除了写类工具，leaf/implement 与 bash 侧仍是直通。

**引入方案**：`SubagentConfig` 增加 `PermissionFn func(tool, args string) bool`，由 main.go 注入与主循环一致的审批逻辑；GAP-S1 的工具白名单本身也是一层软权限，两者叠加。

#### ✅ GAP-S2：权限门禁（已实现，PermissionMode）

对齐 Claude Code PermissionMode，主代理与子代理共用同一 `PermissionGate` 实例（internal/agent/permission.go）：

- **四模式**：`auto`（出厂默认 = 现行为零破坏，2026-11-14 用户确认）/ `default`（只读放行，写类+bash 危险命令需确认）/ `acceptEdits`（写类放行，bash 危险命令需确认）/ `plan`（只读放行，写类直接拒绝）
- **分级**：write/edit/skill_manage/win32 操作类 = TierEdit；bash 走命令级危险匹配（rm -rf、mkfs、dd of=/dev/、git push --force、curl|sh 等 12 条正则，宁可漏报不误报）；其余一律 Safe（含 mcp_call/插件/delegate_task——委派不落盘，风险由子代理内逐工具把关）
- **执行前审批**：runOneToolCall 在 Plugins.Call 之前 Authorize（不受 30s 插件超时影响）；拒绝按 guard 终止分支形状回填成对消息 + controlNextStep 跳过本批剩余
- **确认通路**：复用 builtins.SetAskUser（TUI/interactive/Server 三处接线，子代理同弹窗）；gate 内 askMu 串行化（TUI askPending 单槽不覆盖）；「本会话允许」缓存（CanonicalizeArgs 键序无关，容量 100 FIFO，仅内存不持久化）
- **headless fail-safe**：无交互环境 PermAsk 一律拒绝，文案给三条出路（用户确认）
- **接线**：config.json `permission_mode` / `/permission` 命令（查看/切换+持久化/reset）；子代理经 `SubagentConfig.Permission` 继承，clone 共享指针

#### 🟡 GAP-S3：WebUI 前端渲染收尾（推送链路已落地）

进度可见性三层已通：
1. **缓冲/查询**：Agent 实例级 `OnStep`（loop.go）→ 句柄 `progress` 缓冲（最近 20 条）→ `get_subagent.progress` / `/subagents`「进度」行
2. **TUI 推送**：`SubagentManager.SetOnProgress` → tui.go `deliverReminderHook` 注册 → QueueUpdateDraw 更新状态栏（仅 RUNNING 时显示，子任务完成后行自动消失）
3. **WebUI 推送**：serve 模式下 SetOnProgress → `EmitToWS("subagent_progress", {id,line})` 实时广播 /ws（不进回放日志：子代理管理器进程级共享无会话归属，断线重连由 get_subagent 兜底）

剩余差距：WebUI **前端**未渲染 `subagent_progress` 事件（前端在 dist/，独立工程）；后端契约已就绪。

#### ✅ GAP-S4：子任务记录落盘回放（已实现，2026-11-15）

照抄 todo 的「状态内嵌回放」模式（internal/agent/subagent_persist.go）：

- **落盘**：句柄到达终态（SUCCEEDED/FAILED/CANCELLED，含 Cancel 路径）时快照编码成 `<!--subagent-state:{json}-->` 标记消息，main.go 经 session.Store.Append 随会话 JSONL 追加持久化（`SubagentManager.SetPersister`）
- **回放**：会话恢复（启动 / `/session` 切换）时从 LoadRaw 全量历史提取每个 id 最后一次出现的快照（穿透压缩裁剪）合并进 subs map；崩溃时仍处 PENDING/RUNNING 的标记为 CANCELLED「进程重启中断」；只补缺不覆盖活句柄，id 计数器推到快照之后防撞
- **不进模型视野**：标记消息恢复时从 LLM 上下文与渲染中剔除（`StripSubagentStateMessages`），只服务于状态重建；会话文件保留原样（append-only）
- 长任务的子 agent 结论跨会话可引用（get_subagent / list_subagents / /subagents）

#### ✅ GAP-S5：token 计量（已实现，2026-11-15）

per-subagent token 计量：子代理 OnLLMStream 置为 usage 累加器（loop 据此走流式路径），usage 分片（`stream_options.include_usage` 下发的 final chunk）按请求累加进句柄 `TokensIn/TokensOut`。`/subagents` 渲染带 `tokens:入/出` 段（零计量不显示）；快照随 GAP-S4 落盘持久化。附带收益：子代理在途 LLM 请求也走 CancellableStreamToolCallLLM，支持真取消。非流式 LLM / 网关不下发 usage 时保持 0（best-effort）。

> 备注：v0.8.8 已修复的两项（role 死字段、排队/执行共用超时）与 steer 注册缺失已从本清单移除，见 1.1 现状表。

---

## 二、与 Pi Agent 的差距（pi-comparison.md 快照后的残留项）

下表只列**当前仍未解决**的项；`✅` 的项见 pi-comparison.md 第二节，不再重复。

| # | 功能 | Pi 实现要点 | Mizar 现状 | 建议 |
|---|---|---|---|---|
| P-1 | ~~扩展事件拦截~~ | tool_call 事件可 `deny/rewrite/intercept/allow` | ✅ 已实现（2026-11-15）：`Hooks.ToolCallDecision`（deny 立即生效 / rewrite 原地改写后继续 / allow 放行；panic fail-closed deny），接入 runOneToolCall 执行前，拒绝复用权限拒绝的消息形状 | 已补齐 |
| P-2 | ~~分支树会话~~ | `/tree` `/fork` `/clone`，JSONL + 分支摘要，交互回溯 | ✅ 简化版已实现（2026-11-15）：`/fork`（全量）/ `/fork <n>`（保留前 n 条）从当前上下文派生新 session ID 重放前缀，血缘记 `{"type":"fork"}` 元数据行（`ForkParent` 可查），标题带 "(fork)" 标记；todo 依赖随前缀回放。无 `/tree` 可视化与分支内回溯 | 已补齐简化版，`/tree` 按需再做 |
| P-3 | ~~thinking level 完整档位~~ | off/minimal/low/medium/high/xhigh/max | ✅ 已实现（2026-11-15）：auto/off/low/medium/high/xhigh/max，xhigh/max 透传 reasoning_effort（需网关支持） | 已补齐 |
| P-4 | **模型轮换/多 provider** | Ctrl+P / `/scoped-models` / models.json 多模型切换 | `/model` 单切换；子任务已可按任务指定模型（Model 字段生效），但主对话仍单模型 | 🟡 结合 GAP-S1 自定义类型（per-type model），价值大于 UI 级轮换 |
| P-5 | ~~Project Trust~~ | 项目级资源加载确认 + trust.json | ✅ 已实现（2026-11-15 简化版）：`internal/trust` + project_trust.json；首次进入含项目级 AGENTS.md/.cursorrules/USER.md/.mizar/prompts 的目录询问一次（非交互按拒绝），未信任则跳过注入；`/trust` 管理 | 已补齐简化版 |
| P-6 | ~~export HTML~~ | `--export` + highlight.js 归档 | ✅ 已实现（2026-11-15）：`internal/exporter` 自包含 HTML（内联 CSS + 极简关键字高亮，离线可用）；`-export -session <id>` / `-export -c` / `/export` 命令 | 已补齐（无 CDN，内网可用） |
| P-7 | ~~图片读取~~ | read 工具 MIME 检测转 image 附件 | ✅ 已实现（2026-11-15，opt-in）：`config image_read=true` 时 read 遇图片文件入队，Agent 每轮挂到最后一条 user 消息，LLM 层编码为多模态 content 数组 | 已补齐（需多模态网关） |

**已优于 Pi、不引入**（详见 pi-comparison.md 第六节）：技能索引注入原生实现、系统提示词强冻结、禁用走工具层不破缓存、MCP 客户端 + WS 桥、bash 默认超时、技能统计/周报/自动沉淀、CacheStats、Prompt Templates。

---

## 三、与 CodeBuddy 的差距（主要欠账）

### 3.1 差距总表

| # | 维度 | CodeBuddy | Mizar 现状 | 等级 |
|---|---|---|---|---|
| C-1 | 子代理类型系统 | 具名类型（Explore/Plan/自定义），每类独立工具集+描述，定义在 `.codebuddy/agents/` | role 行为预设 + 只读角色工具白名单 + 用户自定义类型（~/.mizar/agents/*.md，含 per-type model 与工具白名单）均已实现 | ✅ 基本持平（缺项目级目录） |
| C-2 | 结构化任务清单 | TaskCreate/List/Get/Update：依赖（blockedBy）、owner、状态机、activeForm，多代理共享 | `todo` 已有 blocked_by 依赖 + 自动解锁 + 会话回放；无 owner/认领语义 | 🟢 基本持平 |
| C-3 | 多代理团队 | TeamCreate + 共享任务列表 + 代理间消息协议（SendMessage），互相认领/阻塞 | 无 | 🟡 |
| C-4 | 后台代理 | 代理级后台运行 + 完成通知协议，主代理可继续其他工作 | delegate_task 异步 + DrainDone 回灌 + OnProgress 推送已对齐 | 🟢 基本持平 |
| C-5 | 权限模式 | plan/default/acceptEdits/bypassPermissions 多档 + 逐工具审批 + 子代理继承 | PermissionMode 四档已实现（auto/default/acceptEdits/plan），逐工具分级审批，子代理继承同一 gate；无逐工具白名单 UI | 🟢 基本持平 |
| C-6 | 用户级 Hooks | 用户 settings 配置 shell 命令响应工具事件（PreToolUse 等），可阻塞并回注反馈 | ✅ 已实现（2026-11-15）：config.json `hooks` 段（pre_tool_use/post_tool_use），stdin JSON 上下文 + MIZAR_HOOK=1；pre 可 deny（stdout JSON 或 exit 2）/rewrite，故障记日志放行；post 只观察；`/hooks` 命令查看 | ✅ 基本持平 |
| C-7 | 结构化记忆 | 四类记忆（user/feedback/project/reference）+ 索引每会话自动加载 | ✅ 已实现（2026-11-15）：`internal/memory` 四分类 MEMORY.md + 每会话自动注入系统提示 + memory 工具/`/memory` 命令读写；与 memory 插件（HMEM 语义检索）互补 | ✅ 基本持平 |
| C-8 | 工作树隔离 | git worktree 一键隔离实验性修改 | ✅ 已实现（2026-11-15）：`internal/worktree` + worktree_create/leave/list 工具；delegate_task 的 working_dir 现真正锚定子代理的 bash/read/write/grep/find/ls | ✅ 基本持平 |
| C-9 | 延迟工具加载 | ToolSearch 按需加载工具 schema，未用工具不占上下文 | ✅ 已实现（2026-11-15）：config `tool_inject_mode=index` 时系统提示+tools 只含核心工具，其余列索引，模型用 `tool_search` 按需加载（会话内保持） | ✅ 基本持平 |
| C-10 | 联网检索 | WebSearch/WebFetch 内置 | 无（离线定位使然，可经 MCP 接入） | ⚪ 定位外 |
| C-11 | 计划模式深度 | 计划文件可迭代修订 + 子代理 plan 审批流 | `/plan` + `exit_plan_mode` 基本闭环已对齐 | 🟢 基本持平 |

### 3.2 重点展开

#### C-1 子代理类型系统（✅ 已实现）

CodeBuddy 视角的完整形态 = 行为预设（已有）+ 工具白名单（已有）+ 用户自定义类型（已有，2026-11-15）：

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

`delegate_task` 描述自动追加"可用自定义子代理类型：explorer(只读)、tester(跑测试)、…；role 直接传类型名"。三层工具收窄互斥：类型白名单 > 只读角色摘写类 > 全量；per-type model 复用 modelDeriver 派生路径。与 CodeBuddy 的差异：CodeBuddy 支持项目级目录（.codebuddy/agents/），Mizar 目前只有用户级（~/.mizar/agents/），需要时加一个项目级加载点即可。

#### C-2 任务清单带依赖（✅ 已实现）

`todo` 已升级为 `{id, text, done, blocked_by[]}`（internal/builtins/todo_pi.go）：

- `todo_add` 支持 `blocked_by`（校验存在性、剔除自身/重复/越界引用）
- `todo_toggle` 完成前置时自动解锁后继（返回"已解锁 #2"）；跳序完成提示未完成的前置；re-open 前置后继自动恢复等待（就绪状态是派生的）
- `list` 与 `/todos` 显示（等待 #n）标记
- 状态随会话回放持久化（与原机制同一条 `<!--todo-state:-->` 通道）
- delegate_task 描述与 DelegationGuide 引导：串行依赖先落 todo，tasks[] 只放互相独立的

剩余差距：owner/认领语义（多代理团队 C-3 时再做）。

#### C-7 结构化记忆四分类（✅ 已实现）

对齐 CodeBuddy 的 MEMORY.md 设计，`internal/memory` 包（2026-11-15）：

- **存储**：`~/.mizar/memory/MEMORY.md` 单文件，固定四章节 `## user / ## feedback / ## project / ## reference`，每行 `- 事实`；未知章节/散条目容错（归 reference / 忽略），换行折叠保证「一行一事实」结构不被破坏。
- **自动注入**：启动时 `Store.Render()` 生成注入段 → `Agent.MemoryPrompt`（volatile 层，与技能索引同级）；注入上限 200 条 / 8000 字节，超限截断并给提示。`config.json memory_enabled=false` 可关闭自动注入（工具仍可用）。
- **写入即生效**：`memory` 工具（add/list/remove/count）与 `/memory` 命令（查看 / add / rm / clear）写后回调 `SetMemoryPrompt` 刷新并失效系统提示缓存，新事实当轮即可见。
- **与 memory 插件的分工**：插件 `mem_*` = 海量记忆语义检索（HMEM/向量）；本包 = 小而精、每会话必见的固定分类事实清单。

#### C-8 工作树隔离（✅ 已实现）

`internal/worktree` 包（2026-11-15）+ 三个工具，全部走 git 官方命令（不自己造目录结构）：

- **worktree_create**：`git worktree add -b <branch> <path> <base>`，默认路径 `<repo>.worktrees/<branch>`，已存在目录/branch 直接报错（不自动切换已有分支，避免两处同分支互相干扰）。
- **worktree_leave**：`git worktree remove [--force]`，只删 worktree 与元数据，**不删分支、不 merge**；有未提交改动需 `force=true`。
- **worktree_list**：解析 `git worktree list --porcelain`，展示分支/路径/locked/prunable。
- **working_dir 真正生效**（配套关键修复）：此前 `delegate_task.working_dir` 只写子 Agent 的 `WorkDir` 提示锚点，但 bash/read/write/grep/find/ls 全按进程 cwd 解析——隔离形同虚设。现 `internal/agent/workdir_wrap.go` 按工作目录包一层工具适配器：bash 命令前置 `cd "<dir>" && `（模型已显式 cd 则尊重），路径类工具把空/相对 path 锚到 `<dir>`、绝对路径原样。只作用于子代理（不改全局工具实现，避免影响主代理与并发会话）。

#### C-9 延迟工具加载（✅ 已实现）

照抄技能渐进披露（SkillInjectMode: index）思路，`internal/agent/tool_index.go`（2026-11-15）：

- **开关**：`config.json tool_inject_mode`（默认 full = 现行为零破坏；index = 按需加载）。
- **index 模式**：`ActiveTools()` 发给 LLM 的 tools 清单只含**核心工具**（bash/read/write/edit/grep/find/ls/respond/todo/ask_user_question/skill/委派系列/tool_search）+ **本会话已通过 tool_search 加载的工具**；被推迟的工具在系统提示里只列名称+一句话描述（静态索引，缓存友好）。
- **tool_search 工具**：按 `names`（精确）或 `query`（关键词模糊，名称命中权重更高）加载；加载后本会话保持可用，下一轮即可调用。
- **per-session 已加载集合**：`toolIndex` 随 Agent 实例隔离（Clone 继承注入模式但不继承加载集合），Web 多会话互不串台。

#### C-5 权限分级（离线部署场景同样需要）

内网/私有化不等于免审批：bash 的 `rm -rf`、edit 生产配置仍需要闸门。方案：

- `PermissionMode`: default（危险工具需确认）/ acceptEdits（edit/write 放行）/ plan（只读工具放行）/ auto
- 落点在 `plugins.Manager.Call` 前 + Agent 循环 tool 执行前，一次实现主/子代理同时受益（子代理经 GAP-S2 的 PermissionFn 继承）

---

## 四、优先级路线图

| 优先级 | 项 | 工作量 | 对应缺口 |
|---|---|---|---|

> 已完成移出路线图：role 生效 + 行为预设、只读角色工具白名单强制（explore/review 摘除 edit/write）、排队/执行超时分离、steer_subagent 注册、委派时机指引（DelegationGuide）、按任务换模型、有限嵌套委派（v0.8.7/v0.8.8 交付）；todo 依赖 blocked_by + 自动解锁（C-2）、`/subagents` 内部命令、委派工具参数宽容剥壳、原生 `sleep(ms)` 宿主函数注册修复、子任务进度缓冲与查询（OnStep → Progress → get_subagent//subagents）、OnProgress 推送（TUI 状态栏 + WebUI /ws 广播）、权限门禁 PermissionMode 四模式 + 子代理继承（GAP-S2/C-5，2026-11-14 晚交付）；**扩展事件拦截 ToolCallDecision（P-1）、子任务句柄随会话落盘回放（GAP-S4）、thinking level xhigh/max（P-3）、用户自定义子代理类型 ~/.mizar/agents/*.md（GAP-S1 第二层/C-1）**（2026-11-15 交付）；**/fork 会话分支派生（P-2 简化版，含 interactive/TUI 会话 ID 指针同步——顺带修复 /session 切换后落盘仍写旧文件的既有错位）**（2026-11-15 交付）；**用户级 hooks（C-6，config.json hooks 段 shell 命令响应工具事件）与 per-subagent token 计量（GAP-S5，流式 usage 累加进句柄）**（2026-11-15 晚交付）；**结构化记忆四分类 + MEMORY.md 自动注入索引（C-7，`internal/memory` 包 + memory 工具 + `/memory` 命令 + Agent.MemoryPrompt volatile 段）**（2026-11-15 晚交付）；**worktree 隔离（C-8，`internal/worktree` + worktree_create/leave/list 工具 + delegate_task working_dir 工具锚定）**（2026-11-15 晚交付）；**延迟工具加载（C-9，`config tool_inject_mode=index` + tool_search 按需加载 + ActiveTools 只发核心+已加载）**（2026-11-15 晚交付）；**Project Trust 简化版（P-5，`internal/trust` + project_trust.json + `/trust`）、export HTML（P-6，`internal/exporter` 自包含 + `-export`/`/export`）、图片读取（P-7，`config image_read` + read 入队 + LLM 多模态编码）**（2026-11-15 晚交付）。

---

## 四·五、与 Hermes Agent 的对比（2026-11-15 新增）

Hermes Agent（Nous Research，191k 行 Python，22 个消息平台）与 Mizar 定位不同赛道，逐子系统对比见 **`docs/hermes-comparison.md`**。

从 hermes 提炼的、值得 Mizar 补的三项（已进入待办）：

| 编号 | 项 | 说明 | 工作量 |
|---|---|---|---|
| H-1 | ~~会话全文搜索（FTS5）~~ ✅ 已交付 | `internal/sessionsearch`：SQLite FTS5 增量索引（trigram 中英文子串可搜）+ `session_search` 工具（搜索/上下文/整段/列表）+ `/search` 命令 | 已完成 |
| H-2 | ~~todo 压缩后重注入~~ ✅ 已交付 | `builtins.RenderForInjection` + `Agent.TodoInject`；Compactor 成功压缩后注入 pending 待办（完成项不注） | 已完成 |
| H-3 | ~~memory 字符预算分类型~~ ⛔ 已放弃 | hermes MEMORY.md 2200 字符 / USER.md 1375 字符硬上限 + 冻结快照；Mizar 现有 8000 字节/200 条上限已够用，按类型细分预算收益低而复杂度上升（多组阈值+超限策略）。**2026-11-15 决定不做**，如未来记忆条目真的膨胀到影响上下文再重评估 | 不做 |
| H-7 | ~~技能图谱 + 反思~~ ✅ 已交付 | `internal/learninggraph`（技能/记忆节点 + related_skills 边 + 词法重叠边 + 密度统计）+ `internal/reflect`（孤岛技能/高频工具/记忆脱节/低使用率 四类洞察）+ `/learn graph｜reflect｜json` | 已完成 |

**明确不学**：消息平台网关（22 平台 = 22 份维护）、自研浏览器/TTS/ASR/computer_use（用 MCP）、serverless 后端（modal/daytona，与离线定位冲突）。

## 五、涉及文件索引

| 文件 | 内容 |
|---|---|
| `internal/agent/subagent.go` | 子代理管理器（本文档第一节全部引用） |
| `internal/agent/subagent_types.go` | 用户自定义子代理类型（GAP-S1 第二层：加载/解析/SetAgentTypes） |
| `internal/agent/subagent_persist.go` | 子任务句柄落盘回放（GAP-S4：标记消息/回放/Restore） |
| `cmd/mizar/forkcmd.go` | /fork 分支派生核心逻辑（P-2：前缀重放/血缘/标题） |
| `internal/session/session.go` | 会话存储（fork 血缘记录 AppendFork/ForkParent） |
| `internal/userhooks/userhooks.go` | 用户级 hooks（C-6：pre 裁决/post 观察，stdin JSON 上下文） |
| `internal/memory/memory.go` | 结构化长期记忆（C-7：四分类 MEMORY.md 读写/渲染/注入上限） |
| `internal/worktree/worktree.go` | git worktree 隔离（C-8：Create/List/Remove/Prune，全走 git 官方命令） |
| `internal/builtins/worktree_tool.go` | worktree_create/leave/list 工具 |
| `internal/agent/workdir_wrap.go` | working_dir 工具锚定（C-8 配套：bash 前置 cd，路径类工具拼相对路径） |
| `internal/agent/tool_index.go` | 延迟工具加载（C-9：ActiveTools/延迟索引/tool_search/会话级已加载集合） |
| `docs/hermes-comparison.md` | 与 Hermes Agent 逐子系统对比（2026-11-15） |
| `internal/sessionsearch/sessionsearch.go` | 会话全文搜索（H-1：FTS5 增量索引/短词 LIKE 退化/会话清理） |
| `internal/builtins/session_search_tool.go` | session_search 工具（搜索/上下文/整段/列表四模式） |
| `internal/learninggraph/learninggraph.go` | 技能图谱（H-7：技能+记忆节点/related_skills 边/词法重叠边/密度统计） |
| `internal/reflect/reflect.go` | 反思引擎（H-7：孤岛技能/高频工具/记忆脱节/低使用率 + 图谱渲染 + LLM 蒸馏提示） |
| `internal/trust/trust.go` | 项目信任（P-5：project_trust.json 判定/ProjectResources/Ensure） |
| `internal/exporter/exporter.go` | 会话导出 HTML（P-6：自包含文档/代码高亮/工具折叠块/HTML 转义） |
| `internal/agent/image_sink.go` | 图片读取（P-7：待注入队列/ImageMIME/attachPendingImages） |
| `internal/builtins/memory_tool.go` | memory 工具（C-7：add/list/remove/count，写后刷新注入） |
| `cmd/mizar/main.go:716-723` | 五个委派工具注册 + DelegationGuide 注入 |
| `internal/llm/openai.go` | `WithModel`（modelDeriver 的实现侧）+ thinking 档位（P-3 xhigh/max） |
| `internal/builtins/todo_pi.go` | 平铺 todo（C-2 升级对象，回放机制复用点） |
| `internal/agent/hooks.go` | Hooks 挂载点（P-1 ToolCallDecision 已接入） |
| `internal/agent/clone.go` | Agent 克隆（子代理复用） |
| `internal/agent/steer.go` | 主循环 steer（子代理 Steer 对齐参考） |
| `internal/agent/loop.go:655` | Jobs.DrainDone 回灌通道挂载点 |
| `docs/pi-comparison.md` | Pi 快照对比（本文档第二节的前置） |
