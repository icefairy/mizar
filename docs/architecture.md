# Mizar (开阳) 架构设计文档

> 版本：v0.1 草案 · 2026-08-12
> 作者：玄凌子

## 1. 背景与动机

### 1.1 灵感来源

[Pi Agent](https://github.com/mariozechner/pi) 展示了极简 AI Agent 的优雅：单目录、技能 + 扩展、Agent 自己写扩展的自举循环。但 Pi 构建在 Node.js 之上——`npm install`、node_modules、几百 MB 依赖，这在内网离线环境是不可接受的。

### 1.2 核心约束（按优先级排序）

| # | 约束 | 来源 |
|---|---|---|
| 1 | **零运行时依赖** | 内网部署：无 Node、无 Python、无 npm registry |
| 2 | **单文件分发** | 安装 = 拷一个二进制 |
| 3 | **可自举** | Agent 自己写插件扩展自己，不依赖外部生态 |
| 4 | **性能好** | 优于解释型运行时 |
| 5 | **离线可用** | 无外网环境下完整工作 |

### 1.3 生态位判断

当前 Agent 生态（2026）：

| Agent | 核心 | 插件机制 | 依赖 | 离线部署 |
|---|---|---|---|---|
| Pi | Node.js | TS 扩展 | npm + node_modules | ❌ |
| pi_agent_rust | Rust | 内嵌 JS 跑 TS 扩展 | 需兼容 pi 生态（npm） | ⚠️ |
| Claude Code | Node.js | SKILL.md + MCP | npm | ❌ |
| Codex | Rust | AGENTS.md + MCP | 无运行时，但生态分发靠网络 | ⚠️ |
| Goose | Rust | MCP 扩展 | 70+ 扩展靠网络分发 | ⚠️ |
| Crush | Go | MCP | 无 | ⚠️ |
| **Mizar** | **Go** | **TS 插件 + SKILL.md + MCP** | **零** | **✅** |

**结论**：所有主流 Agent 都假设"有网络"（装依赖、拉模型、分发扩展）。内网/离线/私有化部署是真空地带——而大企业（银行、军工、能源、政务）恰恰是离线环境 + 高预算 + 强合规需求。**Mizar 的生态位：唯一能过内网安全审查、零依赖离线自举的 Agent。**

## 2. 总体架构

```
┌─────────────────────────────────────────────────────┐
│                   Mizar (单个 Go 二进制)              │
│                                                     │
│  ┌──────────┐   ┌──────────┐   ┌──────────────┐    │
│  │  Skills  │   │ Plugins  │   │ MCP Client   │    │
│  │ SKILL.md │   │ .ts/.js  │   │ (stdio/HTTP) │    │
│  │ 加载器    │   │ 加载器    │   │              │    │
│  └────┬─────┘   └────┬─────┘   └──────┬───────┘    │
│       │              │                │            │
│  ┌────┴──────────────┴────────────────┴───────┐     │
│  │               Agent 核心循环                │     │
│  │  规划 → 工具调用 → 错误修复 → 自举           │     │
│  └────┬───────────────────────────────┬───────┘     │
│       │                               │            │
│  ┌────┴─────┐                  ┌──────┴───────┐     │
│  │ LLM Client│                  │  goja 引擎    │     │
│  │(OpenAI兼容)│                  │ + esbuild    │     │
│  └────┬─────┘                  │ (TS→JS 编译)  │     │
│       │                        │ .js 免编译直跑 │     │
│       │                        └──────┬───────┘     │
└───────┼───────────────────────────────┼─────────────┘
        │                               │
   ┌────┴─────┐                 ┌───────┴────────┐
   │ 璇玑网关   │                 │  宿主函数注册    │
   │(LLM 汇聚/  │                 │ http/json/fs/   │
   │ 路由/缓存) │                 │ shell/llm_chat  │
   └──────────┘                 └────────────────┘
```

### 2.1 三层扩展模型

Mizar 的能力扩展分三层，各司其职：

| 层 | 载体 | 内容 | 自举方式 |
|---|---|---|---|
| **技能层** | SKILL.md（Markdown 文本） | 指导 Agent 怎么做的知识/流程 | Agent 写文档即可 |
| **插件层** | .ts / .js 文件 | 可执行的新工具/能力 | Agent 写代码，.ts 走 esbuild 编译、.js 免编译直跑 goja，热加载 |
| **工具层** | MCP 服务器 | 任意语言实现的协议服务 | 启动进程/连接端点即接入 |

三层互不阻塞：技能不需要代码，插件不需要外网，MCP 不需要改二进制。

## 3. 关键技术选型

### 3.1 Go 核心

- 静态编译单文件、跨平台（linux-amd64/arm64/windows-amd64）、goroutine 并发
- LLM 写 Go 成功率高于 Rust（无借用检查器），核心代码自举友好
- 无 GC 停顿、内存占用低、启动毫秒级

### 3.2 goja —— 纯 Go JS 引擎

- `github.com/dop251/goja`，纯 Go 实现 ECMAScript 5.1 + 大部分 ES6
- **零 CGO、零外部依赖**，交叉编译全平台无障碍
- 插件场景大部分时间是 Go↔JS 桥接调用，纯 Go 桥接反而快于 CGO（v8go 的 CGO 调用开销大）
- 对比：v8go（性能碾压但 CGO 毁掉单文件）、quickjs-go（CGO）、ModerncQuickJS（纯 Go 但 API 不如 goja 成熟）

### 3.3 esbuild —— Go 库内嵌 TS 编译器

- `github.com/evanw/esbuild/pkg/api`，**esbuild 本身就是 Go 写的**
- 静态编译进二进制，TS→JS 转译毫秒级（类型擦除，不做类型检查——正合自举：立即能跑，跑错再修）
- 2026 年微软已用 Go 重写 TypeScript 编译器（tsgo，快 10 倍），TS 工具链已完全 Go 化，未来可接入 tsgo 做嵌入式类型检查

### 3.4 为什么插件用 TS 而不是 Lua

| 维度 | Lua | TS |
|---|---|---|
| LLM 生成成功率 | 高 | **最高**（训练语料全球第一） |
| 类型标注 | 无 | 有（约束 LLM 少幻觉） |
| 生态兼容 | 无 | 与 pi 生态格式相同 |
| 依赖 | 零 | 零（esbuild 内嵌） |
| 编译 | 无 | 一次转译（毫秒级） |

**结论**：Lua 语法更简单但 LLM 写 TS 成功率更高、可维护性更好，且 TS 插件格式天然对齐 pi 生态。代价只是一次 esbuild 转译——它已经编在二进制里。

### 3.5 宿主函数（插件能用的 Go 能力）

```go
// 注册给 JS 插件的全局函数（一次性写好，之后插件随便组合）
vm.Set("http_request", httpRequest) // 统一 HTTP：method 任意 GET/POST/PUT/DELETE/PATCH，headers JSON 可选
vm.Set("http_get", httpGet)         // 薄封装：http_request("GET", url, "", "")，保留兼容
vm.Set("http_post", httpPost)       // 薄封装：http_request("POST", url, body, "")
vm.Set("json_decode", jsonDecode) // JSON 解析
vm.Set("json_encode", jsonEncode)
vm.Set("fs_read", fsRead)         // 文件读取（10MB 上限，超限拒绝）
vm.Set("fs_read_range", fsReadRange) // 有界 seek 读：fs_read_range(path, offset, length) → [content, totalSize, error]，单次 4MB，大文件循环分片
vm.Set("fs_write", fsWrite)
vm.Set("fs_list", fsList)
vm.Set("shell_exec", shellExec)   // 执行命令（白名单 + 超时 + 输出上限）
vm.Set("llm_chat", llmChat)       // 调 LLM —— 自举闭环的钥匙
vm.Set("log", log)                // 日志
vm.Set("sleep", sleep)
vm.Set("ws_emit", wsEmit)         // 向所有 WS 客户端推送自定义事件（Server 模式）
vm.Set("ws_connect", ...)         // WS 客户端桥：连外部 WS 服务（如飞书长连接）
vm.Set("ws_send", ...)
vm.Set("ws_onmessage", ...)
vm.Set("ws_close", ...)
vm.Set("db_query", dbQuery)       // 内置数据库查询：db_query(driver, dsn, sql) → JSON（v1.2+）
vm.Set("mcp_call", mcpCall)       // 外部 MCP server：mcp_call(server, tool, argsJSON) → 文本（v1.3+）
vm.Set("hook_on", hookOn)         // 生命周期挂载点：hook_on(event, cb(ctxJSON)) （v1.3+）
// —— 纯函数标准库（v1.3+，无 I/O 零副作用，引擎创建时无条件注册）——
vm.Set("time_now", ...)           // 当前 UTC 时间 RFC3339Nano；time_unix() → 秒
vm.Set("uuid", ...)               // UUID v4（crypto/rand）
vm.Set("base64_encode", ...)      // base64_decode(s)
vm.Set("hash_sha256", ...)        // SHA-256 hex
vm.Set("path_join", ...)          // path_base(p) / path_dir(p)，POSIX 语义
vm.Set("url_parse", ...)          // URL → JSON {scheme,host,path,query,fragment,user}
vm.Set("count_tokens", ...)       // 估算 token 数（CJK 按字符、其他按 4 字符/token，与压缩器同口径）
```

**HTTP 统一化**（v0.2.4+）：早期只有 `http_get`/`http_post` 两个固定方法，无法覆盖 PUT/DELETE/PATCH 等场景，且 `main.go` 里 host 实际未实现这两个函数（架构文档画饼）。现统一为 `http_request(method, url, body, headersJSON)`：

```ts
// headersJSON 为 JSON 字符串，可选（空字符串 = 不带自定义头）
const r1 = http_request("GET", "https://api.example.com/status", "", "");
const r2 = http_request("POST", "https://api.example.com/data", JSON.stringify({a: 1}), JSON.stringify({"Content-Type": "application/json"}));
const r3 = http_request("PUT", "https://api.example.com/items/1", "body...", "");
const r4 = http_request("DELETE", "https://api.example.com/items/1", "", "");
```

`http_get`/`http_post` 保留为薄封装（兼容旧插件），新代码一律用 `http_request`。

```ts
// extensions/disk_watch.ts —— Agent 自写插件示例
export function tool_disk_watch(): string {
  const out = shell_exec("df -h / | tail -1");
  const pct = parseInt(out.split(/\s+/)[4]);  // "80%"
  if (pct > 80) {
    http_request("POST", "http://notify.internal/feishu", json_encode({ msg: "磁盘占用 " + pct + "%" }), "");
    return "已告警";
  }
  return "正常 (" + pct + "%)";
}
```

### 3.6 db_query——内置数据库查询（v1.2+）

插件连接常见数据库用内置驱动，**不需要**额外进程或 MCP server。驱动白名单：`sqlite3`（纯 Go 免 CGO）/ `mysql` / `postgres`。

```
db_query(driver, dsn, sql) → JSON 字符串
```

- **查询语句**（SELECT/SHOW/PRAGMA 等）返回 JSON 行数组：`[{"id":1,"name":"zhang"},...]`
- **非查询语句**（INSERT/UPDATE/DELETE/DDL）返回 `{"rowsAffected":N}`
- 内置 30s 超时；DSN 长度限制；驱动白名单外直接报错
- sqlite3 的 DSN 就是文件路径；mysql/postgres 用标准 DSN 格式

```ts
// 常见关系型——内置，秒连
export function tool_users(): string {
  return db_query("sqlite3", "/data/app.db", "SELECT id, name FROM users LIMIT 10");
}

// 参数化注意：db_query 只接字符串，SQL 拼接时插件自行校验/转义输入
export function tool_insert(name: string): string {
  const safe = name.replace(/'/g, "''");   // SQL 注入防护（至少）
  return db_query("sqlite3", "/data/app.db", "INSERT INTO users (name) VALUES ('" + safe + "')");
}
```

**边界**：`db_query` 只覆盖内置驱动的库。连冷门数据库（MongoDB/Redis/ClickHouse 等）时走 MCP server（见 3.7），外部 server 自带驱动，不进主体二进制。

### 3.6.1 连接池 + 批量 + 显式关闭（v1.3+）

**连接池**：db 连接按 `driver|dsn` 缓存复用（同一 DSN 共享同一连接）。文件库空闲 5min 自动 Close，`:memory:` 永久保留（跨调用数据可见）。性能对比（SQLite 500 行基准）：

| 操作 | 每次重开连接 | 连接池复用 |
|---|---|---|
| 逐条插入 | 193ms | 46ms（4.2x）|
| 100 次点查 | 31ms | 9ms（3.4x）|

**会话语义**：同一 DSN 复用连接 → 会话状态（`PRAGMA`/`SET SESSION`/临时表/用户变量）跨调用保留。插件需要干净会话时用 `db_close` 显式重置。

```ts
db_query("sqlite3", "/data/app.db", "PRAGMA foreign_keys=OFF");   // 影响后续同 DSN 调用
db_close("sqlite3", "/data/app.db");                               // 丢弃残留，下次重建连接
```

**db_exec_batch**：SQL 数组一个事务执行，失败整体回滚。

```ts
db_exec_batch("sqlite3", "/data/app.db", JSON.stringify([
  "CREATE TABLE IF NOT EXISTS mem (id INTEGER PRIMARY KEY, task TEXT)",
  "INSERT INTO mem (task) VALUES ('a'); INSERT INTO mem (task) VALUES ('b');"
]));
// → {"rowsAffected":2,"statements":3}
```

性能：批量 500 行 3ms（事务 + 连接复用），逐条 46ms（连接复用无事务）。**写多行优先 batch**。

### 3.7 mcp_call——冷门能力的外部通道（v1.3+）

插件需要 Redis/Kafka/MongoDB/ClickHouse 等长尾能力时，不内置驱动，走 `mcp_call` 调外部 MCP server：

- `mcp_call(server, tool, argsJSON)` → 文本
- 配置驱动：`config.json` 加 `mcp_servers`（支持 stdio + HTTP 传输）
- 惰性连接 + 复用 + 30s 超时；调用失败返回错误，不阻塞后续

**分层总览**：`db_query`（内置关系型）→ `mcp_call`（外部任意服务），插件按场景选，互不阻塞。内置 3 驱动保持主体轻量，长尾能力全部外置，二进制永不膨胀。

### 3.8 插件生命周期（v1.3+）

插件可导出两个可选钩子，做初始化和反初始化：

| 钩子 | 触发时机 | 用途 |
|---|---|---|
| `plugin_init()` | 加载成功后（含首次加载、热重载新版本） | 建表、注册 hook、初始化状态 |
| `plugin_cleanup()` | 卸载前（热重载替换、插件文件被删除） | 释放连接、关闭端口、反注册 |

示例：
```ts
export function plugin_init(): void {
  db_exec_batch("sqlite3", "/data/app.db", "[...]");  // 建表
  hook_on("RunEnd", (ctx) => {...});                  // 注册 hook
}
export function plugin_cleanup(): void {
  db_close("sqlite3", "/data/app.db");                // 释放连接
}
```

设计要点：
- **回调在锁外执行**：init/cleanup 内可自由调宿主函数（db_query 等），不会死锁
- **热重载原子性**：新版本 `plugin_init` 成功才生效；失败保留旧版本
- **删除自动卸载**：插件文件消失 → `plugin_cleanup` + 注销全部工具/命令/hook/HTTP server（无幽灵插件）
- **无钩子兼容**：插件不导出这两个函数完全不受影响（Has 探测跳过）

### 3.9 网络能力（v1.3+）

插件可直接开网络服务/连外部服务，做分布式互联：

| 能力 | 函数 | 说明 |
|---|---|---|
| TCP 服务器 | `tcp_listen` / `tcp_stop` | onAccept 回调接入连接，返回 connID |
| TCP 客户端 | `tcp_dial` / `tcp_send` / `tcp_onrecv` / `tcp_close` | 双向文本通信，协议自定 |
| HTTP 服务器 | `host_listen` | 已存在（Manager 接管热重载关闭） |
| FTP 客户端 | `ftp_connect` / `ftp_list` / `ftp_upload` / `ftp_download` / `ftp_mkdir` / `ftp_rmdir` / `ftp_delete` / `ftp_rename` / `ftp_close` | **仅客户端**，不暴露服务器 |
| WS 客户端 | `ws_connect` / `ws_send` / `ws_onmessage` / `ws_close` | 连外部长连接服务（飞书等） |

设计原则：
- **回调驱动**：`tcp_onrecv`/`tcp_onmessage` 注册 JS 回调，数据到达自动触发，不阻塞 Agent 主循环
- **零状态泄漏**：TCP server stop / 插件热重载时全部连接自动关闭
- **命名统一**：`tcp_*`/`ftp_*`/`ws_*` 前缀，与 `host_listen` 并列

### 3.10 稳定性加固（v1.3+）

| 防线 | 机制 | 效果 |
|---|---|---|
| 加载失败隔离 | loadPlugin 逐个加载，失败进 failed map | 坏插件不影响其他插件与主程序 |
| 热重载原子性 | 新引擎成功才替换，失败保留旧 | 改坏不丢 |
| 删除自动卸载 | loadAll 对比目录发现文件消失 → plugin_cleanup + 注销 | 无幽灵插件 |
| **panic 隔离** | Call / RunScript / FireHook 全部 defer recover | 宿主函数 panic 不拖垮主程序 |
| **执行超时** | Call / RunScript 默认 30s，goja Interrupt 打断 | 死循环插件被中断，引擎可继续用 |

## 4. 自举闭环（核心卖点）
```
用户需求
   │
   ▼
Agent 规划：需要新能力吗？
   │                          ┌──────────────┐
   ├── 已有工具可用 ──────────►│ 直接执行任务   │
   │                          └──────────────┘
   └── 缺工具 ──► 写插件（.ts 文件）
                     │
                     ▼
               esbuild 编译（内嵌，毫秒级）
                     │
                     ▼
               goja 热加载 → 新工具注册成功
                     │
                     ▼
               继续执行原任务（用新工具）
                     │
                     ▼
           【可选项】把插件沉淀进 extensions/ 目录
           （下次会话直接可用，技能也可写 SKILL.md）
```

- 自举**不依赖网络**：编译链在二进制里，模型在本地网关
- 自举**不需要重启**：文件变化 → 重新编译 → 新工具生效
- 弱模型宽容：工具调用失败 → 自动修复循环（错误信息回喂 → 改代码 → 重试）

## 5. 内网/离线部署场景

### 5.1 部署矩阵

| 环节 | 需要外网吗 | Mizar 方案 |
|---|---|---|
| 安装 | ❌ | 拷一个二进制，`chmod +x` 即用 |
| 运行时 | ❌ | 静态编译，无动态链接 |
| 插件依赖 | ❌ | esbuild 内嵌，无 npm |
| 技能 | ❌ | SKILL.md 纯文本，走内部 Git/共享盘 |
| 工具 | ❌ | MCP 服务器本地进程/内网 HTTP |
| LLM | ❌ | 本地 vLLM/Ollama + 璇玑网关汇聚 |
| 安全审计 | ❌ | 依赖面=0，攻击面=0，无隐式外联 |

### 5.2 与璇玑网关的配合

```
内网服务器
  ├── Mizar Agent（单二进制）
  │     └── llm_chat ──► 璇玑网关 (127.0.0.1:3002)
  │                          ├── vLLM (本地模型)
  │                          ├── Ollama (本地小模型)
  │                          └── 路由/缓存/配额
  └── 插件 + 技能 + MCP（全是本地文件）
```

- Mizar 只认 OpenAI 兼容协议，璇玑网关正好提供
- 缓存命中 → 降成本；路由规则 → 大模型复杂任务、小模型简单任务
- 弱模型场景：Mizar 的错误修复循环保证便宜模型也能干活

## 6. 与 pi_agent_rust 的对比

| 维度 | pi_agent_rust | Mizar |
|---|---|---|
| 核心 | Rust | Go |
| 插件 | 内嵌 JS 跑 pi 的 TS 扩展 | goja 跑 esbuild 编译的 TS |
| 生态 | ✅ 兼容 pi 全部扩展 | ❌ 自有生态，但兼容 SKILL.md + MCP |
| 插件依赖 | ⚠️ 要跑 pi 生态需处理 npm | ✅ 零依赖单文件 |
| 开发速度 | Rust 借用检查器 | Go 简单，LLM 自举核心容易 |
| 复杂度 | 高（extensions 子系统 86.5k 行） | 低 |
| 定位 | 承接 pi 生态的 Rust 用户 | 内网离线零依赖极简自举 |

**本质差异**：pi_agent_rust 选择"兼容 pi"，Mizar 选择"零依赖"。它背上了 pi 的 npm 包袱换生态；Mizar 放弃 pi 扩展兼容换架构极简——而 SKILL.md（Anthropic 标准）和 MCP（协议标准）两大生态都是语言无关的，Mizar 零成本接入，这才是真正的护城河。

## 7. 安全设计（大企业合规）

1. **零依赖 = 零供应链风险**：无 npm 几万个小包的投毒面
2. **文件访问沙箱**：fs_* 限定在项目目录，越界报错
3. **shell 白名单**：shell_exec 只允许注册过的命令（df/ls/cat/git...），拒绝 rm -rf 类危险模式
4. **超时与限额**：插件执行超时、输出上限、内存上限
5. **无隐式外联**：二进制不主动联网；HTTP 函数默认内网代理，出网需显式配置
6. **技能/插件审核**：SKILL.md 是文本可走审批流，插件代码可走代码评审

## 8. 路线图

### v0.1 —— 验证自举闭环（已完成）
- [x] Go 骨架 + goja 引擎 + esbuild 编译管线
- [x] 宿主函数集（http/json/fs/shell/llm_chat）
- [x] 插件加载器：扫描 extensions/ → 编译 → 注册
- [x] Agent 循环：规划 → 工具调用 → 错误修复
- [x] 手写插件 + Agent 写插件双路径验证（17*23=391 端到端通过）

### v0.2 —— 三层扩展 + 缓存友好循环（已完成 v0.2.4）
- [x] SKILL.md 加载器（解析 frontmatter → 注入 system prompt）
- [x] MCP 客户端（stdio + HTTP）
- [x] 会话管理（JSONL）
- [x] 自举沉淀：SkillWriter 自动写 SKILL.md
- [x] 会话压缩器 Compactor（pi 参考：触发/切点/结构化摘要）
- [x] Hook 挂载点系统（11 挂载点 + 缓存影响分级）
- [x] 缓存友好：system 前缀稳定 + 工具列表稳定 + 摘要隔离
- [x] 初始化向导（--init + ~/.mizar/config.json + 自动探测）
- [x] 斜杠命令注册表（内置 /reload /reset /quit + 插件 command_*）
- [x] Server 模式：OpenAI 兼容 HTTP + JSON-RPC 2.0 + WebSocket + 动态开关
- [x] 快速插入（steer/abort）——WS 实时纠正
- [x] 插件 WS 能力（rpc_* 自定义方法 + ws_emit 推送 + WS 客户端桥）
- [x] 内置工具集（bash/grep/find/read/write/edit/ls，pi 对齐）
- [x] AGENTS.md 自动读取（全局 + 局部相加）
- [ ] 自举沉淀端到端验证

### v0.3 —— 内网产品化
- [ ] 弱模型宽容循环调优（错误修复、逐步降级）
- [ ] 安全沙箱加固（限额、白名单、审计日志）
- [ ] 与璇玑网关深度集成（缓存命中感知、成本控制）
- [ ] 三平台交叉编译 + 发布（GitHub + Gitee）

## 9. Hook 挂载点与缓存影响

### 9.1 设计原则

1. **全环节可挂载**：Agent 循环每个关键环节都有挂载点，从 Run 开始到结束。
2. **列表多挂载**：每个挂载点是 `[]HookFunc`，多个插件可同时挂载同一挂载点，按注册顺序执行；单个挂载函数返回 error 不中断链（观察者模式，错误仅记录）。
3. **缓存影响分级**：每个挂载点标注 CacheImpact——这是本框架最重要的约束之一。
   - 🔴 **严重**：在 LLM 请求前触发且能修改消息列表 → 任何改动都会让整个 prefix cache 失效，命中率暴跌
   - 🟡 **中等**：在工具结果/回复之后触发，可能影响后续消息 → 影响有限
   - 🟢 **安全**：只读/事后观察，不影响发给 LLM 的消息序列

### 9.2 挂载点总表

| 挂载点 | 时机 | 缓存影响 | 可修改 | 说明 |
|---|---|---|---|---|
| RunStart | Run 开始、task 已就绪 | 🟢 安全 | - | 任务开始：计时、初始化状态 |
| RunEnd | Run 结束、reply 已生成 | 🟢 安全 | - | 任务结束：统计、上报 |
| StepStart | 每步循环开始 | 🟡 中等 | Messages(不建议) | 进度上报、外部熔断 |
| StepEnd | 每步循环结束 | 🟡 中等 | - | 步骤计数、状态持久化 |
| **LLMRequest** | **LLM 调用前、messages 即将发送** | **🔴 严重** | **Messages** | **⚠️ 注入动态上下文会破坏缓存，默认禁用** |
| LLMResponse | LLM 调用后、reply 已返回 | 🟢 安全 | - | 日志、流式转发 |
| ToolCall | 工具调用前 | 🟢 安全 | Tool/Args | 参数校验、权限检查 |
| ToolResult | 工具结果产生后 | 🟡 中等 | - | 结果过滤、错误上报 |
| CompactionBefore | 压缩执行前 | 🟢 安全 | - | 压缩预检查 |
| CompactionAfter | 压缩执行后 | 🟢 安全 | Messages(不建议) | 摘要后处理、更新外部记忆 |
| Error | 任何错误发生 | 🟢 安全 | - | 错误告警、降级策略 |

### 9.3 缓存警告（必须阅读）

- **LLMRequest 是唯一 🔴 挂载点**：它把动态内容注入 LLM 消息流。一旦注入的内容在多次请求间变化，系统前缀缓存（system + 工具列表 + 历史）全部失效。**不要在 LLMRequest 注入逐次变化的内容**（时间戳、随机数、计数器）。需要动态上下文时：
  1. 内容静态（如用户配置文件）→ 放 system（构建时注入一次）
  2. 内容逐次变化（如工作记忆）→ 放**用户消息末尾**（ProjectDiscovery 实测：动态工作记忆放 system 使缓存命中率跌到 7%；移到 user 末尾后恢复）
  3. 禁用所有带随机性的注入
- **插件热加载（ReloadTools）会破坏缓存一次**：工具列表是 system prompt 的一部分，新增工具改变 system 字节。这是显式 trade-off，仅在确实新增工具时调用，禁止轮询调用。
- **压缩（Compaction）设计为低频**：只在超限时触发，压缩后摘要作为稳定前缀插入 system 之后，重新建立可缓存前缀。摘要请求用独立请求（X-Mizar-Request: summarize），prompt 前缀与主对话不同，天然不污染主对话缓存。

### 9.4 插件挂载示例

```go
h := agent.NewHooks()
// 多个插件可同时挂载 RunStart
h.OnRunStart(pluginA.OnRunStart)  // 插件 A：计时
h.OnRunStart(pluginB.OnRunStart)  // 插件 B：审计日志
// 只读观察 LLM 响应
h.OnLLMResponse(func(ctx *agent.HookContext) error {
    log.Printf("step %d: %s", ctx.Step, truncate(ctx.Reply, 100))
    return nil
})
```

## 10. Server 模式与快速插入

### 10.1 三种接入协议

Mizar 支持 `-serve` 持久运行（daemon），对外提供三种接入方式，均可用 `--http/--rpc/--ws` 启动开关控制，运行期可用 `/admin/switch` 动态开关（即时生效，关后对应端点返回 503）：

| 协议 | 端点 | 用途 | 第三方示例 |
|---|---|---|---|
| OpenAI 兼容 HTTP | `POST /v1/chat/completions` `GET /v1/models` | 直接当 OpenAI API 用，零改造成本接入 | `openai` SDK 改 base_url |
| JSON-RPC 2.0 | `POST /rpc` | pi 式 RPC 命令集 | 脚本/CLI 调用 |
| WebSocket | `GET /ws` | 实时事件流 + 快速纠正（steer） | 交互式前端 |

### 10.2 JSON-RPC 方法集

| 方法 | 参数 | 说明 |
|---|---|---|
| `agent.run` | `{task}` | 执行任务（HTTP 同步；WS 异步，结果推回） |
| `agent.steer` | `{message}` | 快速插入纠正（不阻塞，立即注入当前循环） |
| `agent.abort` | - | 中止当前循环，返回 ErrAborted |
| `agent.running` | - | 查询运行状态 |
| `tools.list` | - | 列出可用工具 |
| `system.ping` | - | 健康检查 |

### 10.3 WebSocket 事件流（快速插入的可视化基础）

服务端 → 客户端实时推送（Agent 循环的 Hook 广播）：

```
{"type":"event","event":"llm_response","step":0,"content":"..."}
{"type":"event","event":"tool_call","step":0,"tool":"calc","args":"..."}
{"type":"event","event":"tool_result","step":0,"tool":"calc","result":"..."}
{"type":"ack","method":"agent.steer","id":"..."}   // 纠正已受理
{"jsonrpc":"2.0","id":"run1","result":{"reply":"..."}}  // run 完成
```

**典型交互**（用户看到中间输出不对立刻纠正）：
1. 客户端发 `agent.run`（异步执行，读循环不阻塞）
2. 收到 `tool_call` / `llm_response` 事件 → 发现与预期有差距
3. 立即发 `agent.steer {message}` → ack 秒回
4. 纠正消息注入下一轮 LLM 调用（带 `【用户快速纠正】` 前缀）
5. 后续事件体现纠正效果（实测：steer 后模型把第二个计算从 9*9 改为 3*4，并按指令直接收尾）

### 10.4 快速插入机制（steer/abort）设计

参照 pi 的 steer 命令，实现为 Agent 层的线程安全槽位：

- `Steer(msg)`：外部随时调用（HTTP/WS/JSON-RPC/同进程），**最新覆盖**（多条纠正只保留最新，用户最新意图优先）
- 循环在**下一次 LLM 调用前** drain 槽位，注入为 `Message{Role: RoleUser, Content: "【用户快速纠正】"+msg}`，优先级高于压缩
- `Abort()`：原子标志，循环在下一个检查点停止，返回 `ErrAborted`
- **缓存影响 🟡**：steer 注入发生在消息序列**末尾**（插入点之后缓存失效，但 system 前缀 + 早期历史仍可命中）——用户主动纠正，接受此 trade-off；abort 不影响缓存

### 10.5 插件接入 WS（注册自定义事件/方法）

插件导出函数即注册（与 tool_* / command_* 同机制，热重载支持）：

| 导出 | 能力 | 第三方调用方式 |
|---|---|---|
| `rpc_<name>(params)` | 注册自定义 JSON-RPC 方法 | HTTP `POST /rpc` + WS 均可调 `name` 方法 |
| `ws_emit(event, dataJSON)` | 主动向所有 WS 客户端广播自定义事件 | 客户端收到 `{"type":"event","event":"<自定义>","data":...}` |
| `command_<name>(args)` | 注册交互式斜杠命令 | CLI interactive 输入 `/name args` |

插件示例（notify.ts）：

```ts
export function rpc_notify(params) {
  const p = JSON.parse(params || "{}");
  ws_emit("notify", JSON.stringify({level: p.level || "info", msg: "实时通知"}));
  return JSON.stringify({ok: true});
}
```

注意：`ws_emit` 宿主函数在 Server 启动后注入，插件需经 `ReloadAll()` 强制重载才能拿到（`LoadAll` 是增量重载，modTime 未变不会重新执行）。

### 10.6 认证与动态开关

- Bearer token（`--token`）：所有端点统一校验，无 token 返回 401
- `GET /admin/status`：查询三服务开关状态
- `POST /admin/switch` `{"service":"http|jsonrpc|ws","enabled":true|false}`：动态开关，即时生效，重启后回到启动配置

## 11. 初始化向导与配置

### 11.1 交互式初始化（--init）

新机器首次运行：`./mizar --init` 进入 REPL 引导（命令式，与交互对话共用斜杠命令机制）：

| 命令 | 功能 |
|---|---|
| `/provider` | 输入 OpenAI 兼容端点 + API Key，自动 GET /v1/models 验证连接并列出模型 |
| `/model` | 列出供应商全部模型（真实探测），输入序号或模型名选择 |
| `/think` | 思考模式：`on`/`off`/空=自动探测（发请求试 thinking 参数，不支持则关闭） |
| `/context` | 上下文窗口：数字 / `auto`=自动探测（优先模型元数据 → 内置窗口表 → 默认 128000） |
| `/save` | 保存到 `~/.mizar/config.json` |
| `/quit` `/help` | 退出 / 帮助 |

### 11.2 配置存储（~/.mizar/）

```
~/.mizar/
├── config.json    # 供应商 + 模型 + 思考模式 + 上下文窗口
└── AGENTS.md      # 全局指令（可选）
```

- **启动自动加载**：无 `--init` 启动时读 `~/.mizar/config.json`，覆盖默认 flag（`flag.Visit` 判断显式 flag 优先）
- **thinking 透传**：配置开启时请求体带 `"thinking":{"type":"enabled"}`，关闭带 `disabled`（deepseek 等模型支持）
- **路径统一**：`config.ConfigDir()` 是唯一路径来源（配置 + 全局 AGENTS.md 同目录，杜绝漂移）

### 11.3 内置斜杠命令

| 命令 | 功能 | 实现 |
|---|---|---|
| `/reload` | 重载配置 + 插件热重载 + 同步插件命令 | `ReloadAll()` 强制重载（非增量） |
| `/reset` | 重置会话上下文（参照 pi 的 /new） | `agent.Reset()` 清 Initial/steer/abort |
| `/quit` | 退出交互模式 | |

## 12. 内置工具集（pi 式）

`internal/builtins/` 提供 7 个 Go 实现的内置工具，**与插件 `tool_*` 同一通道**（`plugins.Manager.RegisterBuiltin()`，来源标记 `<builtin>`，不受插件热重载影响）。签名对齐 pi：

| 工具 | 参数 | 说明 |
|---|---|---|
| `bash` | command, timeout | 执行 shell 命令（超时 + 输出上限） |
| `grep` | pattern, path, glob, ignoreCase, literal, context | 内容搜索（regex/字面量 + 上下文行） |
| `find` | pattern, path, limit | 文件查找（glob 模式） |
| `read` | path, offset, limit | 读文件（行号分页） |
| `write` | path, content | 写文件（自动建父目录） |
| `edit` | path, oldText/newText 或 edits[] | 精准替换（唯一匹配校验，支持批量） |
| `ls` | path, limit | 列目录（排序 + 目录标记） |

实测：真实 LLM 自主规划 `ls → bash → grep → edit → read → bash → reply` 完成文件修改任务，工具链路闭环。

## 13. AGENTS.md 自动读取

`internal/context/` 实现全局 + 局部相加注入：

- **全局**：`~/.mizar/AGENTS.md`（与配置文件同目录，所有项目共享）
- **局部**：从 `--workdir`（默认 cwd）向上递归查找 AGENTS.md（git 风格，最近者优先）
- **相加**：全局在前 + 局部在后拼接，注入 System prompt（缓存友好：启动时一次读取，运行期稳定）
- 启动日志：`AGENTS.md 注入: /root/.mizar/AGENTS.md, /path/to/proj/AGENTS.md`

## 14. 技术选型实测（Go GC 与二进制体积）

### 14.1 Go GC 对 Agent 的影响：可忽略（实测）

| 指标 | 实测值 |
|---|---|
| STW 暂停 | **14 μs**（模拟 Agent 循环内存模式，20 秒仅触发 1 次 GC） |
| GC 模式 | 并发标记清扫，STW 只有"停止世界"一小段 |

**为什么无影响**：Agent 循环的瓶颈是 LLM 网络往返（数百 ms~秒级），GC 暂停是 μs 级——差 4 个数量级，完全淹没在网络延迟里。核心数据是消息切片（追加为主、压缩器周期性截断），GC 最擅长的短命对象模式。真正要防的是 goroutine 泄漏与无界缓存（已通过插件 30s 超时 + Compactor token 预算规避）。

**建议**：`GOMEMLIMIT` 设上限（如 2GiB），防止内存充裕机器上 GC 懒惰堆涨。

### 14.2 二进制体积实测对比

| 方案 | 体积 | 备注 |
|---|---|---|
| Go 最小 HTTP+JSON 服务 | 5.7M | `-trimpath -ldflags '-s -w'` |
| Go Mizar 完整（goja+esbuild+ws） | 19M | 含全部功能 |
| Rust 最小 HTTP+JSON 服务 | 555K | LTO + panic=abort + strip |
| Node.js 本体（Bun 打包需携带） | 119M | bun build --compile 类项目 90~110M |

**结论**：Go 是 Rust 的 ~10 倍体积（静态链接完整运行时），但 5~20M 在现代机器无体感；Go 是 Bun 的 **1/5**（后者要扛完整 JS 引擎）。Mizar 的 19M 主要来自 goja（JS 引擎）+ esbuild（TS 编译器）——换取插件系统零依赖，对"个人用、单二进制分发"定位正确。

## 15. 深度复刻 deepseek-harness（dsh）的优点

2026-08-28 系统性对比 [@/data/codes/deepseek-harness/] 与 mizar，评估可复刻的产品级能力。dsh 是 DeepSeek AI 的 TypeScript monorepo agent harness（53+ 包，Cordis 插件架构），mizar 是 Go 单二进制 agent 框架——两者定位不同但核心 agent loop 能力可借鉴。

### 15.1 对比结论

| dsh 能力 | mizar 现状 | 是否复刻 | 理由 |
|---|---|---|---|
| Cordis 一切皆插件 / profile / bundle | 三层扩展（技能/插件/MCP） | ❌ | 架构哲学不同；mizar 定位极简单二进制 |
| Web UI / SDK / ACP | 无 | ❌ | 超出范围 |
| **repeat-tool-reminder（循环卫生守卫）** | WeakModelTuner 直接 kill | ✅ | dsh 渐进提醒 [3,5,8] + JSON 参数规范化；比直接 kill 更宽容 |
| **todo_write 工具** | 无 | ✅ | 结构化任务清单；session-owned；UI checklist 渲染 |
| **job_* 后台任务 + bash run_in_background** | bash 全同步 | ✅ | jobs registry + 完成通知自动注入 |
| **ask_user_question 工具** | 无 | ✅ | 文本编号选项作答（TUI/经典/单次任务）；无界面时回退为正文列选项 |
| **skill 按需加载工具** | 索引注入 + read 读文件 | ✅ | 专用 skill 工具一次返回全文，更直接 |
| **会话标题** | UUID 无标题 | ✅ | 首条用户消息 fallback；持久化 titles.json |
| plan mode / goal / schedule | ✅ 已全部复刻 |
| - plan mode | exit_plan_mode 工具 + /plan 命令 + TUI 审批界面 |
| - goal | get_goal / create_goal / update_goal（人类权限约束 + 自阻塞阈值） |
| - schedule | schedule_create / list / delete（after / at / every 三种模式） |
| subagent / workflow engine | 无 | ⏳ | 后续迭代（需引入并发调度抽象） |
| session fork / telemetry | 无 | ⏳ | 后续迭代 |
| 100% 覆盖率门禁 / 快照测试 | scenarios 框架部分覆盖 | 📝 | 工程流程借鉴；不改变产品 |

### 15.2 已复刻实现的细节

#### 15.2.1 Loop Guard（循环卫生守卫）

- 位置：`internal/agent/guard.go`
- 机制：JSON deep key-sort 规范化参数后比较（对齐 dsh `canonicalize`/`sortJsonValue`）
- 阈值默认 [3, 5, 8]：首个发温和提醒，后续发详细提醒（点名工具/次数/参数预览截断 500 字符）
- 超过最高阈值仍重复 → 终止任务
- 集成到 `loop.go`：在工具执行后观察（dsh post-execute semantics），而非执行前拦截
- 向后兼容：保留 `WeakModelTuner.RecordToolCall` API（直接测试仍在通过）

#### 15.2.2 Todo Write 工具

- 位置：`internal/builtins/todo.go`
- 参数：`{todos: [{content, status}]}`；校验非空、去重、单 in_progress 约束
- 行为：whole-list replace（last-write-wins）
- 状态：进程内全局 registry（mizar 当前单 session 场景足够）
- 渲染：`RenderTodos()` 返回格式化 checklist（▶ in_progress / ✓ completed / ○ pending）

#### 15.2.3 后台任务（Jobs）

- 位置：`internal/jobs/` + `internal/builtins/job_tools.go`
- Registry：进程内 map，id 形如 `<kind>-N`（predictable）
- 工具：`job_list` / `job_output`（支持 wait + timeout_ms 阻塞轮询） / `job_kill`
- 完成通知：`Agent.Jobs.DrainDone()` 每步开始时由 loop 消费并注入 user 消息
- bash 的 `run_in_background`：通过 `Agent.RunInBackground` 字段注入（CLI/Server 模式可设置，TUI 模式默认启用）

#### 15.2.4 Ask User Question

- 位置：`internal/builtins/ask.go` + `cmd/mizar/ask.go` + `cmd/mizar/tui.go`
- **文本编号选项**：问题渲染为「1) xxx  2) yyy」，用户直接回复编号即完成选择
  - 经典 readline（`-tui=false`）：`interactive()` 通过 liner 逐问读取
  - TUI：问题渲染进聊天区，输入框回编号（`tuiModel.askViaChat`）
  - 单次任务（`-task`）：stdin 为终端时同样可用；管道/CI 下自动跳过
  - 多选：`1,3` 或 `1 3`；直接回车 = 第 1 项；非编号输入按自由文本；越界编号不静默取错项
