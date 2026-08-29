# Mizar (开阳)

> 北斗第六星 · 双星系统 —— 主星与辅星，互为陪伴，缺一不可

[![License](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](LICENSE)

**开阳 (Mizar)** 是一个极简、零依赖、单文件、可自举的 AI Agent 框架。

- **核心**：单个静态 Go 二进制（~20-30MB，无运行时依赖，无需 npm / Python / Docker）
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

## 核心特性

### 1. 交互式 TUI
基于 tview 构建的终端界面，支持：
- 鼠标滚轮滚动 + 点击不窃取焦点
- 状态栏显示模型名、思考等级等信息
- 输入框多行编辑 + Tab 自动补全
- 执行中消息排队，Alt+↑ 取回，PgUp/PgDn 滚动历史
- 流式输出 + 等待进度指示（LLM SSE → Agent 流式回调 → TUI/Server 增量渲染）

### 2. 内置工具（11+ 个）
基础工具 `bash` / `grep` / `find` / `read` / `write` / `edit` / `ls` / `skill_manage`，+ dsh 复刻新增：
- **`todo_write`**：结构化任务清单（pending / in_progress / completed），每次调用替换整个清单，单次 in_progress 约束防弱模型混乱
- **`ask_user_question`**：向用户提问并等待回答（TUI 模式弹出输入框；Server/CLI 模式降级为提示自主决策）
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

运行期动态开关服务：
```bash
curl -X POST localhost:3003/admin/switch -d '{"service":"ws","enabled":false}' \
  -H "Authorization: Bearer your-token"
```

### 7. 会话记忆（Path-Scoped）
- 按工作目录自动恢复/创建会话（类似 Pi 的 path-scoped session memory）
- JSONL 格式会话日志：追加 / 加载 / 列表 / 删除 / 续接
- AbortReason 归一化与分类（8 种原因）

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
复刻 deepseek-harness 的 goal 工具：`get_goal`/`create_goal`/`update_goal`（edit/pause/resume/complete/blocked）。create/edit/pause/resume 需人类直接消息权限；complete/blocked 可由模型自动报告（blocked 需满足连续阈值）。适用于长期多步骤任务的目标管理。

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
  # /config 查看当前配置

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
│   ├── agent/              # Agent 循环（任务规划/工具调用/错误修复）
│   ├── engine/             # goja 内嵌 JS 引擎 + esbuild 编译管线
│   ├── plugins/            # 插件加载器（扫描 .ts → 编译 → 注册工具）
│   ├── skills/             # SKILL.md 加载器（解析 frontmatter → 注入上下文）
│   ├── mcp/                # MCP 客户端（stdio/HTTP）
│   ├── llm/                # LLM 客户端（OpenAI 兼容协议）
│   ├── shell/              # 安全命令执行
│   ├── lsp/                # LSP 客户端 + 服务器
│   ├── server/             # HTTP/RPC/WebSocket 服务器
│   ├── session/            # 会话管理（JSONL 持久化）
│   └── lifecycle/          # 生命周期钩子管理
├── extensions/             # 插件目录（Agent 自写的 .ts 文件）
├── skills/                 # 技能目录（SKILL.md）
├── scenarios/              # 验证场景（开发 70 / 运维 30）
├── docs/                   # 设计文档
└── go.mod
```

详细设计见 [docs/architecture.md](docs/architecture.md)。

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
| todo_write | — | 结构化任务清单 |
| 计划模式 | /plan + exit_plan_mode | /plan + exit_plan_mode（TUI 审批） |
| 会话目标 | create/get/update_goal | get_goal/create_goal/update_goal（权限约束） |
| 定时提醒 | schedule_create/list/delete | schedule_create/list/delete（after/at/every） |
| 验证框架 | — | 100 场景验证 |

完整对比分析见 [docs/pi-comparison.md](docs/pi-comparison.md)。
