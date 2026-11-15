# 功能兑现审计：声称已做/要做，实际缺失或半成品

> 审计日期：2026-11-15
> 方法：全仓扫描 TODO/FIXME、`go run golang.org/x/tools/cmd/deadcode`（123 个不可达符号）、配置字段读写比对、注释声明 vs 调用点交叉验证、临时测试实证。
> 结论：**发现 15 处「文档/注释/配置声称有、实际没接线或半成品」**，按严重度分级如下。
>
> **修复进度（2026-11-15 当天）**：#1 #2 #3 #5 #6 #7 #8 #10 已修并补测试；#9 #11 #12(部分) #13 已清理死代码；#4（skill_evolution）随 #1 一并接线；#14 #15 保留说明（见文末「修复记录」）。

---

## 🔴 严重（功能宣传有、实际完全不工作）

### 1. ✅已修 BackgroundReview 后台自学习从未启动 —— 「自动沉淀技能/记忆」完全失效

| 项 | 内容 |
|---|---|
| 声称 | `internal/agent/background_review.go` 头注释：「每轮 LLM 响应后，在后台 goroutine 中 fork 一个辅助 agent… 写入 `~/.mizar/skills/` 或 `~/.mizar/memory.md`」；README 也曾宣传「技能自动沉淀」 |
| 实际 | `main.go:807` 只 `NewBackgroundReview(...)` 赋给 `a.BgReview`，**`Start()` 全仓零调用**。`Advance()` 里 `triggerReview` 首行 `if !br.running.Load() { return }` → running 初值 false，永远直接返回 |
| 实证 | 临时测试：不调 Start 时 `Advance()` × 10 轮，AuxLLM 调用次数 = **0**；调 Start 后 1 轮即触发 |
| 附带 | `BackgroundReviewConfig.MemoryFile` 字段**从未被赋值**（`grep "MemoryFile:"` 空）→ 即使启动，写 memory 那半也不生效 |
| 修复 | 在 `Agent.Run` 开头调 `a.BgReview.Start()`、结尾 `Stop()`；或改 `Advance` 不再依赖 running 标志。另需补 `MemoryFile` 赋值 |

### 2. ✅已修 技能禁用名单被当成工具禁用名单 —— 禁用技能实际无效

| 项 | 内容 |
|---|---|
| 声称 | `skills/stats.go` Report：「回复『禁用 <技能名>』可将其禁用」；`/skills` 周报每周提示 |
| 实际 | `main.go:527` `pm.SetDisabledTools(stats.DisabledNames())` —— `SetDisabledTools` 语义是**屏蔽工具注册表**（`internal/plugins/loader.go:578`），传进去的却是**技能名**。且 `internal/skills/skills.go` 渲染时**完全不看 Disabled 名单** |
| 后果 | ① 禁用一个技能 → 技能仍然出现在系统提示里（索引注入）；② 若恰好存在同名工具（如 `memory` 技能 vs `memory` 工具），会把**工具**屏蔽掉；③ 周报里「回复禁用 xxx」**没有任何代码解析这句话**（全仓 grep 无实现） |
| 修复 | 技能渲染处加 Disabled 过滤；`DisabledNames` 不要传给 `SetDisabledTools`；补「禁用 xxx」的输入解析（或在周报文案里删掉这句话） |

---

## 🟡 中等（功能半成品 / 松耦合漏接线）

### 3. ✅已修 WebUI 缓存未命中提示：后端空操作 + 前端无开关

- `webstore.go:74` 有 `ShowCacheMiss` 字段，`webui.go:900` 调 `setCacheMissEnabled(v)` —— 但该函数体是 `_ = v`（**空操作**，注释自承「Server 模式下此处暂为空操作」）。
- 前端 `index.html` / `app.js` 里**根本没有这个开关**（grep 计数 0）→ 用户无法设置，设了也没效果。
- TUI 侧是好的（`tui.go:1628` 读 `cfg.CacheMissNotice` 并 `NewCacheMissDetector`）。

### 4. ✅已修 `skill_evolution` 配置项从未被读取（随 #1 接线）

- `config.go:34` 定义 `SkillEvolution *bool`（注释「技能自动沉淀（nil=默认开启）」）+ `SkillEvolutionEnabled()` 方法。
- 全仓 `SkillEvolutionEnabled()` **零调用**。配合第 1 条：不但开关没接线，连它想控制的功能本身都没启动。

### 5. ✅已修 GoalGate 的「重试上限 / 失败指纹去重」只有字段没有逻辑

- `GoalGate.MaxRetries`（`goal.go:98`）：只在解析/序列化里被赋值，**从未参与任何判断**（`defaultGateMaxRetry` 常量也无人用）。
- `GoalGate.LastFailedFP` + `WorkspaceFP(cwd)`（`goal.go:155`）：`WorkspaceFP` 被 deadcode 判定**不可达**，`LastFailedFP` 从未被赋值。
- 注释暗示「同一工作区状态失败可去重/限次」，实际门禁每次都无条件重跑。

### 6. ✅已修 子代理拿不到 Compactor → 长任务子代理不压缩上下文

- `subagent.go:4` 头注释：「子 agent 跑原生 Run()——自带工具解析/执行/**纠错/压缩**/步数预算」。
- 实际 `runSubagent` 只设了 `child.System / MaxSteps / WorkDir / Permission / OnStep / OnLLMStream`，**没有 `child.Compactor`、`child.Tuner`**（`New()` 里 Compactor 默认 nil）。
- 后果：子代理跑长任务时 `if a.Compactor != nil` 恒假 → 永不压缩 → 撞上游上下文上限直接失败。`Clone()` 是有 Compactor 的，**子代理路径漏了**。