- **无交互界面降级**：不再只说「请自行决策」，而是让模型把选项作为普通文本写进正文，
  用户下一条消息回编号（`RenderPlainQuestions`）
- 超时/EOF/ESC 均返回空答案列表而非报错——提问失败不应打断任务
- 答案格式：`{answers: [{id, value}]}` 或 `{answers: [{id, values}]}`（multi_select）

> 踩坑记录：`builtins.agentAskUser` 与 `agent.agentAskPlanUser` 是**包级全局变量**，
> 只设置 `Agent.AskUser` 字段不会生效（工具读的是全局）。此前 `SetAskUser` / `SetPlanAskUser`
> 全项目零调用者，导致 TUI 里 ask 也一直走降级分支、`exit_plan_mode` 静默 approve。
> 现在 TUI / 经典 / 单次任务三条路径都显式设置这两个全局回调。

#### 15.2.5 Skill 按需加载工具

- 位置：`internal/builtins/skill_tool.go`
- 行为：`skill <name>` 从技能目录查找并一次性返回完整正文（[技能:x] 格式）
- 与索引注入互补：system prompt 仍只注入 name+description（缓存友好），模型需要时按需调用 skill 工具

#### 15.2.6 会话标题

- 位置：`internal/session/title.go`
- 推导：取首条 RoleUser 消息，去换行，截断 60 字符 + `…`
- 持久化：`titles.json`（每个 session store 根目录一份）
- `/sessions` 命令展示标题列

