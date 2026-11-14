# Mizar 能力差距分析：vs Pi Agent / CodeBuddy

> 撰写日期：2026-11-14
> 对比对象：Pi Agent（`@earendil-works/pi-coding-agent`，对照 `docs/pi-comparison.md` 快照 + CHANGELOG 0.85→1.4.x 已移植项）、CodeBuddy Code（Claude Code 系 CLI）
> 方法：逐文件核对 Mizar 当前源码（`internal/agent/subagent.go` 等以实际代码为准），与两者公开能力清单逐项比对
> 关联文档：`docs/pi-comparison.md`（2026-08 快照，部分项已修复，以本文档为准）、`docs/todo.md`（openclaude 14 项已全部完成）

---

> ⚠️ **2026-11-14 22:40 修正**：本文第〇节所列四项欠缺中，第 4 项「`steer_subagent` 已实现但未注册进 main.go」「`role` 字段仍未生效」「并发槽等待与执行共用超时」**已在 v0.8.8 全部修复**（注册见 `cmd/mizar/main.go:720`；role 现注入行为预设并归一化未知值；排队与执行超时已分离）。其余三项（子任务治理层、todo 依赖关系、Pi 扩展事件拦截/分支树）仍成立。

## 〇、结论概要

Mizar 的子任务机制骨架完整、架构选择正确（子 agent = 独立 `Agent` 实例复用原生 `Run()` 主循环），近期已补上**按任务换模型、有限嵌套委派、运行中 Steer** 三项。当前欠缺集中在四个层面：

1. **子任务治理层**：类型化预设、权限门禁延伸、过程可观测、结果落盘——CodeBuddy 成熟、Mizar 空白
2. **任务管理**：平铺 todo，无依赖关系（blockedBy）——并行 fan-out 的结果汇总直接受限
3. **对 Pi**：扩展事件拦截（deny/rewrite/intercept）、分支树会话两项核心未移植
4. **零碎**：`role` 字段仍未生效、`steer_subagent` 已实现但未注册进 main.go、并发槽等待与执行共用超时

---

## 一、子任务机制现状（internal/agent/subagent.go）

### 1.1 已实现能力（2026-11-14 代码核对）

| 能力 | 实现位置 | 说明 |
|---|---|---|
| 派发 | `DelegateTool` subagent.go:510 | 单任务 `{goal,...}` + 批量 `{"tasks":[...]}` fan-out，异步返回句柄 |
| 执行 | `runSubagent` subagent.go:229 | 独立 `Agent` 实例跑原生 `Run()`，自带解析/纠错/压缩/步数预算 |
| 防递归 | `isDelegationTool` subagent.go:374 | 过滤委派类工具；**有限嵌套已支持**：depth < maxDepth 时给子 agent 注册下一代 `delegate_task`（subagent.go:264-291） |
| 按任务换模型 | `modelDeriver` subagent.go:82 | `req.Model` 非空且 LLM 实现 `WithModel` 时派生新实例；不支持时回退主模型并在句柄标注 |
| 查询/取消 | subagent.go:395/418 | `get_subagent` / `list_subagents` / `cancel_subagent`，真 context cancel，幂等 |
| 运行中纠偏 | `Steer` subagent.go:439 | `SteerSubagentTool`（steer_subagent）已实现，经 `h.child.Steer` 非阻塞注入 |
| 并发控制 | sem 信号量 | 上限 10（`defaultMaxConcurrent`），超时排队不丢任务 |
| 结果回灌 | `notify` subagent.go:355 | 借 `Jobs.DrainDone()` 通知通道自动注入主对话（main.go:673） |

### 1.2 子任务机制差距清单

#### 🟢 GAP-S1：`role` 字段无效（bug 级，~20 行）

`SubagentLaunchRequest.Role`（subagent.go:44）在 `launchBatch` 里只做了默认值 `"leaf"`（subagent.go:198-199），**从未注入子 agent 提示词，也未用于过滤工具集**。模型传了 role 会被静默吞掉。

**引入方案**：`runSubagent` 构造 task 时拼入 role 描述；进一步可做 role → 提示词模板 + 工具白名单的映射表（见 GAP-S2）。

#### 🟢 GAP-S2：无子代理类型/预设系统（~150 行）

