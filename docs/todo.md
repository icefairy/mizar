# Mizar 编码能力增强 TODO

> 来源：参考 openclaude 编码优点调研（2026-08-13）
> 目标：将 mizar 打造为具备完整离线编码能力的 Agent 框架

---

## ✅ 已完成

### 1. [x] AbortReason 归一化与分类

- **文件**：`internal/utils/abortreason.go`、`internal/agent/steer.go`
- **说明**：引入 `AbortReason` 枚举（8 种原因）+ `NormalizeAbortReason()` 归一化函数 + `AbortReasonMessage()` 用户文案映射
- **引用**：`agent.ErrAborted`、`agent.ErrTimeout`、`agent.ErrMaxSteps`
- **参考**：openclaude `abortReasons.ts`

### 2. [x] 文档格式解析（PDF / DOCX / XLSX）

- **文件**：`internal/utils/document.go`
- **说明**：
  - `ExtractPDF()` — 用 `rsc.io/pdf` 提取 PDF 文本，按 PDF 坐标系排序（Y 降序 → X 升序）
  - `ExtractDOCX()` — 用 `archive/zip` + `encoding/xml` 解析 `word/document.xml`，提取段落文本（零外部依赖）
  - `ExtractXLSX()` — 用 `github.com/xuri/excelize/v2` 提取每个 sheet 的行列数据（tab 分隔）
  - `DescribeImage()` — 识别图片格式和大小
- **接入点**：`internal/builtins/files.go` → `toolRead()` 自动检测 `.pdf`/`.docx`/`.xlsx` 扩展名
- **依赖**：`rsc.io/pdf v0.1.1`、`github.com/xuri/excelize/v2 v2.11.0`
- **参考**：openclaude `FileReadTool`（PDF/图片检测）

### 3. [x] LSP 依赖引入

- **文件**：`go.mod`
- **说明**：引入 `go.lsp.dev/jsonrpc2 v1.0.1` + `go.lsp.dev/protocol v1.0.1`
- **参考**：openclaude LSPTool

### 4. [x] LSP 客户端实现（LSPTool 当 Agent 工具）

- **文件**：`internal/lsp/client.go`、`internal/lsp/tools.go`
- **说明**：
  - 通过 stdio 启动语言服务器（gopls/tsserver），jsonrpc2 通信
  - 初始化流程：`initialize` → `initialized` 通知
  - 文档通知：`DidOpen` / `DidChange` / `DidClose`
  - 查询方法：`Diagnostics`（诊断）/ `Definition`（定义跳转）/ `References`（引用查找）/ `Completion`（补全）
  - 工具注册：`lsp_diagnostics` / `lsp_definition` / `lsp_references` / `lsp_completion`
- **接入点**：`cmd/mizar/main.go` → `--lsp <binary>` 启动 + `pm.RegisterBuiltin()` 注册工具；未配置时工具优雅降级提示
- **参考**：openclaude LSPTool

### 5. [x] LSP 服务器实现（mizar 自身作为 LSP server）

- **文件**：`internal/lsp/server.go`
- **说明**：
  - 通过 stdio 接收 JSON-RPC 请求，`jsonrpc2.HandlerServer` 处理
  - 生命周期：`initialize` / `initialized` / `shutdown` / `exit`
  - 查询方法：`textDocument/diagnostic` / `textDocument/completion`
  - 插件扩展：`RegisterProvider` 接口 + `GlobalServer` 全局实例
  - 便捷函数：`ServeStdio(ctx, cfg)` 一行启动
- **参考**：openclaude LSP server

### 7. [x] 顺序执行包装器 sequential

- **文件**：`internal/utils/sequential.go`
- **说明**：`Sequential(fn)` 将并发调用的异步函数包装为串行执行，按入队 FIFO 顺序处理，防文件写竞争
- **参考**：openclaude `sequential.ts`

### 8. [x] RaceAbort + 并发 map

- **文件**：`internal/utils/boundedasync.go`
- **说明**：`RaceAbort(ctx, fn)` ctx 取消/超时与协程完成的竞争；`MapWithConcurrency(ctx, items, concurrency, mapper)` 带并发控制+取消+fail-fast 的 map；`ThrowIfAborted(ctx)` 同步检查点
- **参考**：openclaude `boundedAsync.ts`