### 15.4 借鉴 pi agent v1.1.0（2026-10-09）

系统性解析 pi 的 CHANGELOG（287 版本 / 6200 行 / 3048 条），排除 provider / codemode / SDK 类
（mizar 走璇玑网关、不追求 pi 生态兼容），聚焦 agent 核心机制后选定 5 项引入。

| pi 能力 | 引入 | 落地位置 |
|---|---|---|
| 摘要结构化格式 + 文件追踪 | ✅ | `internal/agent/summarize.go`、`compactor.go` |
| 压缩切点规则（split user span / 无空 system） | ✅ | `internal/agent/compactor.go` |
| 显著 cache miss 提示 | ✅ | `internal/agent/cache_notice.go`、`cmd/mizar/tui.go` |
| bash 增量输出流式 | ✅ | `internal/builtins/bash_stream.go`、`cmd/mizar/bash_live.go` |
| 通用工具 `terminate: true` | ✅ | `plugins.Tool.Terminate`、`loop.go` |
| TUI fullscreen / OSC 8 / mermaid / 主题 | ❌ | mizar TUI 是 tview 自绘，迁移性价比低 |
| codemode / classifier / virtual models / SDK | ❌ | 与「零依赖单二进制 + 走网关」定位冲突 |
| MCP OAuth 加固 / cache warming | ❌ | 内网场景无 OAuth；按量付费下收益待评估 |

