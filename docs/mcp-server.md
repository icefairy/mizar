# 把 Mizar 当作 HTTP MCP Server 使用

> 适用版本：v0.8.13+ ｜ 端点：`POST /mcp` ｜ 传输：MCP Streamable HTTP（单请求/单响应 JSON）

Mizar 除了作为 MCP **客户端**（`mcp_call` / `/mcp add`）调用外部 server，
也可以反过来作为 MCP **服务端**，把自身已注册的工具（内置 + 插件）暴露给
Claude Desktop、CodeBuddy、Cursor 等 MCP 客户端。

只暴露**工具能力**：`initialize`、`ping`、`tools/list`、`tools/call`。
不提供 resources / prompts / 采样，也不提供 GET SSE 长连接。

---

## 1. 启动

MCP 端点和 OpenAI / JSON-RPC 服务共用 `--serve` 进程：

```bash
mizar --serve --addr :3003 --token your-token \
  --base-url http://127.0.0.1:3002/v1 --model deepseek-v4-flash
```

MCP 地址就是：

```
http://<host>:3003/mcp
```

> **必须设置 `--token`**。MCP 客户端没有 cookie 登录通道，token 是唯一鉴权手段；
> token 为空时 `/mcp` 直接返回 403，避免把 `bash` 等工具无认证暴露到网络。
> 同时建议 `--addr 127.0.0.1:3003` 或加防火墙——这些工具等价于远程命令执行能力。

---

## 2. 客户端配置

通用写法（不同客户端的配置文件名/外层键可能不同，字段含义一致）：

```json
{
  "mcpServers": {
    "mizar": {
      "type": "http",
      "url": "http://127.0.0.1:3003/mcp",
      "headers": {
        "Authorization": "Bearer your-token"
      }
    }
  }
}
```

部分客户端用 `transport: "streamable-http"` 或 `"http"`，或把地址写在 `serverUrl`；
只要最终发出的是：
- `POST /mcp`
- `Content-Type: application/json`
- `Authorization: Bearer your-token`

即可，不需要额外的 SSE 连接。

---

## 3. 手动验证（curl）

```bash
TOKEN=your-token

# 1) 握手
curl -s http://127.0.0.1:3003/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'

# 2) 列工具
curl -s http://127.0.0.1:3003/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 3) 调用工具（arguments 是对象，服务端会序列化成工具需要的 JSON 字符串）
curl -s http://127.0.0.1:3003/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ls","arguments":{"path":"."}}}'
```

`tools/call` 返回：

```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"..."}]}}
```

工具自身执行失败时不是 JSON-RPC error，而是 `result.isError: true`（MCP 规范）：

```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"read: ..."}],"isError":true}}
```

---

## 4. 工具范围与工作目录

- `tools/list` 返回 `~/.mizar/extensions` 里加载的插件工具 + 全部内置工具
  （`read/write/edit/ls/grep/find/bash/todo/...`），禁用中的工具不会出现。
- 工具执行目录是 Mizar 进程启动时的**工作目录**（`--workdir`，默认当前目录）。
  与 Web UI 的 per-conv 工作空间不同：MCP 调用没有会话概念，因此直接以服务端工作目录为准。
- 工具入参沿用 Mizar 的约定：一个 JSON 对象；MCP 客户端的 `arguments` 对象会被原样序列化后传入。
- 工具没有字段级 JSON Schema，`tools/list` 返回宽松的
  `{"type":"object","additionalProperties":true}`；参数说明看每个工具的 `description`。

### 4.1 上传文件：`upload_file`

MCP 协议本身没有文件上传原语，用内置工具 `upload_file` 把内容写进服务端工作目录：

```json
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{
  "name":"upload_file",
  "arguments":{"path":"docs/report.bin","content":"AAEC/w==","encoding":"base64"}
}}
```

- `encoding` 省略或 `text`：`content` 按 UTF-8 文本写入
- `encoding: "base64"`：解码后写入，用于二进制文件；base64 中的空格/换行会被忽略
- 自动创建父目录；路径相对服务端工作目录，`../` 越界会被拒绝
- 工具层上限：解码后 32MB；但 MCP 单次请求体上限 4MB，因此经 `/mcp` 上传的
  二进制实际约 3MB/次（base64 膨胀 4/3），更大文件请分片多次写入

```bash
curl -s http://127.0.0.1:3003/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"upload_file","arguments":{"path":"a.txt","content":"hello"}}}'
```

---

## 5. 协议细节

| 方法 | 行为 |
|---|---|
| `initialize` | 返回 `protocolVersion`（当前 `2025-03-26`）、`capabilities.tools`、`serverInfo` |
| `ping` | 返回 `{}` |
| `tools/list` | 返回 `{tools:[{name,description,inputSchema}]}` |
| `tools/call` | 返回 `{content:[{type:"text",text}], isError?}` |
| 通知（`notifications/*` 或无 id） | `202 Accepted`，无响应体 |
| 未知方法 | JSON-RPC `-32601` |

单次请求体上限 4MB。工具执行超时沿用插件管理器默认值（30s）。

---

## 6. 与「Mizar 作为 MCP 客户端」的区别

| | 作为客户端 | 作为服务端（本文） |
|---|---|---|
| 配置 | `~/.mizar/mcp.json` 里加 server | MCP 客户端里加 `http://<host>:3003/mcp` |
| 协议方向 | Mizar 主动调用别人 | 别人主动调用 Mizar 的工具 |
| 入口 | `mcp_call` 宿主函数 / `/mcp add` | `POST /mcp` |
| 鉴权 | 可配置 `headers`（如 Bearer） | 必须 `--token` |