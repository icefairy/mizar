---
name: memory
description: Use when 需要跨会话记住事实、检索过往经验、或沉淀知识时——本地记忆读写。
---

# 本地记忆

通过 `memory.ts` 插件读写**本地 SQLite** 记忆库（`~/.mizar/memory/mem.db`）。**零外部服务、零依赖**，随二进制发布即用。

## 何时用

- **写入**：用户告知了值得长期保留的事实（偏好、约定、环境信息）；完成一段工作后提炼出可复用的经验。
- **检索**：开始任务前查历史经验；遇到似曾相识的问题时找之前怎么解决的。
- **管理**：查看/删除/清空记忆。

## 存储与检索原理

- **存储**：SQLite 单文件 `~/.mizar/memory/mem.db`，按 `namespace` 隔离。
- **分词**：中文用**二元组（bigram）**切分 + 单字；英文/数字按整词；过滤停用词。
- **检索**：对记忆与查询分别分词，用「**覆盖率 75% + 精确度 25%**」加权打分，按 token 长度加权（长词信息量大），返回相似度最高的若干条（**默认 3 条**）。

## 工具一览

| 工具 | 用途 | 关键参数 |
|------|------|---------|
| `mem_write` | 写入记忆 | `content`（必填）、`tags`、`memory_type` |
| `mem_search` | 检索 | `query`（必填）、`limit`（默认 3）、`min_score` |
| `mem_list` | 列出最近 | `limit`、`memory_type` |
| `mem_get` | 查看单条 | `id` |
| `mem_delete` | 删除 | `id` |
| `mem_stats` | 统计 | — |
| `mem_clear` | 清空（谨慎）| `namespace`、`all` |

斜杠命令：`/mem store|search|list|get|delete|stats|clear|config`。

## 两种记忆后端（二选一）

Mizar 内置**两个**记忆插件，导出同名 `mem_*` 工具，**同时只应启用一个**（同时启用会工具名冲突，后加载者生效）：

| 插件 | 后端 | 特点 | 默认 |
|------|------|------|------|
| `memory.ts` | 本地 SQLite | 零外部服务、零依赖，开箱即用 | ✅ 启用 |
| `memory_hmem.ts` | [HMEM Server](https://github.com/icefairy/hmem_hermes_agent) | 向量+关键词混合检索、反思引擎、知识图谱，**多 agent 共用一套记忆** | ⛔ 禁用 |

**切换到 HMEM 版**（需先自建 HMEM 服务，默认 `http://localhost:8000`）：

```
/disable_plugin memory.ts
/enable_plugin memory_hmem.ts
```

配置 HMEM 连接（或用环境变量 `MIZAR_HMEM_API_URL` / `MIZAR_HMEM_API_KEY` / `MIZAR_HMEM_NAMESPACE`）：
`/mem config set apiUrl http://localhost:8000` · `/mem config set namespace 我的空间`

> 下文工具表以本地 SQLite 版为准；HMEM 版工具名相同，另有 `mem_reflect`（归纳洞察）、`mem_models`（洞察/心智模型）、`mem_graph`（知识图谱）、`mem_namespaces`（命名空间）。

## 要点

- **写「可复用的事实」，不写流水账**：`用户偏好结论先行` ✓，`今天下午改了 X 文件` ✗。
- **命名空间隔离**：不同项目/领域用不同 `namespace`（环境变量 `MIZAR_MEM_NAMESPACE` 或参数），避免串味。
- **先搜再写**：写入前 `mem_search` 一下，避免重复。
- **检索默认 3 条**：够聚焦；需要更多时显式传 `limit`。
- **`mem_clear` 是破坏性操作**：清空前务必确认命名空间。

## 想换别的记忆后端（如与其它 agent 共用 HMEM）

内置的是本地 SQLite 版。若要与其它 agent **共用一套记忆**（如 HMEM Server），
用 `/disable_plugin memory.ts` 禁用内置版，再放入你自己的记忆插件即可
（`/list_plugins` 查看、`/enable_plugin` 启用、`/reload_plugins` 重载）。
