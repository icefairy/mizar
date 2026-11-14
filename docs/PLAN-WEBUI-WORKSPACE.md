# Mizar Web UI · 文件上传与工作空间规划

> v2.0 ｜ 2026-11-14 13:55 ｜ 目标仓库 `/data/codes/mizar`（Go 单二进制，Web UI 走 `//go:embed`）
>
> 需求：Web UI 是 **B/S 架构**，客户端（浏览器）要能把**本地文件上传到运行 Mizar 的服务器磁盘**；
> 工作空间**开箱可用**、前端可改根目录；上传文件**按会话 ID 分目录**存放，形成网页版「工作空间」概念，
> 让 Agent 能读到、能处理；**会话删除时联动清理**目录。

**已拍板的三个决策（2026-11-14 13:55）**：

| # | 决策 |
| --- | --- |
| 1 | **方案 3：保留跨会话并发 + per-conv 工作空间**（不用 `os.Chdir`、不降级串行） |
| 2 | **默认开箱可用**（无需配置即可上传），前端可选其他路径作为工作空间根目录 |
| 3 | **会话删除 → 联动清理**该会话的工作空间目录 |

⚠️ 本文是**规划**，不含实现。当前版本 `v0.8.6`。落地前先读 `mizar-development` 技能 +
`docs/api.md` + `internal/server/webui.go`。本规划路径/结构以 2026-11-14 代码为准。

> **v2.0 相对 v1.0 的重大修订**：v1.0 推荐「运行前 `os.Chdir` + 工作空间模式降级串行」（方案 1）。
> 用户选了方案 3 之后，**Chdir 路线整体作废**——进程级 cwd 是全局状态，跨会话并发时必然互相踩，
> 方案 3 与方案 1 不是「去掉串行锁」的关系，而是**完全不同的实现：per-call 路径注入**。
> 本版 §一 / §四 / §六 / §九 全部重写。

---

## 一、先说结论（方案 3 的技术本质）

| 结论 | 内容 |
| --- | --- |
| **方案 3 = per-call 路径注入，不是 Chdir** | 进程只有一个 cwd，`os.Chdir` 是全局可变状态，跨会话并发必冲突。方案 3 要在保留并发的前提下 per-conv 隔离，**唯一干净的路是：把「会话目录」作为参数逐次传给工具，工具内部用绝对路径解析，绝不动进程 cwd** |
| **不用改 `Tool.Run` 签名、不碰插件 ABI** | `plugins.Tool`（loader.go:25）加一个**可选**字段 `RunInDir func(cwd, args string) (string, error)`；`Manager` 加 `CallInDir(name, args, cwd)`。老工具（含全部 JS 插件 tool_*）不填该字段 → 自动退回原 `Call`，行为字节不变 |
| **bash 侧几乎零成本** | `shell.Command()` 返回 `*exec.Cmd`，`cmd.Dir = cwd` 一行即让命令在会话目录执行（builtins.go:70）。不需要改 shell 包 |
| **真实横切 = 8 个内置工具、约 20 处路径解析** | `read`/`write`/`edit`/`ls`/`grep`/`find`/`repo_map` 的 `os.Stat`/`ReadFile`/`WriteFile`/`ReadDir`/`filepath.Walk` 全部改成经过统一 helper `resolvePath(cwd, p.Path)`；`bash` 设 `cmd.Dir`。`CreateTemp` 那 3 处是临时文件，不碰 |
| **注入点只需改 loop.go 一行** | `loop.go:1077` `a.Plugins.Call(c.Tool, c.Args)` → `a.Plugins.CallInDir(c.Tool, c.Args, a.WorkDir)`。`a.WorkDir` 在每会话 Clone 后设为 `<root>/<convID>`（WorkDir 是可写字段，Clone 复制后可覆盖） |
| **跨会话并发天然成立** | cwd 是**参数**不是全局状态。A 会话的工具拿 `root/A/`、B 拿 `root/B/`，各自 goroutine 互不干扰。无需 `workspaceMu`、无需降级串行 |
| **`a.WorkDir` 一物两用** | ① loop.go:455-460 注入系统提示词「当前工作目录=`<root>/<convID>`」，模型知道该用相对文件名；② 作为 `CallInDir` 的 cwd 实参，让工具真正在该目录执行。**这次两者终于一致了** |
| ⚠️ **JS 插件工具的路径操作不在改造内** | 插件 `tool_*` 在 goja VM 里跑 JS，路径由 JS 侧 fs API 决定，Go 层注入不到。一期只保证**内置工具** per-conv；插件工具若需要工作空间，二期给 VM 注入 `MIZAR_CWD` 全局并约定插件改用它（见 §十） |
| **开箱可用 = 默认根目录** | 未配置时用 `~/.mizar/workspaces/`（跟随 HOME，测试可隔离），首次上传自动 `MkdirAll`。`webSettings.WorkspaceRoot` 为空即取默认值 |
| **联动清理** | 删除会话时 `os.RemoveAll(<root>/<convID>)`，必须先校验路径落在 root 内、convID 合法，防误删 |