#### 15.4.1 摘要保真（serializeConversation + 文件追踪）

- `serializeConversation`：工具调用渲染为单行 `edit(path="/x", edits=[2 item(s)])`，
  工具结果截断至 800 字符，普通消息保留正文（对齐 pi `serializeConversation`）
- `collectFileTracking`：从 `read`/`files_fuzzy`/`write`/`edit` 的调用参数提取路径，
  去重后累计成 `<read-files>` / `<modified-files>` 清单（对齐 pi Cumulative File Tracking）
- `SummaryPrompt` 补充 `## Constraints & Preferences` 节（用户提过的约束不再压缩即丢）
- 唯一入口 `agent.BuildSummaryInput(msgs)`，LLM 层只负责发请求

> 澄清：早期分析曾以为「工具调用信息在压缩时丢失」，实测否定——`KindToolCall` 消息的
> `Content` 本身就是 `{"action":"tool",...}` JSON。本项改的是**可读性与文件清单**，不是数据丢失。

#### 15.4.2 压缩切点修复

- **去掉空 system 占位**：原 `Compact` 在无 system 输入时插入 `Message{Role: RoleSystem, Content: ""}`，
  白占 prompt 预算且干扰缓存前缀（实测输出 `[0] role="system" content=""`）。现只在首条确为 system 时保留。
