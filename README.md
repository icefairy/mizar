# Mizar (开阳)

> 北斗第六星 · 双星系统 —— 主星与辅星，互为陪伴，缺一不可

**开阳 (Mizar)** is a minimal, zero-dependency, single-file, self-bootstrapping AI Agent framework.

- **Core**: A single static Go binary (~20-30MB, no runtime deps, no npm, no Python, no Docker)
- **Plugins**: TypeScript / JavaScript files — embedded esbuild compiles TS, JS runs directly on goja (no compile step), hot-reload
- **Skills**: Plain-text SKILL.md files (compatible with Anthropic/Hermes third-party skill ecosystem)
- **Tools**: MCP protocol — connect any MCP server implemented in any language
- **Self-bootstrapping**: The agent writes its own plugins and extends itself

**Where it shines**: air-gapped / offline / private deployment — copy one file onto the box and it runs. Zero dependencies, zero npm, zero internet.

```
Install = download one file
Plugin  = one .ts/.js file
Skill   = one .md file
Tool    = one MCP server
```

**中文简介**：**开阳 (Mizar)** 是一个极简、零依赖、单文件、可自举的 AI Agent 框架。单文件二进制（~20-30MB，无运行时依赖）；插件用 TS/JS 文件（TS 走 esbuild 内嵌编译，JS 免编译直跑 goja，热重载）；技能用 SKILL.md 纯文本（兼容第三方技能生态）；工具走 MCP 协议。适合内网/离线/私有化部署——一个文件拷进去就能跑，零依赖、零 npm、零外网。

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

## 快速开始（目标态）

```bash
# 首次运行：交互式初始化向导（配置供应商/模型/思考模式/上下文窗口）
./mizar --init
#   /provider       添加供应商（OpenAI 兼容端点，自动列出模型）
#   /model          选择模型（自动探测 / 手动输入序号）
#   /think          思考模式开关（空=自动探测是否支持）
#   /context auto   上下文窗口（auto=自动探测，或手动输入数字）
#   /save           保存到 ~/.mizar/config.json
# 之后启动自动加载 ~/.mizar/config.json（命令行 flag 优先）

# 内网服务器，无外网、无 Node、无 Python
curl -O http://192.168.1.10:8080/mizar-linux-amd64   # 或 scp 一个文件
chmod +x mizar
./mizar --model http://127.0.0.1:3002/v1   # 对接本地 LLM 网关（如璇玑）
```

```bash
# Agent 自举：让它写一个插件
./mizar "写一个插件：每天 9 点检查磁盘占用，超过 80% 发飞书告警"

# 插件已生成并热加载
ls extensions/
#   disk_watch.ts
```

### Server 模式（持久运行 + 三种接入协议）

```bash
# 持久运行 daemon：OpenAI 兼容 HTTP + JSON-RPC + WebSocket
./mizar --serve --addr :3003 --token your-token \
  --base-url http://127.0.0.1:3002/v1 --model deepseek-v4-flash
```

| 接入方式 | 端点 | 用法 |
|---|---|---|
| OpenAI 兼容 | `POST /v1/chat/completions` | 任意 OpenAI SDK 改 `base_url` 即可 |
| JSON-RPC 2.0 | `POST /rpc` | `agent.run` / `agent.steer` / `agent.abort` / `tools.list` / `system.ping` |
| WebSocket | `GET /ws` | 实时事件流 + 中途快速纠正（steer） |

```bash
# 动态开关（运行期即时生效，无需重启）
curl -X POST localhost:3003/admin/switch -d '{"service":"ws","enabled":false}' -H "Authorization: Bearer your-token"
curl localhost:3003/admin/status -H "Authorization: Bearer your-token"
```

```python
# WebSocket 快速纠正示例：看到输出不对立刻纠正（类似 pi 的 steer）
import websocket, json
ws = websocket.create_connection("ws://localhost:3003/ws", header=["Authorization: Bearer your-token"])
ws.send(json.dumps({"jsonrpc": "2.0", "id": "r1", "method": "agent.run", "params": {"task": "..."}}))
# 收到事件流，发现不对 →
ws.send(json.dumps({"jsonrpc": "2.0", "id": "s1", "method": "agent.steer", "params": {"message": "方向错了，改成..."}}))
# ack 秒回，纠正注入下一轮循环
```

---

## 项目结构（目标态）

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
│   └── shell/              # 安全命令执行
├── extensions/             # 插件目录（Agent 自写的 .ts 文件）
├── skills/                 # 技能目录（SKILL.md）
└── go.mod
```

详细设计见 [docs/architecture.md](docs/architecture.md)。