现状只有 `blocked_tools` 黑名单，无法定义"只读探查型"（只给 read/grep/ls）、"测试运行型"（bash 但禁 write/edit）等预设。CodeBuddy 的做法：

- 具名 agent 类型（Explore / Plan / general-purpose / 用户自定义），每类独立工具集 + 描述
- 描述注入主模型上下文，模型据此自主选择派哪类
- 用户可在项目目录自定义 agent 定义文件

**引入方案**：`~/.mizar/agents/*.md`（frontmatter: name/description/tools/model，正文为该类型 system 附加层）+ `SubagentManager` 启动时加载 + `delegate_task` 描述里列出可用类型。与现有 SKILL.md 生态同构，风格统一。

#### 🟡 GAP-S3：权限门禁不延伸到子任务（~80 行）

主循环有 plan 审批、`ask_user_question` 确认、goal gate；但子 agent 拿到全量工具（bash/edit/write）**不带任何审批**。`delegate_task` 的描述明说"子代理有完整工具能力"。CodeBuddy 的做法是权限模式（plan/default/acceptEdits/bypassPermissions）逐工具审批，且子代理继承父级权限档位。

**引入方案**：`SubagentConfig` 增加 `PermissionFn func(tool, args string) bool`，由 main.go 注入与主循环一致的审批逻辑；`tools` 白名单类预设（GAP-S2）本身也是一层软权限。

#### 🟡 GAP-S4：无过程可见性（~120 行）

只在完成时回灌一条 ≤300 字摘要（`notify` subagent.go:360-362）。运行中的子 agent：

- 步骤进度/当前工具调用对 TUI/WebUI 不可见
- 无流式输出
- 无逐子任务 token/成本记账

**引入方案**：`SubagentHandle` 增加 `Progress` 环形缓冲（最近 N 步："step 3: bash(ls -la)"）；给 Agent 加 OnStep 回调（现有 Hooks 已有挂载点，转发即可）；WebUI 走 `/ws` 推送，TUI 状态栏滚动显示。

#### 🟡 GAP-S5：子任务记录纯内存（~60 行）

`subs` map 不落盘，进程重启后 `get_subagent`/`list_subagents` 全部丢失，长任务的中间产物（子 agent 跑过的结论）无法跨会话引用。

**引入方案**：句柄快照随会话 JSONL 追加落盘（照抄 todo 的 `<!--todo-state:-->` 内嵌回放模式，`RebuildTodos` 同款）；或独立 `subagents.jsonl`。推荐前者，与现有回放机制一致。

#### 🔵 GAP-S6：小瑕疵（~15 行）

- **并发槽等待与执行共用 `req.TimeoutSecs`**（subagent.go:234）：等槽 290s + 执行只剩 10s 会被误判取消。应拆分 queue timeout / run timeout 两个参数。
- **token 计量缺失**：`StepsUsed` 有，但无 per-subagent token/cost，多模型混跑（GAP-S1 修复后）无法核算。
- **`DelegateTool` 恒传 depth=1**（subagent.go:523）：正确（主对话即第 1 层），但建议注释说明，避免后人误改。

#### 🔵 GAP-S7：`steer_subagent` 已实现但未接线（1 行）

`SteerSubagentTool`（subagent.go:610）存在，但 main.go:716-719 只注册了 Delegate/Get/List/Cancel 四个工具。补一行：

```go
pm.RegisterBuiltin(agent.SteerSubagentTool(a.SubAgents))
```

同时核对 `agent.Steer`（主循环 steer.go）与子代理 steer 的消息消费路径是否一致。

---

## 二、与 Pi Agent 的差距（pi-comparison.md 快照后的残留项）

下表只列**当前仍未解决**的项；`✅` 的项见 pi-comparison.md 第二节，不再重复。