> **一句话**：方案 3 的代价是「改 8 个内置工具 + 1 个注入点 + 1 行 loop」，换来「不动全局状态、保留并发、
> 插件零改动」。比 v1.0 的 Chdir 方案改动面大，但语义干净、无并发坑。

---

## 二、需求拆解（四块）

1. **上传能力**：浏览器选/拖拽文件 → `POST /web/api/workspace/upload` → 服务器落盘。多选、进度、失败重试。
2. **开箱可用 + 前端可改路径**：默认 `~/.mizar/workspaces/` 直接可用；设置面板可改成服务器其他绝对路径，
   存 `~/.mizar/webui.json`，重启 `ApplyStoredSettings` 恢复。
3. **按会话隔离 + Agent 可读（方案 3）**：`<root>/<convID>/` 存该会话文件；通过 `CallInDir` 让
   `read`/`bash`/`ls` 以会话目录为基准，模型用相对文件名即可读到；跨会话并发不互相干扰。
4. **联动清理**：删会话 → 删该会话工作空间目录。

---

## 三、现状地基（写任何代码前先认这些）

| 地基 | 位置 | 对方案 3 的约束 |
| --- | --- | --- |
| `plugins.Tool.Run func(args string)` 无 ctx/cwd | `loader.go:25-40` | 不改签名，用**新增可选字段** `RunInDir` 旁路注入，保持 ABI 兼容 |
| `Manager.Call(name,args)` → `Get` → `go func(){ t.Run(args) }()` | `loader.go:721-760` | 加 `CallInDir` 同构实现；工具仍在独立 goroutine + panic recover，结构照抄 |
| JS 插件工具 `Run` 闭包 `vm.Call(name,args)` | `loader.go:478-510` | 不填 `RunInDir`，走原 `Call`，零改动 |
| `CallCommand` / RPC 各有独立 `Run` 调用点 | `commands.go:105`、`rpc.go:98` | 斜杠命令 / RPC **不接工作空间**（控制面，非文件操作），保持不动 |
| loop.go:1077 是 Agent 工具调用唯一入口 | `internal/agent/loop.go` | 改这一行为 `CallInDir(..., a.WorkDir)` |
| `a.WorkDir` 可写、Clone 复制、仅注入提示词 | `clone.go:29`、`loop.go:455-460` | Clone 后覆盖成会话目录；一物两用（提示词 + cwd 实参） |
| `a.ConvID` 每会话独立 | `loop.go:123` | 会话目录名来源（也可直接用 handler 里的 convID） |
| 8 个内置工具用相对路径 | `builtins.go:58`、`files.go:191/304/331/438`、`repo.go:16`、`search.go:18/107` | 逐个接 `resolvePath`；`files.go` 约 12 处、`search.go` 约 6 处 `filepath.Walk/ReadFile` |
| `shell.Command` 返回 `*exec.Cmd` | `builtins.go:70`、`shell.go:281` | `cmd.Dir = cwd` 一行搞定 bash |
| 全量 `os.`/`filepath.` 调用 64 处 / 10 文件 | `internal/builtins/*.go` | 真正要改的只有「解析用户传入 path」的 ~20 处；临时文件、BOM、NFKC 等不碰 |
| Web 端点 cookie 鉴权 `webAuth` | `webui.go:265` | 5 个新端点全过它 |
| 端点三处同步登记 | `webui.go:232-261`、`routemeta.go:41`、`docs/api.md` | 加 5 端点，`/admin/routes` 与 `-gen-openapi` 才不漏报 |
| `webSettings` 持久化 + 重启恢复 | `webstore.go:66`、`webui.go:878` | 加 `workspace_root`，零新增持久化机制 |
| `webStore` 有 `mu` 锁 + 指纹检测 | `webstore.go` | 元数据若存 JSON 复用；或纯磁盘扫描 |
| 同会话串行锁 + 跨会话并发 | `server.go:34-44` | **方案 3 不需要动这些锁**——这正是选它的意义 |
| 前端 `//go:embed` / `web.frontend_dir` 外部化 | 技能 | 改前端前先 `-webui-out` 释放外部目录，改完即时生效 |

---

