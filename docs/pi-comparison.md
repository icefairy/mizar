# Pi Agent 对比分析与引入建议

> 分析对象：Pi Agent v0.84.1（`@earendil-works/pi-coding-agent`）+ pi-cache-guardian v1.0.4
> 对比基准：开阳 Mizar（Go 单二进制，7649 行，15 包）
> 日期：2026-08-13
> 方法：全量读 Pi dist/ 源码（201 文件）+ 官方 docs + CHANGELOG 0.78-0.84，逐项比对 Mizar 本地代码

---

## 一、Pi 功能全景（7 大类）

### A. 核心 Agent

| 功能 | 实现机制 |
|---|---|
| 主循环 | `agent-session.js` 门面层 + pi-ai 内核 `agent.prompt/continue/steer/followUp` 驱动 |
| 7 内置工具 | read/bash/edit/write/grep/find/ls，双层结构 `ToolDefinition(schema+execute+render) → wrapToolDefinition → AgentTool`，支持注入远程 operations（SSH/容器复用） |
| 工具调用批量 + terminate | 扩展 `tool_call` 事件可 terminate 整个批 |
| 分支树会话 | `/tree`、`/fork`、`/clone`，JSONL + 分支摘要 |
| 压缩 compaction | 阈值自动 + `/compact` 手动 + 分支摘要 + 自定义压缩提示 |
| 消息队列 | 多 prompt 排队 |
| 模型轮换 | Ctrl+P / `/scoped-models` |

### B. 扩展系统（Pi 最成熟）

- TS 扩展：异步工厂、生命周期、shutdown
- 事件系统：startup/resource/session/agent/model/**tool**/user bash/input 七类，tool 事件带 `terminate`
- ctx：ui/mode/cwd/sessionManager/modelRegistry/thinkingLevel/signal/isIdle/abort/compact/getSystemPrompt/getContextUsage
- Extension UI 协议：替换编辑器、widgets、状态行、footer、overlays
- 自定义 provider 注册、自定义 summarization

### C. 模型与 Provider

- models.json 自定义模型/提供商（OpenAI 兼容/Anthropic/Google/Bedrock/Mistral/custom headers）
- 内置 provider 大量（Anthropic/OpenAI/Google/Copilot/OpenRouter/Qwen Token Plan/Baseten/NVIDIA NIM/llama.cpp…）
- 认证：`/login /logout`、OAuth 刷新、`pi auth check/print-api-key/print-bearer-token`
- 思考级别：off/minimal/low/medium/high/xhigh/max + `thinkingBudgets`
- 采样参数：samplingParams + vLLM `thinking_token_budget`

### D. 模式

- 交互 TUI（编辑器、@ 文件、粘贴图片、!bash、Ctrl+G 外编、Mermaid/LaTeX、主题）
- `-p/--print`（stdin 合并）、`--mode json`（事件流）、`--mode rpc`（JSONL 协议）
- `--export HTML` + `/share` gist
- SDK（Node 嵌入）、单二进制（bun compile）、三平台

### E. 安全

- Project Trust：项目本地资源加载确认、saved decisions、`--approve/--no-approve`、`project_trust` 事件
- 无内置沙箱（Gondolin/Docker/OpenShell 容器化）
- pi 包管理：install/remove/update/list/config

### F. 技能/知识/资源

- Skills（SKILL.md、frontmatter 校验、`/skill:name`、按需 read 加载）
- Prompt templates（`$1 ${1:-default} ${@:N}`）
- Context files（AGENTS.md/CLAUDE.md/SYSTEM.md/APPEND_SYSTEM.md）
- Pi packages（扩展/技能/提示/主题打包）、主题系统

### G. 缓存/可观测

- footer 缓存统计：CH 命中率、R 读、W 写、token/cost 总计
- showCacheMissNotices、cache-stats.js
- pi-cache-guardian 生态（golden prompt 冻结/技能压缩/400 黑名单）

---

## 二、Mizar 已实现（13 项，代码确认）

1. 核心 Agent 循环：文本 JSON 协议、20 步上限、压缩检查、steer 纠正、abort/Reset
2. Hooks 系统：10 挂载点 + 三级安全分级（🔴Severe/🟡Medium/🟢Safe）
3. 斜杠命令：CommandRegistry + 插件 `command_*` + `/help`
4. 内置工具：bash/grep/find/read/write/edit/ls/skill_manage（8 个）
5. 插件系统：TS→goja+esbuild、`tool_*/command_*/rpc_*`、热重载、禁用工具（缓存友好）、宿主函数
6. 技能：SKILL.md、索引注入（缓存友好）、skill_manage 自动沉淀、统计+周报+禁用
7. Server 模式：`/v1/models`、`/v1/chat/completions`、`/rpc`、`/ws`、admin 端点、Bearer 认证
8. 会话 JSONL：Append/Load/List/Delete/续接（缺分支树）
9. MCP 客户端：stdio/HTTP transport（Pi 无）
10. 配置：wizard、AGENTS.md、config.json
11. Compactor：token 估算/切点/摘要 + 缓存友好摘要前缀
12. WS 客户端桥（Pi 无）
13. 系统提示词缓存冻结 + 技能索引注入（正文变化不破前缀）

