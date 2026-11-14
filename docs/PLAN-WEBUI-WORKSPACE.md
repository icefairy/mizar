# Mizar Web UI · 文件上传与工作空间规划

> v1.0 ｜ 2026-11-14 ｜ 目标仓库 `/data/codes/mizar`（Go 单二进制，Web UI 走 `//go:embed`）
>
> 需求：Web UI 是 **B/S 架构**，客户端（浏览器）要能把**本地文件上传到运行 Mizar 的服务器磁盘**；
> 在设置里配置一个「工作空间根目录」，上传的文件**按会话 ID 分目录**存放，形成网页版「工作空间」概念，
> 让 Agent 能读到、能处理这些文件。

⚠️ 本文是**规划**，不含实现。当前版本 `v0.8.6`。落地前先读 `mizar-development` 技能 +
`docs/api.md` + `internal/server/webui.go`，本规划里的路径/结构以 2026-11-14 代码为准。

---

## 一、先说结论（含一个必须先拍板的地基矛盾）

| 结论 | 内容 |
| --- | --- |
| **上传本身不难，难在「Agent 能不能读到」** | 加一个 `POST /web/api/upload`（multipart，cookie 鉴权）把文件落到服务器磁盘，是 1 天量的活。但**只把文件按会话分目录存好，Agent 默认读不到**——见下方矛盾 |
| ⚠️ **核心矛盾：工具全部共享进程 cwd** | `internal/builtins/files.go` 的 `read`/`write`/`grep`/`find`/`ls` 全用**相对路径**直接 `os.Stat`/`os.WriteFile`，`bash` 用 `shell.Command`（`exec.Command` 默认继承进程 cwd，**无 `cmd.Dir`**）。工具注册表在 `Agent.Clone()` 里是**共享指针**（clone.go），所有会话用同一份工具实例。因此**没有任何 per-call / per-conv 的 cwd 注入点** |
| ⚠️ **`a.WorkDir` 只是提示词，不是执行目录** | `internal/agent/loop.go:455-460` 把 `WorkDir` 注入系统提示词「告诉模型你在哪」，**不改变工具实际执行目录**。改它不会让 `read("a.txt")` 读到别处 |
| **推荐方案：每会话目录 + 运行前 `os.Chdir`** | 工作空间根 `<root>/<convID>/`，每次 `/web/api/run`、`/web/api/chat` 执行**前**把进程 cwd 切到该会话目录、执行后恢复。因为已有「同会话串行锁 + 跨会话全局 runMu」（`server.go` runMu/runLocks），**同一时刻只有一个会话在跑，进程级 Chdir 不会互相踩**。这是复用现有并发模型、改动最小、真正闭环的做法 |
| **不建议为这需求重写工具层** | 方案 B（给每个工具加 cwd 参数 / 每个会话独立工具注册表）要改 `internal/builtins` 全部工具 + `plugins.Manager` + Clone 语义，横切十几个文件，且插件工具无法统一注入。复杂度与收益不成正比 |
| **「设置里配根目录」= `webSettings` 加一个字段** | `webSettings`（`webstore.go`）已持久化到 `~/.mizar/webui.json`，加 `workspace_root string` 即可，复用现有 `/web/api/settings` GET/POST，重启由 `ApplyStoredSettings` 恢复路径天然继承 |
| **上传文件与会话强绑定** | 上传必须带 `conversation_id`，落 `<root>/<convID>/<安全文件名>`；会话删除时可选联动清理目录（默认保留，避免误删） |
| **安全是硬约束** | 路径穿越（文件名 `../`）、绝对路径、软链、大文件、文件类型——上传端点必须全部挡住。这是把「浏览器任意文件」写到服务器磁盘，RCE/覆盖风险最高的一个入口 |

> **一句话**：这需求的真实工作量 80% 不在「上传」，在「让 per-conv 目录成为 Agent 的 cwd 且不出并发/安全问题」。
> 拍板方案 A（Chdir）后再动手，别先写上传接口。

---

## 二、需求拆解（三块，别混为一谈）

1. **上传能力**：浏览器选文件 → `POST /web/api/upload` → 服务器落盘。要能一次多选、显示进度/结果、失败可重试。
2. **工作空间配置**：设置面板加「工作空间根目录」输入框（服务端绝对路径，如 `/data/mizar-workspaces`），
   存 `~/.mizar/webui.json`，进程启动时校验/创建目录。
3. **按会话隔离 + Agent 可读**：`<root>/<convID>/` 存该会话所有上传文件；**切 cwd** 让 `read`/`bash`/`ls`
   直接以会话目录为基准，模型用相对文件名即可读到；前端展示「本会话工作空间文件列表」，可下载/删除。

---

