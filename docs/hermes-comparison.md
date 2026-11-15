# Hermes Agent 对比分析（vs Mizar）

> 分析对象：**Hermes Agent**（Nous Research，`/root/.hermes/hermes-agent`，22.6k Python 文件 / 191k 行，不含测试）
> 对比基准：**开阳 Mizar**（Go 单二进制，`/data/codes/mizar`，76k 行 Go，33 包测试全绿）
> 日期：2026-11-15
> 方法：直接读 hermes 源码目录（`tools/` 340 文件、`agent/` 311 文件、`gateway/` 175、`plugins/` 241），逐子系统比对 Mizar 本地实现
> 关联文档：`docs/pi-comparison.md`（Pi 对比）、`docs/gap-analysis.md`（CodeBuddy/Pi 缺口总账）

---

## 〇、一句话结论

两者**定位完全不同**，不是同一赛道的竞品：

| | Hermes | Mizar |
|---|---|---|
| 定位 | **云端常驻、多平台消息机器人** + 自进化研究代理 | **离线/内网单文件编码 Agent** |
| 部署 | Python 3.2G（venv + node_modules），需装依赖 | 一个静态 Go 二进制，拷过去就跑 |
| 扩展 | Python 插件 + TS 前端 + MCP | TS/JS 插件热重载 + SKILL.md + MCP |
| 强项 | 消息网关 22 平台、学习闭环、会话全文搜索、多模态全家桶、终端后端 6 种、多凭据池 | 零依赖单文件、内嵌 Zig 工具链、Zig→wasm 内核、简洁可控、离线 |
| 代码量 | 191k 行 Python | 76k 行 Go |

**Mizar 在「自举/离线/极简」这条路上更彻底**（内嵌 Zig 编译器、wasm 内核是 hermes 没有的）；
**Hermes 在「生态广度」上完全是另一个量级**（22 个消息平台、18 个 TTS 文件、18 个 computer_use 文件、346 人的系统化工程）。

下面逐项列出**真实的差距**（不是营销话术），并标注哪些值得 Mizar 借鉴。

---

## 一、Mizar 已覆盖 / 更优的项

| 能力 | Hermes 做法 | Mizar 现状 | 结论 |
|---|---|---|---|
| 单文件部署 | Python 解释器 + venv + node_modules | 静态 Go 二进制（内嵌 Zig 归档仍 ~74MB） | **Mizar 胜**（唯一能拷一个文件到内网跑的） |
| 内嵌编译器 | 无 | `/zig init` 释放 Zig 工具链，现场编 C/Zig | **Mizar 独有** |
| WASM 计算内核 | 无 | `internal/wasmkern`，wazero 纯 Go 跑 Zig wasm | **Mizar 独有** |
| 插件热重载 | Python 插件需重启（除 hook） | TS/JS 写盘即热重载（esbuild + goja） | **Mizar 胜** |
| 权限门禁 | `approval.py` 8 文件（多层：detection/floors/smart guardian/human wait） | `PermissionGate` 四模式单文件（~300 行） | 各自取舍：hermes 更细，mizar 更简 |
| 子代理委派 | `delegate_tool` 10+ 文件，含 live log/output schema/toolset 配置 | `subagent.go` 单文件，含进度/落盘/嵌套/steer | **Mizar 更紧凑**（功能相当） |
| 会话压缩 | `context_compressor` + 多文件（summary/archive/reply_anchor/marker） | `compactor.go`（Goal/Progress/Decisions/Next Steps 结构化） | 相当 |
| OpenAI 兼容 Server | 有（`hermes-api-server` toolset） | HTTP + RPC + WS 三协议 | 相当 |
| 技能系统 | SKILL.md + skills_hub（远程安装） | SKILL.md + 分类目录 + 每周统计 | hermes 有远程 hub，mizar 无 |
| 工具注入 | `tool_search` + `tool_describe` + `tool_call` 三桥接（BM25 + snowballstemmer 词干） | `tool_search` 单工具（关键词匹配） | hermes 更成熟 |

---

## 二、Hermes 有、Mizar 缺（按价值排序）

### 🔴 P1 值得补（成本低、价值高）