## 四、方案 3 核心架构（per-call 路径注入）

### 4.1 数据结构改动（ABI 兼容）

```go
// internal/plugins/loader.go
type Tool struct {
    Name, Description, PluginFile string
    Run      func(args string) (string, error)          // 保留，老工具/插件都用
    // RunInDir 可选：需要 per-call 工作目录的工具填这个。
    // 非空时 Manager.CallInDir 优先调它；为空则 CallInDir 退回 Call（走 Run）。
    // 这样内置文件工具能接 cwd，JS 插件工具零改动。
    RunInDir func(cwd, args string) (string, error)
    Terminate bool
    NoTimeout bool
}
```

### 4.2 Manager 加同构调用路径

```go
// CallInDir：优先 RunInDir(cwd,args)，否则退回 Call(name,args)。
// 结构照抄 Call（独立 goroutine + panic recover + maxExec 超时），只把 t.Run(args)
// 换成 t.RunInDir(cwd, args)。
func (m *Manager) CallInDir(name, args, cwd string) (string, error)
```

> 别把 cwd 塞进 `Manager` 字段（`m.cwd`）——那是共享 Manager，跨会话并发会互相覆盖，又退回全局状态陷阱。
> **cwd 必须是每次调用的参数**，这是方案 3 的灵魂。

### 4.3 统一路径解析 helper（新增 `internal/builtins/workspace.go`）

```go
// resolvePath 把用户/模型给的相对路径锚到会话目录。
// cwd 为空 → 原样返回（CLI/TUI 行为完全不变）；绝对/含 .. → 清洗后仍须落在 cwd 内，
// 否则拒绝（防模型用 ../ 跳出工作空间读到别的会话或系统文件）。
func resolvePath(cwd, p string) (string, error) {
    if cwd == "" { return p, nil }
    if p == "" { p = "." }
    abs := p
    if !filepath.IsAbs(p) { abs = filepath.Join(cwd, p) }
    abs = filepath.Clean(abs)
    // 边界校验：abs 必须等于 cwd 或以 cwd+sep 开头
    if abs != cwd && !strings.HasPrefix(abs, cwd+string(os.PathSeparator)) {
        return "", fmt.Errorf("path %q escapes workspace %q", p, cwd)
    }
    return abs, nil
}
```

- **CLI/TUI 零回归**：`cwd==""` 时直接返回原路径，现有 958 个测试不因这层改变行为。
- **防越界**：模型给 `../../etc/passwd` 或别的会话相对路径会被拒，这是 per-conv 隔离的安全边界。

### 4.4 8 个内置工具接 cwd

| 工具 | 改法 |
| --- | --- |
| `bash` | `cmd := shell.Command(p.Command)` → `cmd.Dir = cwd`（cwd 空则不设） |
| `read` | `p.Path = resolvePath(cwd, p.Path)` 后再 `os.Stat` / `ReadFile` / 文档解析 |
| `write` | `resolvePath` → `MkdirAll(filepath.Dir)` → `WriteFile` |
| `edit` | `resolvePath` → `ReadFile` / `WriteFile` |
| `ls` | `resolvePath` → `os.ReadDir` |
| `grep` | `resolvePath(p.Path)` 作为 `filepath.Walk` 根；`Walk` 回调里的绝对路径天然在 cwd 内 |
| `find` | 同 grep，`filepath.Rel` 基准改 `resolve` 后的根 |
| `repo_map` | `resolvePath(base)`（其 `os.Getwd()` 回退在 cwd 非空时改为用 cwd） |

> 实现方式：`builtins.All` 里每个 `toolXxx()` 改造成「返回同时填 `Run` 和 `RunInDir` 的 Tool」，
> `Run` = `RunInDir("", args)`（保证老调用点行为一致），`RunInDir` = 带路径解析的正体。
> 这样 `Manager.Call`（老路径）与 `CallInDir`（新路径）都可用，改动集中、可单测。

### 4.5 Agent 注入（loop.go 一行 + Clone 后一行）

```go
// loop.go:1077
out, err := a.Plugins.CallInDir(c.Tool, c.Args, a.WorkDir)

// server 侧每会话克隆后（handleWebRun / handleWebChat 建 clone 处）：
clone.WorkDir = filepath.Join(workspaceRoot, convID)   // 覆盖提示词 + 作为 cwd 实参
```

- 非 Web 入口（CLI/TUI/RPC）`WorkDir` 是进程 cwd 或空 → `CallInDir` 的 cwd 为空 → `resolvePath` 原样返回 → **行为完全不变**。
- 系统提示词自动出现「当前工作目录=`<root>/<convID>`」，模型用相对文件名 `read("report.pdf")` 即可命中。