| # | 功能 | Pi 实现要点 | Mizar 现状 | 建议 |
|---|---|---|---|---|
| P-1 | **扩展事件拦截** | tool_call 事件可 `deny/rewrite/intercept/allow`，32+ 事件类型 | Hooks 有 10 挂载点但无 Decision 枚举，工具调用前后不能改写/否决 | 🟢 ~30 行：`Hooks` 加 `OnToolCallDecision(ctx) Decision` 回调，Agent 循环响应。这是 Pi 生态扩展性的核心，也顺带为 GAP-C6（用户级 hooks）打地基 |
| P-2 | **分支树会话** | `/tree` `/fork` `/clone`，JSONL + 分支摘要，交互回溯 | 线性 JSONL + `--session` 续接；`agent.Clone()` 只用于子代理克隆（clone.go） | 🟡 简化版：`/fork` 从指定消息位置派生新 session ID 重放前缀。todo 已有按历史回放重建的机制（todo_pi.go），可直接复用 |
| P-3 | **thinking level 完整档位** | off/minimal/low/medium/high/xhigh/max | auto/off/low/medium/high，缺 xhigh/max | 🟢 ~10 行枚举扩展 |
| P-4 | **模型轮换/多 provider** | Ctrl+P / `/scoped-models` / models.json 多模型切换 | `/model` 单切换；GAP-S1 修复后子任务可指定模型，但主对话仍单模型 | 🟡 结合 GAP-S2：预设类型可带 model 字段，价值大于 UI 级轮换 |
| P-5 | **Project Trust** | 项目级资源加载确认 + trust.json | 无 | 🔵 简化版：首次加载项目级 config/技能时 TUI 确认一次 |
| P-6 | **export HTML** | `--export` + highlight.js 归档 | 无 | 🔵 Server 模式已有 `/rpc` 数据出口，HTML 导出优先级低 |
| P-7 | **图片读取** | read 工具 MIME 检测转 image 附件 | read 只出文本 | 🔵 多模态网关可用时再做 |

**已优于 Pi、不引入**（详见 pi-comparison.md 第六节）：技能索引注入原生实现、系统提示词强冻结、禁用走工具层不破缓存、MCP 客户端 + WS 桥、bash 默认超时、技能统计/周报/自动沉淀、CacheStats、Prompt Templates。

---

## 三、与 CodeBuddy 的差距（主要欠账）

### 3.1 差距总表

| # | 维度 | CodeBuddy | Mizar 现状 | 等级 |
|---|---|---|---|---|
| C-1 | 子代理类型系统 | 具名类型（Explore/Plan/自定义），每类独立工具集+描述，定义在 `.codebuddy/agents/` | 通用 delegate + `blocked_tools` 黑名单；`role` 字段无效 | 🔴 |
| C-2 | 结构化任务清单 | TaskCreate/List/Get/Update：依赖（blockedBy）、owner、状态机、activeForm，多代理共享 | `todo` 平铺 list/add/toggle/clear（todo_pi.go:25 `PiTodo{ID,Text,Done}`），无依赖 | 🔴 |
| C-3 | 多代理团队 | TeamCreate + 共享任务列表 + 代理间消息协议（SendMessage），互相认领/阻塞 | 无 | 🟡 |
| C-4 | 后台代理 | 代理级后台运行 + 完成通知协议，主代理可继续其他工作 | jobs 只管 bash 后台任务；子代理异步但无"代理后台跑、完成唤醒"的同级机制（delegate 已近似，缺通知粒度） | 🟡 |
| C-5 | 权限模式 | plan/default/acceptEdits/bypassPermissions 多档 + 逐工具审批 + 子代理继承 | plan 审批 + goal gate，无通用权限分级，子任务无审批（GAP-S3） | 🔴 |
| C-6 | 用户级 Hooks | 用户 settings 配置 shell 命令响应工具事件（PreToolUse 等），可阻塞并回注反馈 | hooks 是插件内部机制（agent/hooks.go），用户不可配 shell 命令 | 🟡 |
| C-7 | 结构化记忆 | 四类记忆（user/feedback/project/reference）+ 索引每会话自动加载 | memory 插件（SQLite/HMEM），非自动加载的结构化记忆 | 🟡 |
| C-8 | 工作树隔离 | git worktree 一键隔离实验性修改 | 无 | 🔵 |
| C-9 | 延迟工具加载 | ToolSearch 按需加载工具 schema，未用工具不占上下文 | 工具定义全量注入；有技能渐进披露（SkillInjectMode: index）但工具层无 | 🔵 |
| C-10 | 联网检索 | WebSearch/WebFetch 内置 | 无（离线定位使然，可经 MCP 接入） | ⚪ 定位外 |
| C-11 | 计划模式深度 | 计划文件可迭代修订 + 子代理 plan 审批流 | `/plan` + `exit_plan_mode` 基本闭环已对齐 | 🟢 基本持平 |

### 3.2 重点展开

#### C-1 子代理类型系统（最值得先做）