### 9. [x] CircularBuffer 泛型工具

- **文件**：`internal/utils/circularbuffer.go`
- **说明**：`CircularBuffer[T]` 泛型环形缓冲，固定容量，`Add`/`AddAll`/`GetRecent(n)`/`ToArray`/`Peek`/`Clear`/`Length`/`Capacity`
- **参考**：openclaude `CircularBuffer.ts`

### 10. [x] 稳定 JSON 序列化

- **文件**：`internal/utils/stablejson.go`
- **说明**：`StableJSON(v)` 对 map 类型按 key 字典序排序后序列化，确保相同内容产出相同 JSON，可用于哈希/缓存
- **参考**：openclaude `stableStringifyJson`

### 11. [x] 安全磁盘任务输出（O_EXCL + session 隔离）

- **文件**：`internal/utils/safedisk.go`
- **说明**：`DumpToTemp(content)` 安全落盘（O_EXCL 防 symlink + session 隔离 + 5GB 上限）；`TaskOutputPath(projectRoot, taskId)` 生成任务输出路径；`SetTaskOutputDir/GetTaskOutputDir` 配置任务输出目录
- **参考**：openclaude `diskOutput.ts`

### 12. [x] 结构化操作跟踪（QueryLifecycleOperationTracker）

- **文件**：`internal/lifecycle/lifecycle.go`
- **说明**：
  - `QueryLifecycle` 查询级生命周期跟踪：`GenerateQueryID()` / `BeginStep()` / `BeginOperation`/`EndOperation` / `Snapshot()` / `FormatLog()` / `ContextWithQuery`（context 注入）
  - ✅ 已接入 `internal/agent/loop.go`：所有 `HookContext.RunID` 使用 queryID，`Step`/`Operation` 结构化跟踪
  - `Agent.Run()` 中：LLM 调用 `BeginOperation("llm")`/`EndOperation("llm")`；工具调用 `BeginOperation("tool:")`；压缩 `BeginOperation("compact")`；终止路径 `q.Complete(err)`
- **参考**：openclaude `QueryLifecycleOperationTracker`

### 13. [x] RepoMap（文件依赖关系图）

- **文件**：`internal/repo/repo_map.go`
- **说明**：`Build(root)` 遍历生成文件级仓库地图（目录/文件/语言/大小）；`Render(maxFiles)` 生成可读文本，按目录分组 + 超限截断
- **参考**：openclaude `RepoMapTool`

### 14. [x] 弱模型宽容循环调优

- **文件**：`internal/agent/weakmodel.go`
- **说明**：`WeakModelTuner` 弱模型宽容策略
  - `ParseFailed` — 连续解析失败检测，逐级升级提示（简单→严格→放弃）
  - `RecordToolCall` — 死循环检测（同工具同参数连续 N 次→强制中断）
  - `LLMFailed`/`LLMSucceeded` — LLM 临时错误自动重试 + 指数退避
  - `RemainingSteps` — 步数上限限制，防止无限循环
- **参考**：openclaude 弱模型调优模式

---

## ✅ 全部完成（14/14）

### 6. [x] LSP 插件扩展（JS 插件层注册 Provider）

- **文件**：`internal/engine/engine.go`、`internal/lsp/pluginbridge.go`、`internal/lsp/server.go`、`internal/plugins/loader.go`、`extensions/lsp_sample.ts`
- **说明**：
  - JS 插件通过 `lsp_register_diagnostic(name, fn)` / `lsp_register_completion(name, fn)` 注册自定义 Provider
  - 桥接层把 JS 回调的 JSON（诊断/补全数组）转换为 protocol 类型
  - pending 队列：server 初始化前注册的 provider 自动暂存，创建后 drain
  - `Unregister` 按名移除（插件热重载时清理旧 provider，防旧 VM 悬挂）
  - 插件管理器跟踪每插件注册名，热重载时自动清理；允许 LSP-only 插件加载
  - 测试：`internal/lsp/pluginbridge_test.go`（7 个用例：解析/端到端/错误路径）
- **优先级**：P2 → ✅

---

## 📊 统计

- **总项**：14
- **已完成**：14（1-14 全部完成）
- **完成度**：100%