### 4.6 为什么这样能保留并发

```
会话 A goroutine: CallInDir("read", {...}, "/ws/convA") → resolvePath → "/ws/convA/report.pdf"
会话 B goroutine: CallInDir("read", {...}, "/ws/convB") → resolvePath → "/ws/convB/report.pdf"
                  ↑ 各自参数，无共享可变状态，无锁，无 Chdir
```

进程 cwd 始终不变（服务器启动目录），所有路径都是绝对路径。**这是方案 3 与方案 1 的根本区别**。

---

## 五、目录布局与配置

### 目录（服务器磁盘）

```
<workspace_root>/                     # 默认 ~/.mizar/workspaces/（开箱可用）
├── <convID-1>/
│   ├── report.pdf
│   └── data.csv
└── <convID-2>/
    └── main.go
```

- 子目录名 = `conversation.ID`（现成唯一键），进路径前**白名单** `[A-Za-z0-9_-]`。
- 上传文件名：`filepath.Base` 净化 → 拒绝 `.`/`..`/空/绝对路径 → 拼 `<root>/<convID>/` →
  `filepath.Abs` 校验前缀。
- **会话删除联动清理**：`os.RemoveAll(<root>/<convID>)`，删前校验 convID 合法 + 路径在 root 内。

### 配置（webSettings 增字段）

```go
type webSettings struct {
    // ...existing...
    WorkspaceRoot string `json:"workspace_root,omitempty"` // 空 = 用默认 ~/.mizar/workspaces/
}
```

- **决策 2「开箱可用」**：空值不再代表「功能关闭」，而是**回退默认根目录** `~/.mizar/workspaces/`。
  `resolveWorkspaceRoot()` 返回 `配置值` 或 `filepath.Join(home, ".mizar", "workspaces")`。
- 前端可改：POST settings 时校验绝对路径、`filepath.Abs` 无 `..`、`MkdirAll` 可创建、非敏感系统目录。
- 存 `~/.mizar/webui.json`，重启恢复。改根目录后**已有会话目录不迁移**（前端提示：旧会话文件仍在原路径）。

---

## 六、上传协议与端点

