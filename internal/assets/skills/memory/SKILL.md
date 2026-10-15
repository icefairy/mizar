---
name: memory
description: Use when 需要跨会话记住事实、检索过往经验、或沉淀洞察时——全局记忆读写。
---

# 全局记忆（HMEM）

通过 `memory.ts` 插件连接 **HMEM Server**，实现跨会话、跨项目的长期记忆。

## 何时用

- **写入**：用户告知了值得长期保留的事实（偏好、约定、环境信息）；完成一段工作后提炼出可复用的经验/洞察。
- **检索**：开始任务前查历史经验；遇到似曾相识的问题时找之前怎么解决的。
- **沉淀**：把「观察 → 经验 → 洞察」逐层提炼，形成结构化知识。

## 记忆层级

```
observation（原始观察）→ experience（结构化经验：动作+结果）→ insight（洞察）/ mental_model（心智模型）
```

## 配置

连接 HMEM Server，配置来源（优先级从高到低）：
- 环境变量：`MIZAR_HMEM_API_URL` / `MIZAR_HMEM_API_KEY` / `MIZAR_HMEM_NAMESPACE`
- 配置文件（通过 `/mem config set <key> <value>` 写入）

## 工具一览

| 工具 | 用途 |
|------|------|
| `mem_write` | 写入一条记忆（可带层级/动作/命名空间）|
| `mem_search` | 语义 + 关键词混合检索 |
| `mem_list` | 列出最近记忆 |
| `mem_get` | 取单条 |
| `mem_delete` | 删除 |
| `mem_stats` | 统计 |
| `mem_reflect` | 触发反思（把经验提炼为洞察）|
| `mem_graph` | 查看记忆关联图 |
| `mem_namespaces` | 列出命名空间 |
| `mem_models` | 列出可用模型 |

## 要点

- **写「可复用的事实」，不写流水账**：`用户偏好结论先行` ✓，`今天下午改了 X 文件` ✗。
- **命名空间隔离**：不同项目/领域用不同 `namespace`，检索时也按 namespace 过滤，避免串味。
- **先搜再写**：写入前 `mem_search` 一下，避免重复；有旧的用 `mem_write` 覆盖式更新。
- **经验要带动作与结果**：`experience` 类型填写 action（做了什么）与 outcome（结果如何），比纯描述更有复用价值。
