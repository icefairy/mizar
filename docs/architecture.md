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
vm.Set("http_get", httpGet)       // HTTP GET/POST（可选内网代理）
vm.Set("json_decode", jsonDecode) // JSON 解析
vm.Set("json_encode", jsonEncode)
vm.Set("fs_read", fsRead)         // 文件读写（限定在项目目录内）
vm.Set("fs_write", fsWrite)
vm.Set("fs_list", fsList)
vm.Set("shell_exec", shellExec)   // 执行命令（白名单 + 超时 + 输出上限）
vm.Set("llm_chat", llmChat)       // 调 LLM —— 自举闭环的钥匙
vm.Set("log", log)                // 日志
vm.Set("sleep", sleep)
```

```ts
// extensions/disk_watch.ts —— Agent 自写插件示例
export function tool_disk_watch(): string {
  const out = shell_exec("df -h / | tail -1");
  const pct = parseInt(out.split(/\s+/)[4]);  // "80%"
  if (pct > 80) {
    http_post("http://notify.internal/feishu", json_encode({ msg: "磁盘占用 " + pct + "%" }));
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

### v0.1 —— 验证自举闭环（1-2 周）
- [ ] Go 骨架 + goja 引擎 + esbuild 编译管线
- [ ] 宿主函数集（http/json/fs/shell/llm_chat）
- [ ] 插件加载器：扫描 extensions/ → 编译 → 注册
- [ ] Agent 循环：规划 → 工具调用 → 错误修复
- [ ] 手写一个插件 + 让 Agent 写一个插件，双路径验证

### v0.2 —— 三层扩展打通（2-4 周）
- [ ] SKILL.md 加载器（解析 frontmatter → 注入 system prompt）
- [ ] MCP 客户端（stdio + HTTP）
- [ ] 自举沉淀：Agent 写完插件自动写 SKILL.md 文档
- [ ] 会话管理（JSONL 树形，参考 pi）

### v0.3 —— 内网产品化（1-2 月）
- [ ] 弱模型宽容循环调优（错误修复、逐步降级）
- [ ] 安全沙箱加固（限额、白名单、审计日志）
- [ ] 与璇玑网关深度集成（缓存命中感知、成本控制）
- [ ] 三平台交叉编译 + 发布（GitHub + Gitee）

## 9. 参考

- [Pi Agent](https://github.com/mariozechner/pi) —— 极简 + 自举的灵感
- [pi_agent_rust](https://github.com/Dicklesworthstone/pi_agent_rust) —— Rust 移植，extensions_js.rs 的插件系统参考
- [goja](https://github.com/dop251/goja) —— 纯 Go JS 引擎
- [esbuild Go API](https://pkg.go.dev/github.com/evanw/esbuild/pkg/api) —— 内嵌 TS 编译器
- [Agent Skills (Anthropic)](https://www.anthropic.com/engineering/agent-skills) —— SKILL.md 标准
- [Model Context Protocol](https://modelcontextprotocol.io) —— 工具协议标准
- [tsgo (TypeScript Go 重写)](https://github.com/microsoft/typescript-go) —— 未来嵌入式类型检查