#### H-1 会话全文搜索（`session_search`）
- **Hermes**：796 行，**SQLite FTS5** 索引全部会话，4 种模式合一（DISCOVERY 搜索 / SCROLL 上下文窗口 / READ 整段 / BROWSE 列表），deduped by lineage，cron 会话降权防「回溯盲区」（BM25 被 cron 词淹没）。**不调 LLM**，纯 DB 查询。
- **Mizar**：会话是 JSONL append-only，`internal/session` 有 Load/List/ScopedList，**无全文搜索**——只能靠 `/sessions` 列表 + 手动 grep 文件。
- **建议**：`internal/session/search.go`，用 `modernc.org/sqlite`（已在依赖里）+ FTS5 建增量索引（会话追加时 upsert）。~300 行可落地。这是「长会话跨会话回溯」的硬需求。

#### H-2 todo 压缩后重注入（`format_for_injection`）
- **Hermes**：`todo_tool.py` 明确——todo 列表在**每次上下文压缩后重新注入**（只注入 pending/in_progress，完成项不注入以免模型重做），且有 **parent/子任务层级** + **revision 单调递增**（UI 拒绝陈旧更新）。
- **Mizar**：`todo_pi.go` 有 `blocked_by` 依赖 + 回放持久化，但**压缩后不重注入**——上下文一压，todo 就从模型视野里消失（只剩回放时恢复内存状态，不进 prompt）。
- **建议**：在 `Compactor.Compact` 后（或 `buildVolatileSection`）追加「当前进行中的待办」段。~50 行。**这是真实缺陷**（长任务压缩后模型可能忘记自己在干什么）。

#### H-3 memory 双文件语义（MEMORY.md + USER.md）
- **Hermes**：`MEMORY.md`（agent 自己的笔记，2200 字符上限）+ `USER.md`（用户画像，1375 字符上限），**冻结快照**——session 开始时注入系统提示，会话中写入只落盘不改 prompt（保前缀缓存不破）。
- **Mizar**：`internal/memory` 四分类（user/feedback/project/reference）单文件，**即时刷新**（`SetMemoryPrompt` 失效缓存，本轮即可见）。
- **差异分析**：mizar 的「即时可见」会破坏一次前缀缓存；hermes 的「冻结快照」省缓存但本轮看不到新事实。两者各有理。
- **结论（2026-11-15）**：mizar 现有 8000 字节/200 条上限已足够防膨胀；按类型细分预算会增加多组阈值与超限策略，收益不抵复杂度，**决定不做**。如未来记忆条目真膨胀到影响上下文再重评估。

### 🟡 P2 中价值（按需做）

#### H-4 多凭据池 + 模型冷却（`credential_pool`）
- **Hermes**：同一 provider 多 key 轮换 + 每个 model 的 cooldown（失败后冷却期不走它）+ `rate_limit_tracker`。
- **Mizar**：`config.Providers` 是「多供应商收藏夹」（不同 baseURL），**无同 provider 多 key 池**，重试只在 `WeakModelTuner`（固定次数退避）。
- **建议**：`internal/llm` 加 key 池（同 baseURL 多 key，轮换 + 失败标记），~150 行。内网多 key 场景少，优先级中。

#### H-5 execute_code / 程序化工具调用（PTC）
- **Hermes**：LLM 写 Python 脚本，通过 RPC 调工具，多步管道**压缩成一轮**（只回 stdout）。本地用持久 session kernel（Unix socket），远程用 request 文件轮询。3 个文件。
- **Mizar**：无。模型只能逐个 tool_call，多步链每步都走一轮 LLM（token 多、慢）。
- **建议**：Mizar 可以给 `bash` 工具加「脚本模式」或用 goja 跑 JS 脚本调工具。但**这违反「一步一工具」的简洁哲学**，且内嵌 JS 引擎已有——可做成 opt-in 的 `execute_js` 工具（把 tool call 用 goja 桥接到 plugins.Call）。工作量中等。

#### H-6 MoA（Mixture-of-Agents）
- **Hermes**：`/moa` 标记单轮为多模型参考（1492 行 `moa_loop.py`），主循环仍管工具调用，只是每轮前用辅助模型聚合参考。
- **Mizar**：只有主模型 + `AuxLLM`（judge/review 用），无 MoA。
- **建议**：Mizar 的 `AuxLLM` 架构已经能支撑简化版 MoA（用 2-3 个辅助模型产出参考，注入主模型）。中低优先级（价值取决于用户是否有多个可用模型）。

