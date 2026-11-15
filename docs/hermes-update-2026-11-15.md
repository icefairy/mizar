# Hermes 本次更新增量分析（2026-11-15）

> 更新区间：`39faafb616`（2026-09-29，上次本地 HEAD）→ `951ec09284`（2026-11-15 01:16，本次 pull 后 HEAD）
> 提交规模：25,849 个提交（含合并），非合并 25,279 条。版本 `0.21.5+30382.g951ec09`
> 对比基准：Mizar v0.8.9（本机 `/data/codes/mizar`）
> 方法：`git log 39faafb..HEAD` 全量导出，按 scope 聚合，逐条读 feat 与核心 fix 的实现

---

## 〇、先说实话：绝大部分更新与 Mizar 无关

按 scope 统计本次新增提交（非合并 25,279 条）：

| scope | 条数 | 与 Mizar 相关性 |
|---|---|---|
| `gateway` | 数千 | ❌ 消息网关（22 平台）——定位外 |
| `telegram` / `slack` / `discord` / `feishu` / `wecom` / `whatsapp` / `matrix` / … | 数千 | ❌ 平台适配 |
| `desktop` | 数百 | ❌ Electron 桌面端 |
| `pm` / `move` / `compat` / `catalog` | 数百 | ❌ 包管理/模块迁移/插件目录 |
| **`agent` / `tools` / `delegate` / `plugins` / `skills` / `memory`** | **约 40 条 feat + 若干 fix** | ✅ **有价值** |

真正值得看的只有下面这些。

---

## 一、值得吸纳的 6 个设计（按价值排序）

### ⭐ 1. 子代理「续跑」：`action=continue` + 崩溃可恢复

**Hermes 做了什么**（11-11 → 11-13 三次提交演进）：

```python
delegate_task(action="continue", subagent_id="sa-0-1a2b3c4d", message="now add tests for it")
```

- 给**已完成**的子代理追加一轮：重建同一个 route（模型/provider/profile）+ 同一套 toolsets，历史从**已保存的 transcript** 加载，`message` 成为它的下一轮
- 保留 `subagent_id` 和 session（transcript 追加，像 `--resume`），作为新的一次 background delegation
- **崩溃可恢复**：每个子代理在 `state.db` 有持久记录，gateway 重启/硬杀/断电后 `continue` 仍可用
- **auto_resume（opt-in）**：gateway 每 15 秒扫一遍，自动续跑「非主动停止」的子代理；最多 2 次自动尝试、deadline 360 分钟、给 parent 只报一次结果
- 明确的**永不续跑**清单：主动 stop、auth/额度/模型不存在这类确定性失败、嵌套子代理、worktree pin 已被回收

**Mizar 现状**：`subagent.go` 有 `steer_subagent`（运行中纠正）+ 句柄落盘回放（GAP-S4），但**子代理一旦终态就彻底结束**，无法追加对话。

**建议（Mizar 版）**：
- `delegate_task` 加 `{"action":"continue","id":"sub-3","message":"..."}` → 用已落盘的句柄 + 历史重建 child Agent，跑一轮
- 我们的 GAP-S4 已经把句柄快照落进会话 JSONL，但**没落子代理自己的对话历史** → 需补 `subagent_transcript`（或直接把 child 的 Initial 也存进快照）
- auto_resume 对 Mizar 意义不大（无 gateway 常驻），**但 `continue` 很有用**

---

### ⭐ 2. 自学习节奏信号：`review_signals`（把「该不该复盘」变成公开契约）

**Hermes 做了什么**：

```python
# agent/review_signals.py（64 行 + 162 行测试）
skill_review_due    = iters_since_skill >= skill_nudge_interval and "skill_manage" in tools
memory_review_due   = turns_since_memory >= memory_nudge_interval
# post_llm_call 钩子里带上这四个只读字段（ReviewSignals 是只读 dict，插件改不了）
```

- 关键点：**子代理/插件做自己的「回合后工作」时，可以复用主 Agent 的节奏判断**，而不是自己数工具调用
- `ReviewSignals` 做成**只读 dict**（`__setitem__` 全部抛 TypeError）——一个实例传给所有钩子，谁也别想篡改
- 节奏用**两个独立计数器**：记忆看「用户轮数」、技能看「工具迭代数」（而不是统一按轮次）

