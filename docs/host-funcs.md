# 开阳宿主函数参考

写插件时的精简清单（~560 tokens）：

- fs_read — 读取文件全文（限 10MB）
- fs_read_range — 有界 seek 读取（单次 4MB）
- fs_write — 写入文件（覆盖）
- fs_list — 列出目录内容
- http_get — GET 请求
- http_post — POST 请求（JSON body）
- http_request — 通用 HTTP 请求（任意方法/头）
- json_encode — 对象转 JSON 字符串
- json_decode — JSON 字符串转对象
- shell_exec — 执行 shell 命令
- log — 写日志
- sleep — 休眠毫秒
- llm_chat — 调用大模型对话
- db_query — 数据库查询（sqlite3/mysql/postgres）
- db_exec_batch — 事务批量执行 SQL 数组
- db_close — 关闭连接丢弃会话残留
- mcp_call — 调用外部 MCP server 工具
- hook_on — 注册生命周期回调
- time_now — 当前 UTC 时间 RFC3339
- time_unix — 当前 Unix 秒
- uuid — 生成 UUID v4
- base64_encode — Base64 编码
- base64_decode — Base64 解码
- hash_sha256 — SHA-256 十六进制摘要
- path_join — 拼接路径
- path_base — 取文件名
- path_dir — 取目录名
- url_parse — 解析 URL 返回 JSON
- count_tokens — 估算文本 token 数
- ws_emit — 向 WS 客户端推送事件
- lsp_register_diagnostic — 注册 LSP 诊断提供者
- lsp_register_completion — 注册 LSP 补全提供者
- tcp_listen — 启动 TCP 服务器
- tcp_dial — TCP 客户端连接
- tcp_send — TCP 发送数据
- tcp_onrecv — 注册 TCP 接收回调
- tcp_close — 关闭 TCP 连接
- tcp_stop — 停止 TCP 服务器
- ftp_connect — FTP 登录
- ftp_list — FTP 列目录
- ftp_upload — FTP 上传
- ftp_download — FTP 下载
- ftp_mkdir — FTP 建目录
- ftp_rmdir — FTP 删目录
- ftp_delete — FTP 删文件
- ftp_rename — FTP 重命名
- ftp_close — FTP 登出关闭
- ws_connect — WS 客户端连接
- ws_send — WS 发送消息
- ws_onmessage — WS 注册消息回调
- ws_close — WS 关闭连接

完整文档用宿主函数 doc_get(name) 按需查询。