## 三、现状地基（写任何代码前先认这些）

| 地基 | 位置 | 对本次的约束 |
| --- | --- | --- |
| Web 端点统一 cookie 鉴权 `webAuth` | `webui.go:265` | 上传/文件列表/下载/删除全部要过它，未登录 401 |
| 端点注册 = `mux.HandleFunc` + `routeTable` + `docs/api.md` **三处同步** | `webui.go:232-261`、`routemeta.go` | 加 4~5 个端点，`/admin/routes` 与 `-gen-openapi` 才会不漏报 |
| `webSettings` 持久化到 `~/.mizar/webui.json`，`ApplyStoredSettings` 重启恢复 | `webstore.go`、`webui.go:878` | 根目录配置直接加字段，零新增持久化机制 |
| 会话 `conversation.ID` 已是唯一键、有 `CreatedAt` | `webstore.go` | 直接拿它做子目录名，不用另造 ID |
| `webStore` 有 `mu` 锁 + 指纹检测外部修改 | `webstore.go` | 文件元数据若也存 JSON，复用这套；或纯靠磁盘扫描 |
| 同会话串行锁 `runLocks[convID]` + 全局 `runMu`/`runCount` | `server.go:34-44` | **方案 A 成立的前提**：同一时刻仅一个 Run 在跑，Chdir 安全 |
| `handleWebRun` / `handleWebChat` 注入历史 → `agent.Run` / `AIChatStream` | `webui.go:434,686` | Chdir 的切入/恢复点就在这两个 handler 的执行边界 |
| `read`/`write`/`grep`/`find`/`ls` 相对路径、无 cwd 参数 | `files.go` | 不能靠传参改目录，只能靠进程 cwd |
| `bash` = `shell.Command`（`exec.Command`，继承进程 cwd） | `builtins.go:70`、`shell.go:283` | 同 |
| `a.WorkDir` 仅注入系统提示词 | `loop.go:455-460` | 可**顺带**把会话目录写进 `WorkDir` 让模型知道，但真正生效靠 Chdir |
| Agent 每会话 `Clone()`，`WorkDir`/`Plugins` 共享复制 | `clone.go` | 系统提示词可 per-conv 覆盖 WorkDir（改 Clone 后的实例），工具不行 |
| 前端 `//go:embed`，改完可能要重编（或走 `web.frontend_dir` 外部化） | 技能 | 加上传 UI 前先 `-webui-out` 释放外部目录，改完即时生效不必重编 |

---

## 四、数据模型与目录布局

### 目录（服务器磁盘）

```
<workspace_root>/                     # 设置里配，如 /data/mizar-workspaces
├── <convID-1>/                      # 会话 A 的独立工作空间
│   ├── report.pdf                   # 用户上传，保持原文件名（净化后）
│   ├── data.csv
│   └── .mizar-uploads.json          # 可选：上传元数据（原名/大小/时间/mime），纯磁盘扫描可不存
├── <convID-2>/
│   └── main.go
└── ...
```

- 子目录名 = `conversation.ID`（现成唯一键）。**会话 ID 必须白名单校验**（只允许 `[A-Za-z0-9_-]`），
  它要进文件路径，绝不能直接拼未经校验的字符串。
- 上传文件名净化：`filepath.Base` 去掉任何目录成分 → 拒绝 `.`/`..`/空 → 白名单字符 → 拒绝绝对路径。
  **先 `filepath.Clean` + `Base`，再拼，再 `filepath.Abs` 校验落在 `<root>/<convID>/` 之内**（防 `..` 穿越）。

### 配置（webSettings 增字段）

```go
type webSettings struct {
    // ...existing...
    WorkspaceRoot string `json:"workspace_root,omitempty"` // 服务器绝对路径，空=禁用上传/工作空间
}
```

- 空 = 功能关闭（上传接口返回 400「未配置工作空间」，前端隐藏上传按钮）。**默认关闭**，避免没人配就往
  `~/.mizar` 或进程 cwd 乱写。
- POST settings 时校验：必须是绝对路径、转 `filepath.Abs` 后不含 `..`、`os.MkdirAll(root, 0o700)` 可创建、
  且**不是** `/`、`/etc`、`/root/.mizar` 等敏感路径（黑名单 + 必须位于某个允许基目录下更稳）。
- 存进 `~/.mizar/webui.json`，重启 `ApplyStoredSettings` 读回。

---

## 五、上传协议与端点