---

## 三、Mizar 缺口清单（12 项，含引入分级）

### 🟢 值得引入（低代码高价值）

| # | 功能 | Pi 实现要点 | 引入方案 | 工作量 |
|---|---|---|---|---|
| 1 | **Prompt templates** | 参数 `$1 ${1:-default} ${@:N}`，`/templatename` 展开 | `~/.mizar/templates/*.md` + 参数展开引擎，复用 CommandRegistry | ~30 行 |
| 2 | **缓存命中率统计** | footer CH/R/W + cache-stats.js | `systemPromptCache` 加 hit/miss 计数，启动打印 + config 开关 | ~30 行 |
| 3 | **thinking level** | off/minimal/low/medium/high/xhigh/max | config `thinking` 扩展为枚举，透传 OpenAI 参数 | ~40 行 |
| 4 | **扩展事件系统升级** | 32+ 事件，tool_call 可 deny/rewrite/intercept/allow | Hooks 加 `OnToolCallDecision` 回调，Decision 枚举 | ~30 行 |
| 5 | **read 双限截断 + 续读提示** | 2000 行/50KB + `[Showing X-Y of N. Use offset=Z]` | read 工具加 maxLines/maxBytes + 续读指令 | ~15 行 |
| 6 | **bash 输出截断 + 落盘** | 50KB + 落盘 temp + 回传路径 | strings.Builder 改限长，超限写 temp | ~20 行 |
| 7 | **edit 模糊匹配** | NFKC + 智能引号/破折号/全角空格归一化 | 归一化后先精确后模糊匹配 | ~25 行 |

### 🟡 中等程度（值得考虑，需结合定位）

| # | 功能 | Pi 实现要点 | 引入评估 |
|---|---|---|---|
| 8 | **模型轮换/多 provider** | Ctrl+P + `/scoped-models` + models.json | Mizar 走配置内 provider 列表 + `/model` 命令，不做 UI 级 Ctrl+P |
| 9 | **分支树会话** | `/tree /fork /clone` 交互回溯 | 简化为 session 派生新 ID（`--session` 续接已支持） |
| 10 | **Project Trust** | `.pi` 资源加载确认 + trust.json | 简化为"首次加载项目级 config 时确认"，不做 trust.json 全套 |
| 11 | **工具终止（terminate）** | tool_call 事件可终止整批 | Hooks 加 terminate 返回，Agent 循环响应 |
| 12 | **同文件写串行队列** | file-mutation-queue 按 realpath 串行 | write/edit 加互斥锁，防并发竞态 |
| 13 | **export HTML** | `--export` + highlight.js | 归档场景，可后置 |
| 14 | **bash 进程树击杀** | killProcessTree 杀整组 | `SysProcAttr{Setpgid:true}` + kill -pid |
| 15 | **edit 行尾/BOM 保真** | BOM 剥离 + LF/CRLF 检测还原 | 写回时保留原始行尾 |
| 16 | **edit 多编辑倒序应用** | 从后往前应用防 offset 偏移 | 收集替换位置降序应用 |

### 🔴 暂不引入（定位/成本不匹配）

| # | 功能 | 原因 |
|---|---|---|
| 17 | **交互 TUI** | Mizar 是嵌入型 agent（Server/JSON-RPC/WS），轻量 readline 已满足"命令行直接干活"；Pi 的 50+ 组件 TUI 是另一套工程 |
| 18 | **容器化沙箱**（Gondolin） | 重工程，v0.3 规划 |
| 19 | **pi 包管理** | Mizar 走 Git（gitea/github），不走 npm 包分发 |
| 20 | **认证管理**（/login OAuth） | Mizar 经璇玑网关统一鉴权，不需要 OAuth 流 |
| 21 | **/share GitHub gist** | 分享场景，非核心 |
| 22 | **单二进制 bun 编译** | Mizar 已是 Go 原生单二进制 |
| 23 | **Prompt 模板 UI 选择器** | TUI 附属能力 |

---

## 四、代码级差异明细（逐工具对比）

### bash 工具

| 维度 | Pi | Mizar 现状 | 结论 |
|---|---|---|---|
| 超时 | 可选，无默认（模型自行 `timeout` 命令） | 已对齐 ✅（5e5fdb8） | 完成 |
| 进程击杀 | killProcessTree 杀整组 | `cmd.Process.Kill()` 只杀父 | 🟡 需修：Setpgid + kill -pid |
| 输出截断 | 50KB/2000 行 + 落盘回传 | 全量 strings.Builder | 🟢 需加 |
| 流式输出 | 100ms 节流推送 | 一次性 collect | 编程模式非必须 |