**Mizar 现状**：`BackgroundReview` 只有一个 `turnsSinceLastReview`（按轮数），且我刚修完「从未启动」的问题。

**建议**：这个设计直接可用。把 `Advance()` 拆成两个计数器（`turnsSinceMemory` / `itersSinceSkill`），并把 due 标志通过 `Hooks.OnStepEnd` / 用户级 hooks 暴露成只读；`ReviewSignals` 的「只读」做法也值得学（我们现在的 hook 是传指针 struct，插件理论上能改）。

---

### ⭐ 3. 插件能力面：`ctx.fork_session` / `ctx.register_idle_task` / `ctx.redact_text`

**Hermes 做了什么**（11-13 三连提交）：

| 能力 | 作用 | 护栏 |
|---|---|---|
| `ctx.fork_session` | 插件在回合后 fork 一个**有界**的后台会话副本（同 system prompt / tools / 记忆 / 热缓存），跑一次多步任务 | 工具白名单只含该插件自己的工具 + `FORK_SAFE_CORE_TOOLS`（**空集**！）；危险命令审批自动拒绝；总输入预算；迭代上限 8~16；parent 开始下一轮时取消 |
| `ctx.register_idle_task` | 插件任务挂到 curator 的空闲调度上（280 行） | 空闲时才跑，不抢主循环 |
| `ctx.redact_text()` | 插件脱敏，不必 import 内部 `agent.redact` | 公开 API 边界 |

**最值得学的是 `FORK_SAFE_CORE_TOOLS: frozenset() = frozenset()`** —— 这个**空集**是刻意的设计声明：
> 「只有一个地方能标记核心工具为 fork-safe。答案是一个都没有——连 `skill_view` 都不行，因为它能渲染技能里的内联 shell。」

**Mizar 现状**：插件系统是「导出 `tool_*` 就注册工具」，**没有 ctx 能力面**（只有宿主函数 `fs_read`/`http_get` 这类）。插件想「回合后做事」只能自己起 goroutine，没有预算/白名单/取消语义。

**建议**：
- 短期可行：给插件宿主函数加 **`redact_text()`**（我们目前**完全没有脱敏**——这是个真实缺口，见下条）
- 中期：`fork_session` 太重，但「**插件注册回合后回调 + 预算 + 工具白名单**」这个模式可以简化落地

---

### ⭐ 4. 敏感信息脱敏（`redact`）—— Mizar 完全缺失

**Hermes 本次的 fix 密度很高**（14 条 `fix(security)`）：

- `feat(security): opt-in security.redact_phone_numbers`（E.164 手机号）
- `fix(redact): 也抓裸 AQ.- 前缀的 Gemini key`
- `fix(trajectory): 落盘 trajectory 前脱敏`
- `fix(api-server): 流式 chat completions 里脱敏 agent 错误`
- `fix(sessions): export --redact` 两 key 掩码成同一文本时仍保留每条
- `fix(skills): 别把明文密钥写进 oss-forensics 工作文件`

**Mizar 现状**：`grep -rn "redact" internal/` → **零结果**。也就是说：
- 会话 JSONL 里可能有 API key / token 明文
- `-export` 导出的 HTML 会原样带出
- 诊断日志 `~/.mizar/logs` 可能带密钥
- 工具报错回传给模型时也不脱敏

**这是本次分析里最值得立刻补的一项**（不是「借鉴设计」，是补真实缺口）。

**建议**：`internal/redact` 包（约 150 行 + 测试）：
- 正则集：`sk-`/`sk-ant-`/`AKIA`/`AQ.`/`ghp_`/`xoxb-`/Bearer token/`-----BEGIN * PRIVATE KEY-----`/E.164 手机号
- 接入点：会话 Append 前、导出前、diag.Logf、工具结果回填
- 配置 `security.redact` 开关

---

### 5. 凭据池的「先选后用」（`choose` / `commit` / `peek` 三段式）

**Hermes 做了什么**：

```python
select()  # 轮换 + 计数（旧）
choose()  # 只选，不记账
commit()  # 用上了才记账
peek()    # 只读
```

关键洞察：**旧版 `select()` 一选就计数**，于是「选了但请求最终没发出」（缓存命中、提前失败、走了别的 key）也被计入轮换，导致 key 使用失衡。新版把「选」和「记录」拆开。

