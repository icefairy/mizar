# Changelog

所有重要变更记录在此。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)。

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
- **ask_user_question 工具**：TUI 模式下阻塞等待用户回答（5 分钟超时），非 TUI 模式优雅降级提示"请自行完成任务"。
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