### read 工具

| 维度 | Pi | Mizar 现状 | 结论 |
|---|---|---|---|
| 截断 | 2000 行/50KB 双限 + 续读提示 | 无截断全量进内存 | 🟢 需加 |
| 图片 | MIME 检测转 image 附件 | 无 | 编程模式非必须 |
| macOS 容错 | NFD 规范化/弯引号 | 无 | 低优先 |

### edit 工具

| 维度 | Pi | Mizar 现状 | 结论 |
|---|---|---|---|
| 模糊匹配 | NFKC + 智能引号归一化 | 精确 strings.Count | 🟢 需加 |
| 多编辑顺序 | 从后往前倒序应用 | 顺序 Replace（offset 偏移 bug） | 🟡 需修 |
| 行尾/BOM | BOM 剥离 + LF/CRLF 还原 | 直接 ReadFile/WriteFile | 🟡 需加 |
| TUI 预览 | 执行前异步 diff 预览 | 无 | TUI 附属，跳过 |

### 其他

| 功能 | Pi | Mizar | 结论 |
|---|---|---|---|
| rg/fd 自动下载 | ensureTool 自动装 | Go 内置 regexp/Glob 自包含 | Mizar 更好，不引入 |
| 输出守卫 | stdout 接管+背压（护 TUI） | 无 | 无需引入 |
| 工具双层定义 | ToolDefinition+wrap+renderer | plugins.Tool 单结构 | Mizar 简化更好 |

---

## 五、引入优先级总表（按 P0-P3）

| 优先级 | 项 | 行量 | 价值 |
|---|---|---|---|
| 🔴 P0 | edit 多编辑倒序应用 | ~10 行 | 修隐蔽 offset 错配 bug |
| 🔴 P0 | bash 进程树击杀 | ~5 行 | 子进程孤儿化资源泄漏 |
| 🟡 P1 | read 双限截断 + 续读提示 | ~15 行 | 防大文件撑爆内存 |
| 🟡 P1 | bash 输出截断 + 落盘 | ~20 行 | 防超大输出撑爆内存 |
| 🟢 P2 | 扩展工具拦截（deny/rewrite/intercept/allow） | ~30 行 | Pi 生态最核心可扩展性 |
| 🟢 P2 | edit 模糊匹配 | ~25 行 | 减少 oldText 未匹配报错 |
| 🟢 P2 | prompt templates | ~30 行 | 复用提示省 token |
| 🟢 P2 | 缓存命中率统计 | ~30 行 | 可观测 |
| 🟢 P2 | thinking level 枚举 | ~40 行 | 对齐主流 agent 控制 |
| 🔵 P3 | 写操作串行队列 | ~30 行 | 并发写竞态（低频） |
| 🔵 P3 | edit 行尾/BOM 保真 | ~20 行 | 跨平台兼容 |
| 🔵 P3 | 工具 terminate | ~20 行 | 可控性增强 |
| 🔵 P3 | 模型轮换/多 provider | 中 | 多模型切换 |
| 🔵 P3 | export HTML | 中 | 归档 |
| 🔵 P3 | Project Trust 简化版 | 中 | 安全 |
| 🔵 P3 | 分支会话派生 | 中 | 回溯 |

---

## 六、已优于 Pi（保留不引入）

- **技能索引注入**：Pi 用 pi-cache-guardian 插件实现，Mizar 原生（SkillInjectMode: index）
- **系统提示词强冻结**：Pi 每轮 re-capture golden，Mizar 首构建后永不重建直到 ReloadTools
- **禁用走工具层**：Mizar 禁用只摘工具注册表，提示词零变化
- **MCP 客户端 + WS 客户端桥**：Pi 无
- **Server admin 端点**（/admin/switch）：Pi 无
- **bash 默认超时策略**（对齐 Pi 后取消）✅ 2026-08-13 完成
- **技能统计+周报+自动沉淀**：Pi 无 skill_manage 生命周期管理

---

## 七、TUI 决策（2026-08-13 用户确认）

**不引入 Pi 重型 TUI**。理由：

1. Mizar 定位是**嵌入型 agent**（Server 模式 + JSON-RPC + WS + MCP），不是交互式终端工具
2. 现有 `interactive.go` 的 **bufio readline 模式已满足"命令行一敲直接干活"**——输入任务 → 斜杠命令分发 → Agent.Run → 输出回复
3. Pi 的 TUI（50+ 组件、Mermaid/LaTeX 渲染、主题系统、编辑器替换协议）是另一个工程量级的完整终端 UI，与 Mizar 核心价值（缓存保护/MCP/WS/技能沉淀）不匹配

**readline 模式的增强方向**（可选，低成本）：
- 历史命令上下键（github.com/chzyer/readline）
- `/model` 切换命令（配合多 provider）
- 启动 banner 显示当前模型/缓存状态