与 GAP-S2 同一件事，CodeBuddy 视角的完整形态：

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

`delegate_task` 描述追加"可用类型：explorer(只读)、tester(跑测试)、…；不传 role 则通用"。这同时解决 GAP-S1（role 生效）、GAP-S2（预设）、部分 C-5（白名单即软权限）、P-4（per-type model）。

#### C-2 任务清单带依赖（fan-out 的直接收益）

现状痛点：`delegate_task` 一次派 5 个子任务后，主 agent 只能轮询 `get_subagent` 拼结果；子任务间有先后依赖（先 build 再 test）时模型自己记。

方案：`todo` 升级为 `{id, text, status, owner, blocked_by[]}`：

- `todo_add` 支持 `blocked_by`；`todo_toggle` 完成任务时自动解锁后继（返回"已解锁 #3"）
- `delegate_task` 描述引导：派发前先 `todo_add` 落依赖，子任务完成回灌时模型对照清单推进
- 复用 todo 的会话回放持久化（状态 JSON 多几个字段而已）

工作量 ~100 行，是并行委派体验的最大短板修复。

#### C-5 权限分级（离线部署场景同样需要）

内网/私有化不等于免审批：bash 的 `rm -rf`、edit 生产配置仍需要闸门。方案：

- `PermissionMode`: default（危险工具需确认）/ acceptEdits（edit/write 放行）/ plan（只读工具放行）/ auto
- 落点在 `plugins.Manager.Call` 前 + Agent 循环 tool 执行前，一次实现主/子代理同时受益（子代理经 GAP-S3 的 PermissionFn 继承）

---

## 四、优先级路线图

| 优先级 | 项 | 工作量 | 对应缺口 |
|---|---|---|---|
| 🔴 P0 | 注册 `steer_subagent` + role 注入提示词 | ~25 行 | GAP-S7、GAP-S1 |
| 🔴 P0 | 并发槽/执行超时分离 | ~15 行 | GAP-S6 |
| 🔴 P0 | 子代理类型预设（agents/*.md） | ~150 行 | GAP-S2、C-1 |
| 🟡 P1 | 任务清单依赖（blocked_by） | ~100 行 | C-2 |
| 🟡 P1 | 子代理权限门禁（PermissionFn + PermissionMode） | ~80 行 | GAP-S3、C-5 |
| 🟡 P1 | 子任务进度上报（OnStep 回调 + Handle.Progress） | ~120 行 | GAP-S4 |
| 🟡 P1 | 扩展事件拦截 Decision（对齐 Pi） | ~30 行 | P-1 |
| 🟡 P1 | 子任务句柄随会话落盘回放 | ~60 行 | GAP-S5 |
| 🔵 P2 | `/fork` 会话分支派生（复用 todo 回放机制） | ~100 行 | P-2 |
| 🔵 P2 | thinking level xhigh/max | ~10 行 | P-3 |
| 🔵 P2 | 用户级 hooks（shell 命令响应工具事件） | ~150 行 | C-6 |
| 🔵 P2 | 结构化记忆四分类 + 自动加载索引 | ~200 行 | C-7 |
| 🔵 P3 | per-subagent token/cost 计量 | ~50 行 | GAP-S6 |
| 🔵 P3 | worktree 隔离 | ~100 行 | C-8 |
| 🔵 P3 | 延迟工具加载 | ~120 行 | C-9 |
| 🔵 P3 | Project Trust 简化版 / export HTML / 图片读取 | 各~80 行 | P-5/6/7 |

---

## 五、涉及文件索引

| 文件 | 内容 |
|---|---|
| `internal/agent/subagent.go` | 子代理管理器（本文档第一节全部引用） |
| `cmd/mizar/main.go:655-726` | SubagentManager 构造 + 工具注册（GAP-S7 修补点） |
| `internal/builtins/todo_pi.go` | 平铺 todo（C-2 升级对象） |
| `internal/agent/hooks.go` | Hooks 挂载点（P-1 Decision 扩展点） |
| `internal/agent/clone.go` | Agent 克隆（子代理复用） |
| `internal/agent/steer.go` | 主循环 steer（GAP-S7 对齐参考） |
| `internal/agent/loop.go:655` | Jobs.DrainDone 回灌通道挂载点 |
| `docs/pi-comparison.md` | Pi 快照对比（本文档第二节的前置） |