- **split user span**：当切点落在某个 user span 内部（该 span 自身超 KeepRecentTokens）时，
  额外为该 span 前缀生成一份摘要并与历史摘要合并（对齐 pi `isSplitTurn`），
  避免「前半段进摘要、后半段被保留」造成语义断裂。前缀摘要失败不阻断主流程。

#### 15.4.3 显著 cache miss 提示

`agent.CacheMissDetector`：命中率从高位下跌 ≥30 个百分点且落到 50% 以下时提示一次，
命中率恢复后允许再次提示（避免刷屏）。约束：prompt < 5000 tokens 不判定（比率噪音大）、
首次观测无基线不提示。TUI 在流内 usage 与任务结束补记两处统一走 `recordUsage`，
保证每请求只观测一次。

#### 15.4.4 bash 增量输出

- `builtins.streamWriter`：同时充当 `io.Writer` 与节流回调（80ms），并发安全；
  `Flush` 保证命令结束/超时后尾部输出不丢
- 注入方式与 `ask_user_question` 一致（包级全局 `SetBashStream`，TUI 在任务开始时注册、结束时注销）
- TUI 侧 `bash_live.go`：内存缓冲只保留尾部（上限 64KB），渲染时取末尾 12 行；
  回调为 nil（Server/CI）时退化为纯累计，行为与改动前一致
