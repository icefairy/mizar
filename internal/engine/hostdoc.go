package engine

import (
	"fmt"
	"strings"
)

// HostDoc 宿主函数文档（两级：Brief 供精简清单，Full 供 doc_get 按需查询）。
// 这里是宿主函数的权威文档来源——新增/修改宿主函数时同步维护本条。
type HostDoc struct {
	Name     string // 函数名
	Brief    string // 精简版一句话（自动生成清单用）
	Full     string // 完整文档（参数/返回/示例/常见坑）
}

// hostDocs 全部宿主函数文档表（顺序即清单顺序）。
// 精简清单与 doc_get 均以此为唯一数据源，杜绝文档与代码脱节。
var hostDocs = []HostDoc{
	// ---- 文件 ----
	{Name: "fs_read", Brief: "读取文件全文（限 10MB）", Full: `fs_read(path) -> string
读取文件全文，返回文件内容字符串。
参数: path — 绝对或相对路径
限制: 超过 10MB 报错；大文件用 fs_read_range 分片
示例: fs_read("/data/app.db")`},
	{Name: "fs_read_range", Brief: "有界 seek 读取（单次 4MB）", Full: `fs_read_range(path, offset, length) -> [content, totalSize]
按偏移量读取文件片段，返回 [内容, 文件总大小]。
参数: offset — 起始字节；length — 读取字节数（上限 4MB）
用途: 大文件分片读取，配合 totalSize 循环取完
示例: const [c, total] = fs_read_range("/big.log", 0, 1024*1024)`},
	{Name: "fs_write", Brief: "写入文件（覆盖）", Full: `fs_write(path, content) -> void
写文件，不存在则创建，存在则覆盖。自动建父目录。
示例: fs_write("/tmp/x.txt", "hello")`},
	{Name: "fs_list", Brief: "列出目录内容", Full: `fs_list(dir) -> string[]
返回目录下所有条目名（不含路径前缀），非递归。
示例: fs_list("/data/codes")`},

	// ---- 网络 ----
	{Name: "http_get", Brief: "GET 请求", Full: `http_get(url) -> string
发起 GET 请求，返回响应体字符串。无超时参数（默认 30s）。
示例: http_get("https://api.example.com/v1/status")`},
	{Name: "http_post", Brief: "POST 请求（JSON body）", Full: `http_post(url, body) -> string
POST 请求，body 为 JSON 字符串，自动设置 Content-Type: application/json。
示例: http_post("https://api.example.com/v1/create", json_encode({name: "x"}))`},
	{Name: "http_request", Brief: "通用 HTTP 请求（任意方法/头）", Full: `http_request(method, url, body, headersJSON) -> string
通用 HTTP：method 任意（GET/POST/PUT/DELETE/PATCH），headersJSON 为 JSON 字符串对象。
示例: http_request("PUT", "https://api.example.com/v1/up", "{\"a\":1}", "{\"X-Token\":\"abc\"}")`},

	// ---- 数据 ----
	{Name: "json_encode", Brief: "对象转 JSON 字符串", Full: `json_encode(v) -> string
任意 JS 值转 JSON 字符串。
示例: json_encode({a: 1, b: [2, 3]})  // "{\"a\":1,\"b\":[2,3]}"`},
	{Name: "json_decode", Brief: "JSON 字符串转对象", Full: `json_decode(s) -> object
JSON 字符串解析为 JS 对象。非法 JSON 抛错。
示例: json_decode("{\"a\":1}").a  // 1`},
	{Name: "shell_exec", Brief: "执行 shell 命令", Full: `shell_exec(cmd) -> string
执行 shell 命令并返回 stdout。stderr 合并到输出。
安全: 命令字符串会被 shell 解析——禁止拼接不可信输入；优先用 fs_*/http_* 宿主函数
示例: shell_exec("df -h | tail -5")`},

	// ---- 基础 ----
	{Name: "log", Brief: "写日志", Full: `log(msg) -> void
写入主进程日志（stdout，带时间戳）。调试用。
示例: log("plugin started")`},
	{Name: "sleep", Brief: "休眠毫秒", Full: `sleep(ms) -> void
阻塞休眠指定毫秒。轮询场景配合使用。
示例: sleep(1000)  // 等 1 秒`},
	{Name: "llm_chat", Brief: "调用大模型对话", Full: `llm_chat(messagesJSON) -> string
调用配置的大模型，messages 为 OpenAI 格式 JSON 数组，返回 assistant 文本。
示例: llm_chat(json_encode([{role: "user", content: "你好"}]))`},

	// ---- 数据库 ----
	{Name: "db_query", Brief: "数据库查询（sqlite3/mysql/postgres）", Full: `db_query(driver, dsn, sql) -> string
内置数据库访问，驱动白名单: sqlite3 / mysql / postgres（纯 Go 免 CGO）。
SELECT/SHOW/PRAGMA 返回 JSON 行数组 [{"col":val},...]；写语句返回 {"rowsAffected":N}。
连接按 driver|dsn 池化复用；会话状态（PRAGMA/SET/临时表）跨调用保留，需干净会话用 db_close。
SQL 注入: db_query 只接字符串，拼接前必须转义（见坑）
示例: db_query("sqlite3", "/data/app.db", "SELECT id FROM users LIMIT 5")
坑: ①逐条 INSERT 慢（每条重建事务），批量用 db_exec_batch；②字符串参数用 .replace(/'/g, "''") 转义`},
	{Name: "db_exec_batch", Brief: "事务批量执行 SQL 数组", Full: `db_exec_batch(driver, dsn, sqlsJSON) -> string
SQL 数组一个事务执行，任一失败整体回滚。返回 {"rowsAffected":N,"statements":M}。
每条可含分号多语句（自动拆分）。写多行优先用它（比逐条 db_query 快 10x+）。
示例: db_exec_batch("sqlite3", "/d.db", json_encode([
  "CREATE TABLE IF NOT EXISTS t (id INTEGER)",
  "INSERT INTO t VALUES (1); INSERT INTO t VALUES (2);"
]))`},
	{Name: "db_close", Brief: "关闭连接丢弃会话残留", Full: `db_close(driver, dsn) -> string
关闭 driver|dsn 的连接池连接，丢弃该连接上的会话状态（PRAGMA/SET/临时表）。
下次 db_query 自动重建连接。幂等。:memory: 库 close 后数据消失。
示例: db_close("sqlite3", "/data/app.db")`},

	// ---- 外部能力 ----
	{Name: "mcp_call", Brief: "调用外部 MCP server 工具", Full: `mcp_call(server, tool, argsJSON) -> string
调用配置的 MCP server 工具。server 须在 config.json mcp_servers 里配置（stdio 命令或 HTTP URL）。
覆盖 db_query 白名单外的长尾能力（Redis/Kafka/MongoDB/ClickHouse 等）。
示例: mcp_call("redis", "get", json_encode({key: "foo"}))`},
	{Name: "hook_on", Brief: "注册生命周期回调", Full: `hook_on(event, callback) -> void
注册 Agent 生命周期挂载点回调。callback 签名 (ctxJSON: string) => string。
事件: RunStart/RunEnd/StepEnd/ToolPreprocess/ToolResult/ToolResultPostprocess/CompactionBefore/CompactionAfter/PlanUpdated/PlanExecuted/PlanRejected
ctxJSON 为事件上下文 JSON（含 task/tool/result 等字段），回调返回字符串被记录。
示例: hook_on("RunEnd", (ctx) => { db_query("sqlite3", "/d.db", "INSERT ..."); return "ok"; })`},

	// ---- 纯函数标准库 ----
	{Name: "time_now", Brief: "当前 UTC 时间 RFC3339", Full: `time_now() -> string
当前 UTC 时间，RFC3339Nano 格式（含纳秒）。
示例: time_now()  // "2026-08-14T12:00:00.123456789Z"`},
	{Name: "time_unix", Brief: "当前 Unix 秒", Full: `time_unix() -> number
当前 Unix 时间戳（秒）。
示例: time_unix()  // 1786668000`},
	{Name: "uuid", Brief: "生成 UUID v4", Full: `uuid() -> string
生成 UUID v4（crypto/rand，非伪随机）。
示例: uuid()  // "3f2a..."`},
	{Name: "base64_encode", Brief: "Base64 编码", Full: `base64_encode(s) -> string
标准 Base64 编码（含填充）。
示例: base64_encode("hello")  // "aGVsbG8="`},
	{Name: "base64_decode", Brief: "Base64 解码", Full: `base64_decode(s) -> string
标准 Base64 解码。非法输入抛错。
示例: base64_decode("aGVsbG8=")  // "hello"`},
	{Name: "hash_sha256", Brief: "SHA-256 十六进制摘要", Full: `hash_sha256(s) -> string
SHA-256 哈希，输出 hex 小写。
示例: hash_sha256("abc")  // "ba7816bf8f01cfea..."`},
	{Name: "path_join", Brief: "拼接路径", Full: `path_join(...parts) -> string
POSIX 路径拼接，自动处理分隔符。变参。
示例: path_join("/a", "b", "c.ts")  // "/a/b/c.ts"`},
	{Name: "path_base", Brief: "取文件名", Full: `path_base(p) -> string
返回路径最后一段（文件名）。
示例: path_base("/a/b/c.ts")  // "c.ts"`},
	{Name: "path_dir", Brief: "取目录名", Full: `path_dir(p) -> string
返回路径目录部分。
示例: path_dir("/a/b/c.ts")  // "/a/b"`},
	{Name: "url_parse", Brief: "解析 URL 返回 JSON", Full: `url_parse(u) -> string
解析 URL，返回 JSON {scheme,host,path,query,fragment,user}。
示例: url_parse("https://u@x.com:8080/api?q=1#top")`},
	{Name: "count_tokens", Brief: "估算文本 token 数", Full: `count_tokens(text) -> number
估算 token 数（CJK 按字符、其他按 4 字符/token）。与 Agent 压缩触发同口径。
估算用；精确值以 LLM API usage 为准。
示例: count_tokens("你好hello")  // 4`},

	// ---- WebSocket ----
	{Name: "ws_emit", Brief: "向 WS 客户端推送事件", Full: `ws_emit(event, dataJSON) -> void
向所有 WebSocket 客户端推送自定义事件（Server 模式）。
示例: ws_emit("plugin_update", json_encode({v: 1}))`},

	// ---- LSP ----
	{Name: "lsp_register_diagnostic", Brief: "注册 LSP 诊断提供者", Full: `lsp_register_diagnostic(name, fn) -> error
注册诊断提供者，fn(uri) => string（诊断 JSON）。
仅 LSP 相关插件使用。`},
	{Name: "lsp_register_completion", Brief: "注册 LSP 补全提供者", Full: `lsp_register_completion(name, fn) -> error
注册补全提供者，fn(uri, line, col) => 补全项 JSON。
仅 LSP 相关插件使用。`},
}

// hostDocIndex name -> HostDoc 查询索引（构建一次）。
var hostDocIndex = func() map[string]HostDoc {
	m := make(map[string]HostDoc, len(hostDocs))
	for _, d := range hostDocs {
		m[d.Name] = d
	}
	return m
}()

// HostDocGet 返回函数完整文档（doc_get 宿主函数）。
func HostDocGet(name string) (string, error) {
	d, ok := hostDocIndex[name]
	if !ok {
		return "", fmt.Errorf("doc_get: 未找到函数 %q", name)
	}
	return d.Full, nil
}

// HostDocBriefs 返回精简清单（name + brief，自动生成插件开发提示词用）。
func HostDocBriefs() string {
	var sb strings.Builder
	for _, d := range hostDocs {
		sb.WriteString("- ")
		sb.WriteString(d.Name)
		sb.WriteString(" — ")
		sb.WriteString(d.Brief)
		sb.WriteString("\n")
	}
	return sb.String()
}

// HostDocList 返回全部文档条目（生成命令统计用）。
func HostDocList() []HostDoc {
	return hostDocs
}