新增端点（全部 `webAuth` cookie 鉴权，三处同步登记）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/web/api/workspace/upload` | `multipart/form-data`：`conversation_id` + 多个 `file`。落 `<root>/<convID>/`，返回每个文件的 `{name,size,saved_as}` |
| GET | `/web/api/workspace/files?conversation_id=` | 列该会话目录下的文件（名称/大小/修改时间），不返回内容 |
| GET | `/web/api/workspace/download?conversation_id=&name=` | 下载单个文件（带 `Content-Disposition`，防目录穿越同上传） |
| DELETE | `/web/api/workspace/file?conversation_id=&name=` | 删单个文件 |
| GET | `/web/api/workspace/root` | 返回当前配置的根目录（前端展示 / 校验用） |

**上传 handler 硬约束**：

```go
r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal)   // 单次总上限，如 100MB
mr := r.MultipartReader()                                 // 流式，别 ParseMultipartForm 全量进内存
// 每个 part：
//   1. convID 白名单
//   2. name := filepath.Base(part.FileName())；净化 + 拒绝穿越
//   3. dst := filepath.Join(root, convID, name)；filepath.Abs 后必须仍在 <root>/<convID>/ 内
//   4. 若 dst 已存在是软链 → 拒绝（os.Lstat）；单文件上限（如 50MB）
//   5. io.Copy 到临时文件（同目录 .tmp）→ 原子 rename，避免半文件
//   6. 不做任何执行/解压/内容解析（zip 炸弹、可执行文件一律只当字节存）
```

- **不自动解压、不扫描内容、不执行**——服务端只搬运字节。要看内容由 Agent 的 `read` 工具去读。
- 同名文件：默认覆盖（提示），或加时间戳后缀——规划选「覆盖 + 返回 `saved_as` 实际名」，语义最简单。
- 鉴权后还要校验 `conversation_id` 属于当前用户可见会话（Web 目前单用户 admin，但仍 `getConv` 确认存在，
  别让任意字符串建目录）。

---

## 六、让 Agent 真正读到（方案 A：运行前 Chdir）

这是闭环的关键，**不是可选优化**。

### 执行边界

`handleWebRun` / `handleWebChat` 在拿到会话、抢到 runLock **之后**、调用 `agent.Run`/`AIChatStream` **之前**：

```go
wsDir := filepath.Join(workspaceRoot, convID)   // 已确保存在
restore, err := chdirFor(wsDir)                  // os.Chdir + 返回恢复闭包
if err != nil { /* 410/500，回退到进程 cwd 或禁用 */ }
defer restore()                                  // 务必恢复，别污染后续请求
```

- `chdirFor` 用 `os.Getwd()` 存旧值 → `os.Chdir(wsDir)` → 返回 `func(){ os.Chdir(old) }`。
- **为什么安全**：已有 `runLocks[convID]` 同会话串行 + 全局 `runMu`（非 Web 入口）/`runCount` 并发上限。
  但注意——**跨会话是真并发**（技能明确：「不同 session_key 并发是产品正常形态」）。两个会话同时跑时，
  进程级 `os.Chdir` 会互相覆盖 cwd！这是方案 A 的**致命细节**，必须处理 ↓

### ⚠️ 并发下的 Chdir 冲突 —— 必须拍板

进程只有一个 cwd，跨会话并发时 A 切到 `convA`、B 切到 `convB`，谁后切谁生效，另一个的工具就读错目录。
三条路，**推荐第 1 条**：

1. **工作空间模式 = 全局降级为串行**（推荐）：一旦 `workspace_root` 启用，Web 的 Run/Chat 走**一把全局锁**
   （复用/新建 `workspaceMu`），保证同一时刻只有一个会话在执行、独占 cwd。代价：启用工作空间后
   失去跨会话并发（但换来「每个会话文件严格隔离 + Agent 直接可读」，符合个人 B/S 工具定位）。
   前端可提示「工作空间模式下会话串行执行」。**改动小、语义清晰、可验证**。
2. **每会话独立子进程/独立 cwd**：为每个会话 fork 一个 worker 进程（各自 cwd），架构改动大，不做。
3. **方案 B 重写工具层**（每工具注入 cwd）：横切 `internal/builtins` + 插件工具无法统一，不做。

> 若坚持「跨会话并发 + per-conv 工作空间」两者都要，只能走方案 B，排期翻倍。
> 规划默认选 **1**：工作空间开启时 Web 执行串行，且 `workspaceMu` 与现有 `runMu` 合一，别造两套锁。

### 顺带：把会话目录告诉模型

`agent.Clone()` 后、Run 前，把该实例的 `WorkDir` 覆盖成 `wsDir`（`WorkDir` 是可写字段，Clone 复制后可改），
让系统提示词里出现「当前工作目录 = `<wsDir>`」。这样模型知道该用**相对文件名**还是绝对路径去 `read`，
与 Chdir 形成双重保障。`ls` 工具配合文件列表接口，让模型能先看到有哪些文件。

---

## 七、前端（Web UI）

- **设置面板**：加「工作空间根目录」输入 + 「已启用/未配置」状态；空时上传相关 UI 隐藏。
- **会话主区**：加一个「工作空间」折叠面板/标签：
  - 文件列表（名称/大小/时间），支持**拖拽或多选上传**、下载、删除；
  - 上传中进度条（用 `XMLHttpRequest`/fetch 的 multipart，WS 不必掺和）；
  - 新文件上传后，往对话里插入一条**可见的系统提示**（`kind=tool` 或直接一条 user 可见说明）
    「已上传 report.pdf 到本会话工作空间」，让模型下一轮知道有文件可读——**但不要把它塞进 LLM 上下文
    当 tool 消息**（技能：裸 tool 消息会 400），用普通文本提示即可。
- 改前端前先 `-webui-out` 释放到外部目录（技能要求），改完即时生效、不必重编二进制。

---

## 八、安全清单（这个端点风险最高，逐条勾）

| 风险 | 防线 |
| --- | --- |
| 路径穿越 `../../etc/passwd` | `filepath.Base` + `filepath.Abs` 校验前缀 + convID 白名单 |
| 绝对路径 / 覆盖系统文件 | 拒绝绝对、根目录黑名单、dst 必须在 `<root>/<convID>/` 内 |
| 软链逃逸 | `os.Lstat` 拒绝已存在的软链目标 |
| 大文件 / 磁盘打满 | `MaxBytesReader` 总上限 + 单文件上限 + 可选会话配额 |
| 可执行/脚本上传 | 只存字节，不执行、不设可执行位（0644）、不解压 |
| 未鉴权访问 | 全部端点 `webAuth`；`convID` 校验属于当前会话 |
| 会话 ID 注入 | 白名单 `[A-Za-z0-9_-]`，进路径前一律校验 |
| cwd 污染 | `defer restore()`；工作空间模式串行化（§六） |
| 删除误删 | 删单个文件需二次确认（前端）；会话删除**默认不**联动删目录 |

---

## 九、落地排期（每步可独立验证）

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **WS-0 配置 + 目录骨架** | `webSettings.WorkspaceRoot` + settings 校验/创建 + `/web/api/workspace/root` | GET/POST settings 往返；空=禁用；非法路径被拒；重启恢复 |
| **WS-1 上传/列/下载/删** | 4 个端点 + routeTable + api.md；安全单测（穿越/软链/大文件/绝对） | curl 真传一个文件到隔离实例，列得出、下得回、删得掉；恶意文件名全部 400 |
| **WS-2 Chdir 闭环** | `chdirFor` + 工作空间模式串行锁 + Clone 后覆盖 WorkDir | 隔离实例真跑 Agent：`read("上传的文件名")` 能读到内容；两会话并发时目录不串 |
| **WS-3 前端** | 设置项 + 文件面板 + 拖拽上传 + 上传提示插入对话 | 浏览器端真实上传→对话让模型读文件→得到正确内容（真上游端到端） |
| **WS-4 打磨** | 上传元数据、配额、会话删除策略、OpenAPI 生成、移动端适配 | `-gen-openapi` 端点齐全；`go test ./...` 不回归（忽略已知 context 包 2 环境假红） |

**验证姿势**（照技能）：`HOME=/tmp/mzt /tmp/mizar-test -serve -addr :3201 ...` 起隔离实例，
从 `/tmp/mzt/.mizar/config.json` 读随机密码登录，`-c jar` 存 cookie（父目录要先建，否则静默不写→全 401），
再 curl 上传 + `/web/api/run` 让 Agent 读文件。**必须真跑一轮**证明 Chdir 生效，单测证不了回调/进程 cwd。

---

## 十、明确不做（避免过度设计）

- **不重写工具层注入 cwd**（方案 B），除非明确要「跨会话并发 + per-conv 目录」两者兼得。
- **不做服务端解压/预览/杀毒扫描**：只搬运字节，内容交给 Agent 工具。
- **不做多用户文件权限**：当前 Web 单用户 admin，上传与会话绑定即可，不引入 RBAC。
- **不做云存储/对象存储**：本地磁盘目录，符合零配置部署定位。
- **不把上传内容塞进 LLM 上下文**：只插入一条可见文本提示，避免 token 爆炸与 tool 消息协议问题。

---

## 待用户拍板

1. **§六的并发取舍**：启用工作空间时**降级为全局串行**（推荐，改动小），还是必须保留跨会话并发（→ 方案 B，排期翻倍）？
2. 工作空间**默认关闭**（空配置），还是给个默认根目录（如 `~/.mizar/workspaces`）开箱即用？
3. 会话删除时，工作空间目录**保留**（推荐）还是联动清理？