新增 5 个端点（全 `webAuth` cookie 鉴权，三处同步登记）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/web/api/workspace/upload` | multipart：`conversation_id` + 多个 `file`，落 `<root>/<convID>/`，返回 `{name,size,saved_as}` |
| GET | `/web/api/workspace/files?conversation_id=` | 列该会话文件（名/大小/mtime） |
| GET | `/web/api/workspace/download?conversation_id=&name=` | 下载单文件（带 `Content-Disposition`） |
| DELETE | `/web/api/workspace/file?conversation_id=&name=` | 删单文件 |
| GET | `/web/api/workspace/root` | 返回当前生效根目录（前端展示/校验） |

**上传 handler 硬约束**：

```go
r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal)   // 总上限 100MB
mr := r.MultipartReader()                                 // 流式，别 ParseMultipartForm 全量进内存
// 每 part：convID 白名单 → filepath.Base 净化文件名 → filepath.Abs 校验在 <root>/<convID>/ 内
// → os.Lstat 拒绝已存在软链 → 单文件上限 50MB → 同目录 .tmp 临时文件 → 原子 rename
// → 不执行 / 不解压 / 不解析内容，0644 只存字节
```

- 同名文件覆盖，返回 `saved_as` 实际名。
- 鉴权后校验 `conversation_id` 属于当前可见会话（`getConv` 确认存在），别让任意字符串建目录。

**删除端点**：同上传的路径净化 + 边界校验；会话删除联动见 §五。

---

## 七、前端

- **设置面板**：工作空间根目录输入（留空=显示默认路径）、「当前生效路径」回显；改路径即时生效（下次上传/工具调用即按新根）。
- **会话工作空间面板**：文件列表（名/大小/时间）、拖拽或多选上传、下载、删除；上传进度条（fetch/XHR multipart，不掺 WS）。
- **上传后提示**：往对话插一条**普通文本**「已上传 report.pdf 到本会话工作空间」，让模型下轮知道有文件可读——
  **不要塞 `role:tool` 消息**（技能：裸 tool 消息进上下文会让上游 400），普通文本提示即可。
- 改前端前先 `-webui-out` 释放外部目录，改完即时生效、不必重编二进制。

---

## 八、安全清单（这个端点风险最高，逐条勾）

| 风险 | 防线 |
| --- | --- |
| 路径穿越 `../../etc/passwd` | `filepath.Base` + `filepath.Abs` 前缀校验 + convID 白名单 |
| 绝对路径 / 覆盖系统文件 | 拒绝绝对、根目录黑名单、dst 必须在 `<root>/<convID>/` 内 |
| **模型用 `../` 跳出会话工作空间** | `resolvePath` 边界校验：清洗后必须落在 cwd 内（§4.3）——这是 per-conv 隔离的核心 |
| 软链逃逸 | 上传 `os.Lstat` 拒已存在软链目标 |
| 大文件 / 磁盘打满 | `MaxBytesReader` 总上限 + 单文件上限 + 可选会话配额 |
| 可执行/脚本上传 | 只存字节，不执行、0644、不解压（zip 炸弹一律当字节） |
| 未鉴权访问 | 5 端点全 `webAuth`；convID 校验属于当前会话 |
| **联动清理误删** | 删目录前 convID 白名单 + `filepath.Abs` 校验在 root 内 + 只删单层 `<root>/<convID>` |
| cwd 污染 | **方案 3 不改进程 cwd**，`os.Getwd()` 永远不变，此项天然免疫 |

---

## 九、落地排期（每步可独立验证）

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **WS-0 配置 + 开箱默认** | `webSettings.WorkspaceRoot` + `resolveWorkspaceRoot()` 默认 `~/.mizar/workspaces/` + `/workspace/root` + settings 校验 | 空配置首次上传自动建目录；改路径后即时生效；重启恢复；非法路径被拒 |
| **WS-1 上传/列/下载/删 + 联动清理** | 5 端点 + routeTable + api.md；删会话联动 `RemoveAll`；安全单测（穿越/软链/绝对/大文件/误删） | curl 真传一个文件到隔离实例，列得出/下得回/删得掉；删会话目录消失；恶意文件名全 400 |
| **WS-2 方案 3 工具层（核心）** | `plugins.Tool.RunInDir` + `Manager.CallInDir` + `resolvePath` + 8 个内置工具接 cwd + loop.go:1077 注入 + Clone 覆盖 WorkDir | **CLI/TUI 全量测试不回归**（cwd 空=原行为）；单测：`resolvePath` 越界拒绝；`CallInDir("read", ..., "/ws/A")` 读到 A 的文件；**两会话并发各读各的、互不串** |
| **WS-3 前端** | 设置项 + 工作空间面板 + 拖拽上传 + 上传提示 | 浏览器真上传 → 对话让模型 `read("文件名")` → 得到正确内容（真上游端到端） |
| **WS-4 打磨** | 上传元数据、配额、OpenAPI 生成、路径迁移提示、移动端 | `-gen-openapi` 端点齐全；`go test ./...` 不回归（忽略已知 context 包 2 环境假红） |

**验证姿势**（照技能）：`HOME=/tmp/mzt /tmp/mizar-test -serve -addr :3201 ...` 起隔离实例，
从 `/tmp/mzt/.mizar/config.json` 读随机密码登录，`-c jar` 存 cookie（父目录要先建，否则静默不写→全 401），
curl 上传 → `/web/api/run` 让 Agent `read` 文件。**WS-2 必须真起两个并发会话各传同名文件，
证明读到的内容分别是各自的**（这是方案 3 的立身之本，单测 + 真跑都要有）。

---

## 十、明确不做（避免过度设计）

- **不用 `os.Chdir`、不加全局串行锁、不降级并发**（方案 1 作废）——方案 3 的灵魂就是无全局状态。
- **不改 `Tool.Run` 签名、不动 JS 插件 ABI**：插件 tool_* 保持走 `Run`，一期不接工作空间；
  若二期要支持，给 goja VM 注入 `MIZAR_CWD` 全局并约定插件改用它，属独立改动。
- **斜杠命令 / RPC 不接 cwd**：控制面工具，不做文件操作。
- **不做服务端解压/预览/杀毒**：只搬字节，内容交给 Agent 工具。
- **不做多用户 RBAC**：Web 当前单用户 admin，上传与会话绑定即可。
- **不做云存储**：本地磁盘，符合零配置定位。
- **不把上传内容塞进 LLM 上下文**：只插一条可见文本提示。
- **暂不改 session 目录归属**：`~/.mizar/sessions` 与工作空间是两回事，不合并。

---

## 已决策 / 待确认

- ✅ 方案 3（保留并发 + per-conv 注入）、✅ 开箱可用 + 前端可改路径、✅ 联动清理。
- 待定（不阻塞开工）：单文件/会话配额默认值；前端拖拽还是仅点选；改根目录后旧会话文件是否提供「迁移」按钮（规划默认不迁移，仅提示）。
