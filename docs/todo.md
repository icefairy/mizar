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

- **文件**：`internal/utils/lifecycle.go`
- **说明**：`QueryLifecycle` 查询级生命周期跟踪：`GenerateQueryID()` / `BeginStep()` / `BeginOperation`/`EndOperation` / `Snapshot()` / `FormatLog()` / `ContextWithQuery`（context 注入）
- **参考**：openclaude `QueryLifecycleOperationTracker`
- **工作量估算**：~120 行
- **状态**：✅ 基础工具完成；❌ 尚未接入 `internal/agent/loop.go`（接线为后续项）

### 13. [x] RepoMap（文件依赖关系图）

- **文件**：`internal/repo/repo_map.go`
- **说明**：`Build(root)` 遍历生成文件级仓库地图（目录/文件/语言/大小）；`Render(maxFiles)` 生成可读文本，按目录分组 + 超限截断
- **参考**：openclaude `RepoMapTool`
- **状态**：✅ 完成

---

## 🔧 进行中

### 5. [ ] LSP 服务器实现（mizar 自身作为 LSP server）

- **目标**：`internal/lsp/server.go` — mizar 作为 LSP 服务器，支持插件注册自定义 provider
- **功能**：initialize/initialized/shutdown/textDocument/completion/diagnostics
- **工作量估算**：~300 行
- **优先级**：P1

### 6. [ ] LSP 插件扩展

- **目标**：`internal/lsp/providers/provider.go` — 插件通过 `lsp.RegisterDiagnosticProvider()` 注册自定义能力
- **工作量估算**：~50 行
- **优先级**：P2

### 12b. [ ] 结构化操作跟踪接入 Agent 循环

- **目标**：`internal/agent/loop.go` — 将 `utils.QueryLifecycle` 接入 `Agent.Run()`，queryId/step 写进 HookContext 与日志
- **工作量估算**：~30 行

### 14. [ ] 弱模型宽容循环调优

- **目标**：错误修复、逐步降级策略
- **工作量估算**：~100 行

---

## 📊 统计

- **总项**：14
- **已完成**：11（1-4、7-13）
- **进行中**：3（5、6、14）
- **完成度**：79%