#### H-7 学习闭环可视化（`learning_graph`）
- **Hermes**：把「学到的技能 + MEMORY.md/USER.md 记忆块」建成图（技能间 `related_skills` 边 + 记忆↔技能的词法重叠边），桌面端展示。配套 `insights.py`。
- **Mizar**：有 `background_review`（自动沉淀技能/memory）+ 技能统计周报，**无可视化图谱**。
- **建议**：Mizar 已有 `MemGraph`（mem_graph 工具看图谱），但那是 HMEM 插件的；技能/memory 无关系图。低优先级（除非做桌面 UI）。

### 🔵 P3 定位差异 / 暂不做

| 项 | Hermes | Mizar 判断 |
|---|---|---|
| **消息平台网关** | 22 个平台（telegram/discord/slack/whatsapp/signal/email/feishu/dingtalk/wecom/matrix/teams/irc/line/mattermost/ntfy/sms/simplex/google_chat/homeassistant/a2a/buzz/raft/photon），175 文件 | **明确不做**——Mizar 定位内网 CLI，接消息平台是另一个产品。需要的可用 MCP/插件外挂 |
| **终端后端 6 种** | local / docker / ssh / **daytona** / **modal**（serverless）/ singularity | Mizar 只有本地 shell。**docker/ssh 值得考虑**（`internal/shell` 加 backend 抽象），~200 行；daytona/modal 是云服务，不做 |
| **浏览器工具族** | 12 文件：CDP / cloud(browser-use) / lightpanda / vault（密码库填充）/ snapshot / vision | Mizar 无。**可考虑**用 MCP 接 Playwright/Puppeteer，不自研 |
| **多模态全家桶** | vision(5) / image_gen(5) / video_gen(3) / **tts(18)** / transcription(8) / wake_word / computer_use(18) | Mizar 只有 `image_describe` 插件 + 刚做的 `image_read`。vision 有，TTS/ASR/computer_use 无——**都可用 MCP/插件外挂**，不自研 |
| **cron 调度器** | 带平台投递 + ESTOP 全局急停 + kanban 看板 | Mizar 的 `schedule` 只在会话活跃期投递，**不持久化、无系统级 cron**。可考虑加「落盘 + 后台 daemon」版本 |
| **kanban 看板** | 多代理共享任务看板（16 个 kanban_* 工具） | Mizar 的 `todo` 是单会话的；多代理团队任务看板是 CodeBuddy C-3 的同类项，**暂不做** |
| **skills hub 远程安装** | 从 agentskills.io / LobeHub / GitHub / browse.sh 装技能（9 文件） | Mizar 技能只从本地 `~/.mizar/skills` 加载。**可选**：加 `skill_install <url>` 拉 SKILL.md |
| **ACP adapter** | 接入 Zed 等编辑器（acp_adapter/） | Mizar 有 `--lsp-server` 但无 ACP。**暂不做** |
| **research 批量轨迹生成** | batch_runner / trajectory_compressor（训练数据） | 定位外，不做 |
| **多语言 README + docusaurus 站点** | es / ur-pk / zh-CN + 完整网站 | Mizar 有中文 README + 极简 gh-pages，够用 |

---

## 三、架构层面的值得注意差异

### 3.1 复杂度差距

| 维度 | Hermes | Mizar |
|---|---|---|
| 工具实现 | 340 个 Python 文件（单 `browser_*` 就 12 个） | `internal/builtins` ~25 个 Go 文件 |
| Agent 循环 | 311 个文件（`turn_*.py` 拆成 30+ 细粒度模块） | `internal/agent` ~60 个文件（loop.go 单文件 ~1300 行） |
| 网关 | 175 个文件 | 无（只用 `internal/server` ~30 文件做三协议） |
| 插件 | 241 个文件（每个插件多文件 + manifest） | 单 `.ts` 文件热重载 |

**判断**：Hermes 是把「Agent 当分布式系统」在工程化（每件事一个模块 + 测试 + 迁移）；Mizar 是把「Agent 当一个想让人读懂的工具」。这不是谁对谁错——Mizar 的 76k 行里没有一行是为了「支持 22 个平台」而存在的负担。

