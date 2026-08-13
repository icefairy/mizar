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
│  │ SKILL.md │   │  .ts 文件 │   │ (stdio/HTTP) │    │
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
| **插件层** | .ts 文件 | 可执行的新工具/能力 | Agent 写代码，esbuild 编译，热加载 |
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

## 15. 参考

- [Pi Agent](https://github.com/mariozechner/pi) —— 极简 + 自举的灵感，compaction/loop/RPC(steer) 设计参考
- [pi_agent_rust](https://github.com/Dicklesworthstone/pi_agent_rust) —— Rust 移植，extensions_js.rs 的插件系统参考
- [goja](https://github.com/dop251/goja) —— 纯 Go JS 引擎
- [esbuild Go API](https://pkg.go.dev/github.com/evanw/esbuild/pkg/api) —— 内嵌 TS 编译器
- [Agent Skills (Anthropic)](https://www.anthropic.com/engineering/agent-skills) —— SKILL.md 标准
- [Model Context Protocol](https://modelcontextprotocol.io) —— 工具协议标准
- [Don't Break the Cache (arXiv 2601.06007)](https://arxiv.org/abs/2601.06007) —— LLM 缓存策略研究
- [ProjectDiscovery Cache Hacks](https://projectdiscovery.io/blog) —— 动态工作记忆缓存陷阱案例
- [tsgo (TypeScript Go 重写)](https://github.com/microsoft/typescript-go) —— 未来嵌入式类型检查