### 7. ✅已修 `agentPool.drop` 从未调用 → 删会话不释放 Agent 实例

- `agentpool.go:69` 注释「会话删除时释放其 Agent（避免长期累积）」。
- 全仓 `pool.drop(` **零调用**（`webui.go:387` 只 `st.delete(id)` 删存储，没通知 pool）。
- 后果：`byConv` map 随删除的会话只增不减（每个实例含 Initial 历史，内存持续增长）。`count()` 也是死代码（只能靠测试观察到）。

### 8. ✅已修 子代理句柄 map 只增不减

- `subagent.go` 的 `sm.subs[h.ID] = h` 之后**没有任何 delete**（grep 空）。长跑进程里每派一个子任务就永久留一个句柄（含完整 Result 文本）。

---

## 🔵 轻微（死代码 / 文档措辞不实）

| # | 项 | 说明 |
|---|---|---|
| 9 | `evtLog.nextSeq` | 注释「前端可用来判断是否漏了事件」，但 `handleWebEvents` 用的是 `since()`，`nextSeq` 死代码 |
| 10 | `Server.Shutdown` | 实现了优雅关闭，但**全仓无调用**；也没有 `signal.Notify` 处理 SIGINT/SIGTERM → Ctrl+C 直接杀进程，不 flush 会话/不关 DB |
| 11 | `Agent.toolManager` | tool_index.go:70，死代码 |
| 12 | `CacheImpact.String/CacheWarning`、`AllHookSpecs`、`StreamTextExtractor.IsTool`、`extractAssistantText` | 均为 deadcode 报告中的不可达符号 |
| 13 | `builtins.EnsureSessionsDir` | sessionsearch 里新加的工具函数，实际未被调用（`Open` 内部自己建目录） |
| 14 | `config.ProbeContextWindow` / `ProbeThinking` / `RunWizard` | 探测/向导函数存在但主流程未接线（`-gen-*` 之外的入口未用） |
| 15 | `skill_stats` 把「工具调用」当「技能使用」 | `uses` 表里混着 `bash/grep/edit`（工具名）与 `handoff/database`（技能名）——因为 `SetOnToolCall` 记的是工具名。周报「技能使用排行」实际是「工具调用排行」 |

---

## 建议修复优先级

| 优先级 | 项 | 工作量 | 理由 |
|---|---|---|---|
| 1 | #1 BgReview 未启动（+MemoryFile） | ~10 行 | 宣传的自学习功能等于不存在 |
| 2 | #2 技能禁用串到工具禁用 | ~40 行 | 用户按提示操作会得到反效果（还可能屏蔽工具） |
| 3 | #6 子代理缺 Compactor/Tuner | ~5 行 | 长任务子代理会撞上下文上限直接失败 |
| 4 | #7/#8 句柄/map 只增不减 | ~20 行 | 长跑进程内存泄漏 |
| 5 | #3 WebUI cache 提示 | ~30 行 | 设置项无效，误导 |
| 6 | #10 Shutdown + 信号 | ~20 行 | 优雅退出（flush 会话、关 DB） |
| 7 | #4/#5 无效配置与字段 | ~15 行 | 要么接线要么删掉，避免下次误以为有 |
| 8 | #9/#11~15 死代码 | ~30 行 | 清理，降低误读 |

---

## 附：本次审计的验证手段

```bash
# 死代码
go run golang.org/x/tools/cmd/deadcode@latest ./... | wc -l    # 123 处

# 配置字段读写比对
grep -oE 'json:"[a-z_]+' internal/config/config.go | ...        # 逐项查读取点

# 关键结论的实证测试（已删除，可复现）
# 不调 Start() → Advance()×10 → AuxLLM 调用 0 次
```


---

## 修复记录（2026-11-15）

| # | commit | 要点 |
|---|---|---|
| #1 + #4 | `96a40cf` | BgReview 去掉 Start 依赖（改看配置）；新增 `OnMemory` 回调写结构化 MEMORY.md；`skill_evolution` 接线 |
| #2 | `96a40cf` | `skills.Manager.disabled` 过滤 All/Render；`builtins.SetDisabledSkills` 让 skill 工具拒读；新增 `/skills` 命令；周报文案改指 `/skills disable` |
| #6 | `1bb4708` | 子代理注入 Compactor / Tuner（Tuner 用新增 `Clone()`，避免与主代理共用计数） |
| #7 | `1bb4708` | 会话 DELETE 接 `pool.drop` |
| #8 | `1bb4708` | `maxRetainedSubagents=200` + `pruneLocked`（只淘汰终态、运行中保留） |
| #3 | `53f5030` | `setCacheMissEnabled` 真接线 + 前端下拉 + `agent.SetCacheMissNoticeEnabled` |
| #10 | `53f5030` | `-serve` 注册 SIGINT/SIGTERM → 5s 优雅关闭 + 关索引 |
| #5 | `f29fffe` | `RunGateFull` 维护 Attempts/LastFailedFP；耗尽后停止自动续跑 |
| #9/#11/#13 | 本次 | 删 `evtLog.nextSeq`、`Agent.toolManager`、`EnsureSessionsDir` 死代码 |

保留说明：
- **#12** 部分符号（`AllHookSpecs` / `extractAssistantText` / `CacheImpact.String`）**被测试或架构文档引用**，不是死代码，保留。
- **#14** `ProbeContextWindow` / `ProbeThinking` / `RunWizard`：为未来入口预留，暂不再接线（不误导用户即可）。
- **#15** 技能统计与工具调用混记：**刻意保留**（周报的「工具调用排行」本身有参考价值），已在代码注释里说明语义。