**Mizar 现状**：`config.Providers` 是多供应商收藏夹（不同 baseURL），**没有同 provider 多 key 池**——我在上次对比里已列为 H-4（未做）。

**建议**：如果要做 H-4，直接采用这个三段式；不做也可以记住这个教训（**计数点应放在「真的用上了」之后**，而不是「决定用它」时）。

---

### 6. `tool_search` 的默认延迟 + 三桥接

**Hermes 做了什么**（本次新增）：

- `feat: defer manage_connections behind tool_search by default`
- `feat: defer text_to_speech behind tool_search by default`
- 桥接是**三个**工具：`tool_search`（找）/ `tool_describe`（看 schema）/ `tool_call`（调用）
- 关键设计：**catalog 无状态**（每次组装都从活的 tool-defs 重建）——注释里明确说「session-keyed 的 catalog 会漂移并静默丢工具」

**Mizar 现状**：`tool_inject_mode=index`（opt-in）+ 单 `tool_search`（搜索即加载，下一轮可调，**不需要 describe/call 桥接**）。

**判断**：我们的两段式（search → 直接调）比三段式**更省一轮 LLM**，不必改。但两点值得吸收：
- ✅ **默认延迟长尾工具**（我们现在是 opt-in，默认 full）——可以考虑默认把「多模态/远程类」几个工具延迟
- ✅ **catalog 无状态**——我们已经是每次 `ActiveTools()` 从 `Plugins.Tools()` 现算，符合这条；值得写进注释防止后续优化成缓存

---

## 二、明确不用学的

| 项 | 原因 |
|---|---|
| `gateway tickets`（SSH 客户端票据）、per-profile 票据 | 消息网关专属 |
| `cron_fresh_thread_per_run` / `wrap_response` | 无 cron 投递需求 |
| `opt-in dm_threads: shared`（Telegram DM 分线程） | 平台专属 |
| `AutoRAG` catalog 插件 | 云端插件市场，Mizar 无 |
| Gemini Interactions API / 各种新模型 | 走 OpenAI 兼容网关，模型无关 |
| `MOVES` 模块迁移工具链 | Python 包重构专用 |
| `Scope`（home/secrets/terminal policy 三合一） | 多租户云端场景；我们单机 |
| desktop Bot Mode 一堆 fix | Electron 桌面端 |

---

## 三、建议落地顺序

| 优先级 | 项 | 工作量 | 理由 |
|---|---|---|---|
| **P0** | **敏感信息脱敏 `internal/redact`** | ~150 行 | **真实缺口**（会话/导出/日志/错误回填全裸奔），不是「借鉴」 |
| P1 | 子代理 `action=continue`（续跑终态子代理） | ~120 行 | 需要补落子代理 transcript；对长任务链很有用 |
| P2 | `review_signals` 双计数器 + 只读语义 | ~60 行 | 直接改进刚修好的 BackgroundReview |
| P3 | 插件 ctx：先做 `redact_text`，后考虑「回合后回调 + 预算 + 白名单」 | ~80 / ~300 行 | 插件生态能力面的第一步 |
| P4 | `tool_search` 默认延迟长尾工具 | ~30 行 | 小改动，省上下文 |
| — | 凭据池三段式 | 随 H-4 | 记住「计数点放在真的用上之后」 |

---

## 附：本次更新的时间线要点

```
11-06  delegate: 子代理可被 subagent_id 寻址；subagent.list 行列 session
11-08  curator: post_curator_run 插件钩子
11-09  browser-vault 插件 API 公开（LoginBackend 等）
11-10  tool_search: 默认延迟 text_to_speech / manage_connections
       tools: 拒绝缺必填参数的内置工具调用，未知参数要标记
       三个 Scope 提交（home/secrets/terminal policy 合一）
11-11  delegate: continue 已完成子代理（自有会话）
       sandbox: hosted scope 的工具只能触达自己的沙箱
11-12  gemini: Interactions API 默认（新模型）
11-13  ★ plugins: ctx.fork_session / ctx.register_idle_task / post_llm_call review_signals
       ★ plugins: ctx.redact_text()
       ★ delegate: delegation.auto_resume（opt-in 自动续跑）
11-14  credentials: choose/commit 三段式选 key
       security: redact_phone_numbers 等一批脱敏 fix
       tools: FTS 命中与 live context 同源时正确披露
```