- 工具返回值与 50KB/2000 行截断语义、超时进程组 kill 行为均不变

#### 15.4.5 通用工具 terminate

`plugins.Tool.Terminate`：工具执行成功且声明该字段时，以返回值作为最终回答，
省掉一次 follow-up LLM 调用（对齐 pi 0.69.0 `terminate: true`）。执行失败时忽略声明——
错误需回填给模型纠正。`respond` 保持原有特判路径不变。

> `exit_plan_mode` 的终止语义是动态的（approve 才终止、keep_planning 不终止），
> 静态字段不适用，故未套用——避免引入回归。

### 15.5 架构决策

- **无 import cycle**：jobs 包独立于 agent/builtins，builtins 不 import agent（通过参数传递 jobs registry）
- **最小侵入**：所有新功能以插件式工具形式注册，不修改现有工具行为
- **向后兼容**：WeakModelTuner 保留；Guard 默认开启（NewRepeatGuard 在 New() 中初始化）
- **降级策略**：无交互界面时 ask_user_question 退化为「正文列选项 + 用户下一条回编号」，不停止 agent

#### 15.2.7 Plan Mode（计划模式）

- 位置：`internal/agent/plan.go` + `internal/builtins/plan_tool.go`
- 命令：`/plan [guidance]` 进入（可附带规划指引）；`/plan off` 退出
- 工具：`exit_plan_mode` —— 模型调用时弹出用户审批界面（TUI 模式）
- 系统提示词：计划模式激活时在 SystemPrompt 末尾注入 guidance 文本块
- 审批交互：通过 `agentAskPlanUser` 回调阻塞等待用户选择（Approve / Keep planning）
- 降级：无 TUI 时直接批准（plan mode 在 CLI 模式等价于静默通过）