### 3.2 memory 注入语义差异（重要）

```
Hermes： session 开始 → MEMORY.md/USER.md 冻结成快照 → 进 system prompt
         会话中 memory 工具写入 → 只落盘 → prompt 不变（前缀缓存零破坏）
Mizar：  启动 → Render() → Agent.MemoryPrompt → 进 volatile 层
         会话中 memory 工具写入 → SetMemoryPrompt → 失效缓存 → 下一轮 prompt 变
```

**取舍**：mizar 换来「写入当轮可见」，代价是每次写 memory 缓存失效一次（新事实通常值得这代价）；hermes 宁可本轮看不到也要保缓存稳定。**两者都合理，取决于会话长度与写 memory 频率**。

### 3.3 工具调度差异

Hermes 的 `tool_search`/`tool_describe`/`tool_call` 是三桥接（先搜 → 看 schema → 调用），Mizar 刚做的 `tool_search` 是单工具（搜到即注入 schema，下一轮直接调）。hermes 多了 **BM25 打分 + snowballstemmer 词干**（英文检索质量更好），Mizar 是简单子串匹配（中文场景其实够用）。

---

## 四、建议落地顺序

| 优先级 | 项 | 工作量 | 理由 |
|---|---|---|---|
| 1 | **H-2 todo 压缩后重注入** | ~50 行 | 修真实缺陷：长任务压缩后模型丢失 todo |
| 2 | **H-1 会话全文搜索 FTS5** | ~300 行 | 长会话回溯的硬需求，SQLite 依赖已在 |
| ~~3~~ | ~~H-3 memory 字符硬上限分类型~~ ⛔ 已放弃（2026-11-15） | ~30 行 | 现有 8000 字节/200 条上限够用，按类型细分收益低、复杂度升 |
| 4 | H-4 同 provider 多 key 池 | ~150 行 | 多 key 场景少，但内网友好 |
| 5 | H-5 execute_code/JS 脚本工具 | ~200 行 | 多步链省 token，需评估是否破简洁哲学 |
| 6 | 终端后端 docker/ssh | ~200 行 | 远程/隔离执行 |
| — | 消息平台 / TTS / computer_use / 浏览器 / kanban | 不做 | 定位外，用 MCP/插件外挂 |

---

## 五、Mizar 不该学的地方（保持克制）

1. **不要把 Agent 拆成 30 个 `turn_*.py`**：hermes 的细粒度模块是为多人协作 + 灰度发布设计的；Mizar 单 loop.go 可读性更好。
2. **不要自研浏览器/TTS/ASR/computer_use**：这些是「要么用成熟 MCP，要么别做」的领域。hermes 18 个 computer_use 文件是资产也是负债。
3. **不要为消息平台做网关**：那是另一个产品（且 22 平台 = 22 份持续维护）。
4. **不要做 serverless 后端（modal/daytona）**：与「内网单文件」定位冲突。

**该学的**：会话搜索、todo 压缩重注入、凭据池的「失败冷却」思路、memory 字符预算、tool_search 的 BM25 打分。

---

## 附：Hermes 核心工具清单（59 个，供参考）

```
核心: web_search web_extract terminal process_manage read_file write_file patch
      search_files vision_analyze image_generate skills_list skill_view skill_manage
      todo_list memory session_search clarify execute_code delegate_task cronjob_manage
      text_to_speech
浏览器: browser_navigate/snapshot/click/type/scroll/back/press/get_images/vision/console/cdp/dialog
        browser_vault_list/unlock/fill/save_login/enter_code  browser_exec
连接器: ha_list_entities/get_state/list_services/call_service（HomeAssistant）
        feishu_doc_read / feishu_drive_*（飞书）
        yb_query_group_info/members / yb_send_dm / yb_*_sticker（元宝）
        x_search（X/Twitter）
看板: kanban_show/list/complete/block/request_review/request_changes/heartbeat/comment/create/link/unblock/attach*
桌面: computer_use / desktop_preview / desktop_project / annotate_preview / apply_layout
      focus_pane / read_window_below / close_terminal / read_terminal / gui_tour / show_tip
```
