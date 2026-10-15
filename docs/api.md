# 开阳（Mizar）Server API 参考

> 适用版本：**v0.4.6+** · 端点均在 `--serve` 模式提供。
> 本文所有示例响应均为**实测输出**（本机 `:3090` 实例），非杜撰。

开阳 Server 模式对外提供 **4 组接口**：

| 组 | 端点 | 认证 | 用途 |
|---|---|---|---|
| OpenAI 兼容 | `/v1/models`、`/v1/chat/completions` | Bearer Token | 任意 OpenAI SDK 直接接入 |
| JSON-RPC 2.0 | `/rpc` | Bearer Token | 编程式调用 agent（run/steer/abort…） |
| WebSocket | `/ws` | Bearer Token | 实时事件流 + 中途纠正 |
| Web UI | `/web/`、`/web/api/*`、`/web/ws` | Cookie 会话 | 浏览器界面（多会话/模型/参数/流式） |
| 管理 | `/admin/status`、`/admin/switch` | Bearer Token | 运行时开关服务 |

---

## 0. 启动

```bash
mizar --serve --addr :3003 --token your-token \
  --base-url http://127.0.0.1:3002/v1 --model deepseek-v4-flash
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `--serve` | false | 启动 Server 模式（持久运行） |
| `--addr` | `:3003` | 监听地址 |
| `--token` | 空 | Bearer Token；**空 = 不校验认证**（仅内网/本机用） |
| `--http` / `--rpc` / `--ws` | 均 true | 分别开关三组服务 |
| `--base-url` / `--api-key` / `--model` | — | 上游 LLM 配置 |

启动后日志会打印 Web UI 地址与登录用户。**Web UI 首次启动自动生成随机密码**，写入 `~/.mizar/config.json` 的 `web.password`。

### 认证

- **Bearer Token**（`/v1/*`、`/rpc`、`/ws`、`/admin/*`）：`Authorization: Bearer <token>`。
  仅当 `--token` 非空时校验；不匹配返回 `401`。
- **Cookie 会话**（`/web/api/*`、`/web/ws`）：先 `POST /web/api/login` 拿 `mizar_web_session` cookie（HttpOnly，24h TTL）。未登录返回 `401`。

### 统一错误格式

所有 JSON 错误体：

```json
{"error":{"message":"unauthorized","type":"mizar_error"}}
```

---

## 1. OpenAI 兼容接口

任意 OpenAI SDK 只需把 `base_url` 指向 `http://<host>:<port>/v1`。

### 1.1 `GET /v1/models`

返回当前模型（供 SDK 探测）。

```bash
curl http://127.0.0.1:3003/v1/models -H "Authorization: Bearer $TOKEN"
```

```json
{"object":"list","data":[{"id":"deepseek-v4-flash","object":"model","created":1792025182,"owned_by":"mizar"}]}
```

### 1.2 `POST /v1/chat/completions`

**这是「Agent 模式」**：走完整工具循环（可读写文件、执行命令、调 MCP 等），把最后一条 `messages` 内容作为任务，其余作为会话历史。

请求体（OpenAI 兼容子集）：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `model` | string | 否 | 模型名（回显用；实际模型由 server 配置决定） |
| `messages` | array | **是** | `[{role, content}]`；**最后一条**为任务，之前为历史 |
| `stream` | bool | 否 | `true` 走 SSE 流式 |
| `max_tokens` | int | 否 | 预留字段 |

**非流式**：

```bash
curl -X POST http://127.0.0.1:3003/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"只回复：pong"}]}'
```

```json
{"id":"chatcmpl-1792025190771269391","object":"chat.completion","created":1792025190,
 "model":"deepseek-v4-flash",
 "choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}
```

**流式**（`stream:true`，SSE `chat.completion.chunk`）：

```
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"1、2、3 ✅"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":...,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

> 说明：流式只转发**最终回复文本**增量；中间工具调用步的内容不转发。纯工具任务（无回复文本流）会在结束时一次性补发完整回复。

---

## 2. JSON-RPC 2.0（`POST /rpc`）

请求：`{"jsonrpc":"2.0","id":1,"method":"<name>","params":{...}}`
响应：`{"jsonrpc":"2.0","id":1,"result":{...}}` 或 `{"jsonrpc":"2.0","id":1,"error":{"code":...,"message":"..."}}`

标准错误码：`-32700` 解析错误 · `-32600` 无效请求 · `-32601` 方法不存在 · `-32603` 内部错误。

### 方法一览

| 方法 | params | 返回 | 说明 |
|---|---|---|---|
| `agent.run` | `{"task":"..."}` | `{"reply":"..."}` | 执行任务（**同步**阻塞到结束） |
| `agent.steer` | `{"message":"..."}` | `{"ok":true}` | 向运行中的循环插入纠正消息（不等待） |
| `agent.abort` | — | `{"ok":true}` | 中止当前循环 |
| `agent.running` | — | `{"running":false}` | 查询运行状态 |
| `tools.list` | — | `{"tools":[{"name","description"}]}` | 列出可用工具 |
| `system.ping` | — | `{"pong":true,"model":"..."}` | 健康检查 |
| *插件自定义* | 任意 | 任意 | 插件导出 `rpc_<name>` 即注册为 `<name>` 方法 |

### 示例

```bash
# 健康检查
curl -X POST http://127.0.0.1:3003/rpc -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"system.ping"}'
```
```json
{"jsonrpc":"2.0","id":1,"result":{"pong":true,"model":"deepseek-v4-flash"}}
```

```bash
# 执行任务
curl -X POST http://127.0.0.1:3003/rpc -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"agent.run","params":{"task":"列出当前目录文件"}}'
```
```json
{"jsonrpc":"2.0","id":2,"result":{"reply":"当前目录包含：..."}}
```

```json
// tools.list（截断示例）
{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"read","description":"..."},{"name":"bash","description":"..."}]}}

// 方法不存在
{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"method not found: nope"}}
```

> ⚠ `agent.run` 是**同步**的：任务跑多久，HTTP 请求就挂多久。需要边跑边纠正，请用 **WebSocket**（见下）。

---

## 3. WebSocket（`GET /ws`）

长连接双向通道：客户端发 JSON-RPC 2.0 请求，服务端实时推送 Agent 中间事件。**这是「看到输出不对立刻纠正」的关键通道。**

- 连接：`ws://<host>:<port>/ws`，`Authorization: Bearer <token>` 头（或 `?token=`，视客户端而定）。
- 一个 Agent 单实例，**所有连接共享同一事件流**（广播模式）。

### 客户端 → 服务端

发送与 `/rpc` 相同的方法集。区别：

- **`agent.run` 异步**：立即返回，不阻塞读循环；结果通过 `{"type":"result",...}` 推回。
- **`agent.steer` / `agent.abort`**：先回 `{"type":"ack","method":"...","id":"..."}`，再执行 —— 所以在 `run` 跑着的同时能实时纠正。

### 服务端 → 客户端

| 消息 | 形态 |
|---|---|
| LLM 响应 | `{"type":"event","event":"llm_response","step":0,"content":"..."}` |
| 工具调用 | `{"type":"event","event":"tool_call","step":0,"tool":"calc","args":"..."}` |
| 工具结果 | `{"type":"event","event":"tool_result","step":0,"tool":"calc","result":"..."}` |
| RPC 结果 | `{"type":"result","id":<请求id>,"result":{...}}` |
| 确认 | `{"type":"ack","method":"agent.steer","id":"..."}` |

### 示例（Node.js）

```js
const WebSocket = require("ws");
const ws = new WebSocket("ws://127.0.0.1:3003/ws", { headers: { Authorization: "Bearer " + TOKEN } });

ws.on("open", () => {
  ws.send(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "agent.run",
    params: { task: "写一个 hello.py 并运行" } }));
});
ws.on("message", (raw) => {
  const m = JSON.parse(raw);
  if (m.type === "event" && m.event === "tool_call") console.log("→", m.tool, m.args);
  if (m.type === "event" && m.event === "tool_result") console.log("←", m.result);
  if (m.type === "result") console.log("最终回复:", m.result.reply);
  // 发现跑偏时立刻纠正：
  // ws.send(JSON.stringify({jsonrpc:"2.0",id:2,method:"agent.steer",params:{message:"改用 Go 写"}}));
});
```

---

## 4. Web UI（浏览器界面 + 其 API）

浏览器打开 `http://<host>:<port>/web/`（`/` 会 302 跳转到 `/web/`）。
前端资源（HTML/CSS/JS）全部 **embed 内嵌**在二进制里，**零外部依赖**（不引用任何互联网资源）。

- **登录**：用户名 `admin`（可配），密码见 `~/.mizar/config.json` 的 `web.password`（首次启动自动生成）。
- **两种模式**（界面顶部切换）：
  - **普通对话**：直连 LLM，不带工具。适合问答。
  - **Agent 模式**：完整工具循环，可执行任务。
  - 两种模式**共享同一会话历史**，切换不丢上下文。
- **多会话**：左侧列表，新建 / 切换 / 删除，标题自动生成，持久化到 `~/.mizar/webui.json`。
- **设置**：模型（可拉取上游清单）、思考等级（auto/off/low/medium/high）、温度、top_p、max_tokens、系统提示词。持久化，重启自动恢复。
- **流式输出**：正文逐字到达，思考过程单独显示。

### Web API 端点

全部需 **cookie 会话**（`mizar_web_session`），未登录 `401`。

| 方法 | 路径 | 请求 | 返回 |
|---|---|---|---|
| POST | `/web/api/login` | `{"username","password"}` | `{"ok":true,"username":"admin"}` + Set-Cookie |
| POST | `/web/api/logout` | — | `{"ok":true}` |
| GET | `/web/api/me` | — | `{"username","model","settings"}` |
| GET | `/web/api/conversations` | — | `{"conversations":[{id,title,mode,updated_at,count}]}` |
| POST | `/web/api/conversations` | `{"mode":"chat\|agent"}` | `{"conversation":{...}}` |
| DELETE | `/web/api/conversations?id=<id>` | — | `{"ok":true}` |
| GET | `/web/api/conversation?id=<id>` | — | `{"conversation":{id,title,mode,messages:[...]}}` |
| PATCH | `/web/api/conversation?id=<id>` | `{"title":"..."}` | `{"ok":true}` |
| POST | `/web/api/chat` | `{"conversation_id","message"}` | `{"reply":"..."}`（**普通对话**） |
| POST | `/web/api/run` | `{"conversation_id","message"}` | `{"reply":"..."}`（**Agent 模式**） |
| POST | `/web/api/abort` | — | `{"ok":true}` |
| GET | `/web/api/settings` | — | `{"settings":{...},"model":"..."}` |
| POST | `/web/api/settings` | `{model,thinking,temperature,top_p,max_tokens,system}` | `{"ok":true,"settings":{...},"model":"..."}` |
| GET | `/web/api/models` | — | `{"models":[...],"current":"..."}`（拉上游清单） |
| GET | `/web/ws` | WebSocket | 同 `/ws` 事件流（cookie 鉴权） |

#### 示例：普通对话 vs Agent 模式

```bash
# 1) 登录（拿 cookie）
curl -c ck.txt -X POST http://127.0.0.1:3003/web/api/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"<密码>"}'

# 2) 新建会话
CID=$(curl -s -b ck.txt -X POST http://127.0.0.1:3003/web/api/conversations \
  -H "Content-Type: application/json" -d '{"mode":"chat"}' | jq -r .conversation.id)

# 3a) 普通对话（直连 LLM，无工具）
curl -b ck.txt -X POST http://127.0.0.1:3003/web/api/chat \
  -H "Content-Type: application/json" \
  -d "{\"conversation_id\":\"$CID\",\"message\":\"你好\"}"

# 3b) Agent 模式（完整工具循环，同一会话历史连续）
curl -b ck.txt -X POST http://127.0.0.1:3003/web/api/run \
  -H "Content-Type: application/json" \
  -d "{\"conversation_id\":\"$CID\",\"message\":\"用 ls 列出当前目录\"}"

# 4) 设置：切模型 + 调温度 + 思考等级
curl -b ck.txt -X POST http://127.0.0.1:3003/web/api/settings \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","thinking":"high","temperature":0.3,"top_p":0.9,"max_tokens":4096}'
```

实测响应：

```json
// GET /web/api/me
{"username":"admin","model":"deepseek-v4-flash",
 "settings":{"model":"deepseek-v4-flash","temperature":0.3,"top_p":0.9,"max_tokens":4096,"thinking":"high"}}

// POST /web/api/chat
{"reply":"我是开阳（Mizar）的对话助手。"}
```

> 两种模式的流式增量都通过 `/web/ws` 广播：
> `{"type":"event","event":"stream","step":N,"delta":"...","kind":"reply|thinking"}`

---

## 5. 管理端点

### `GET /admin/status`

```json
{"services":{"http":true,"jsonrpc":true,"ws":true},"model":"deepseek-v4-flash"}
```

### `POST /admin/switch`

运行时开关服务（即时生效；关闭后对应端点返回 `503`；重启后回到启动参数值）。

```bash
curl -X POST http://127.0.0.1:3003/admin/switch -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"service":"ws","enabled":false}'
```
```json
{"ok":true,"service":"ws","enabled":false}
```

`service` 取值：`http` | `jsonrpc` | `ws`。未知值返回 `400`。

---

## 6. 其他接口

### 6.1 LSP Server 模式（stdio，非 HTTP）

`mizar --lsp-server`：mizar 作为 LSP server，通过 **stdio** 与 IDE/编辑器通信（JSON-RPC over stdin/stdout，遵循 LSP 协议）。不是 HTTP 端点，供编辑器集成。

### 6.2 插件自定义 RPC

插件导出 `rpc_<name>` 函数即自动注册为 `/rpc` 与 `/ws` 上的 `<name>` 方法：

```js
// 插件文件（~/.mizar/extensions/xxx.js）
export function rpc_hello(params) { return { greeting: "hi" }; }
```

```bash
curl -X POST http://127.0.0.1:3003/rpc -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"hello","params":{}}'
# → {"jsonrpc":"2.0","id":1,"result":{"greeting":"hi"}}
```

### 6.3 插件宿主函数

插件内可调用的宿主函数（`fs_read`/`http_get`/`shell_exec`/`llm_chat`/`db_query`/`ws_emit` 等约 40 个）见 [`host-funcs.md`](host-funcs.md)。

---

## 附：端点总表

```
# OpenAI 兼容（Bearer）
GET    /v1/models
POST   /v1/chat/completions            # stream=true 走 SSE

# JSON-RPC 2.0（Bearer）
POST   /rpc                            # agent.run/steer/abort/running, tools.list, system.ping, 插件自定义

# WebSocket（Bearer）
GET    /ws                             # 事件流 + 异步 run + 实时 steer/abort

# 管理（Bearer）
GET    /admin/status
POST   /admin/switch                   # {"service":"http|jsonrpc|ws","enabled":bool}

# Web UI（Cookie 会话）
GET    /                               # 302 → /web/
GET    /web/                           # 界面（embed 内嵌，零外部依赖）
GET    /web/ws                         # 事件流（cookie 鉴权）
POST   /web/api/login
POST   /web/api/logout
GET    /web/api/me
GET    /web/api/conversations
POST   /web/api/conversations
DELETE /web/api/conversations?id=
GET    /web/api/conversation?id=
PATCH  /web/api/conversation?id=
POST   /web/api/chat                   # 普通对话（无工具）
POST   /web/api/run                    # Agent 模式（完整工具循环）
POST   /web/api/abort
GET    /web/api/settings
POST   /web/api/settings
GET    /web/api/models

# stdio（非 HTTP）
mizar --lsp-server                     # LSP server
```