#### 15.2.8 Goal（会话目标）

- 位置：`internal/agent/goal.go` + `internal/builtins/goal_tools.go`
- 工具：`get_goal` / `create_goal` / `update_goal`
- 命令：`/goal <目标>` 创建 ｜ `/goal status` 查看 ｜ `/goal pause`/`resume`/`complete`/`blocked <原因>`/`clear`（复刻 pi-goal）
- 生命周期：pending → in_progress → completed / blocked / paused
- 权限约束：create/edit/pause/resume 需人类直接消息（`MarkHumanTurn` 标记）；complete/blocked 可由模型自动报告
- 自阻塞阈值：默认 3 轮连续同条件才允许 self-block（防止误报）
- 单会话单目标简化版（dsh 支持多目标，mizar 定位极简故只保留一个）

#### 15.2.9 Schedule（定时提醒）

- 位置：`internal/schedule/schedule.go` + `internal/builtins/schedule_tools.go`
- 工具：`schedule_create` / `schedule_list` / `schedule_delete`
- 三种模式：`after <n>秒`（延迟）/ `at <RFC3339>`（绝对时间）/ `every <n>秒`（固定间隔，最小 300 秒）
- 投递：后台 ticker 每 10 秒检查到期项，投递回调由 main.go 设置（打印到终端 + 日志）
- 持久化：提醒记录在内存中，重启后丢失（dsh 通过 session log 持久化；mizar 简化为进程内）

## 17. 参考

- [Pi Agent](https://github.com/mariozechner/pi) —— 极简 + 自举的灵感，compaction/loop/RPC(steer) 设计参考
- [pi_agent_rust](https://github.com/Dicklesworthstone/pi_agent_rust) —— Rust 移植，extensions_js.rs 的插件系统参考
- [goja](https://github.com/dop251/goja) —— 纯 Go JS 引擎
- [esbuild Go API](https://pkg.go.dev/github.com/evanw/esbuild/pkg/api) —— 内嵌 TS 编译器
- [Agent Skills (Anthropic)](https://www.anthropic.com/engineering/agent-skills) —— SKILL.md 标准
- [Model Context Protocol](https://modelcontextprotocol.io) —— 工具协议标准
- [Don't Break the Cache (arXiv 2601.06007)](https://arxiv.org/abs/2601.06007) —— LLM 缓存策略研究
- [ProjectDiscovery Cache Hacks](https://projectdiscovery.io/blog) —— 动态工作记忆缓存陷阱案例
- [tsgo (TypeScript Go 重写)](https://github.com/microsoft/typescript-go) —— 未来嵌入式类型检查
