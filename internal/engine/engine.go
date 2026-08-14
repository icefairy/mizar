// Package engine 封装 goja JS 引擎与 esbuild TS 编译管线。
package engine

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

// Engine 是 goja 运行时 + 宿主函数注册的封装。
type Engine struct {
	vm     *goja.Runtime
	host   *HostFuncs
	closed bool
}

// HostFuncs 是宿主注册给 JS 插件的函数集（Go 能力）。
type HostFuncs struct {
	HTTPGet  func(url string) (string, error)
	HTTPPost func(url, body string) (string, error)
	// HTTPRequest 统一 HTTP 请求：method 任意（GET/POST/PUT/DELETE/PATCH...），
	// headersJSON 形如 {"Authorization":"Bearer xxx","X-Custom":"v"}（可空）。
	HTTPRequest func(method, url, body, headersJSON string) (string, error)
	JSONDecode  func(s string) (map[string]any, error)
	JSONEncode  func(v any) (string, error)
	FSRead      func(path string) (string, error)
	// FSReadRange 有界读取（seek 语义）：从 offset 读最多 length 字节，
	// 返回 [content, totalSize, error]。插件读大文件时用循环分片，
	// 替代 fs_read 的全量读（10MB 上限）。
	FSReadRange func(path string, offset, length int64) (string, int64, error)
	FSWrite     func(path, content string) error
	FSList      func(dir string) ([]string, error)
	ShellExec   func(cmd string) (string, error)
	// LSPUnregisterDiagnostic 插件热重载时按名字移除诊断提供者
	LSPUnregisterDiagnostic func(name string)
	// LSPUnregisterCompletion 插件热重载时按名字移除补全提供者
	LSPUnregisterCompletion func(name string)
	// name: 提供者名称；fn: (uri string) => jsonString（诊断数组）
	LSPRegisterDiagnostic func(name string, fn func(uri string) string) error
	// LSPRegisterCompletion 注册 LSP 补全提供者（JS 插件用）。
	// name: 提供者名称；fn: (uri string, line, col int) => jsonString（补全数组）
	LSPRegisterCompletion func(name string, fn func(uri string, line, col int) string) error
	LLMChat               func(messagesJSON string) (string, error)
	Log                   func(msg string)
	Sleep                 func(ms int)
	// WSEmit 向所有 WS 客户端广播自定义事件（event + JSON 数据）。
	WSEmit func(event, dataJSON string)
	// WSClient 插件 WS 客户端桥（连外部 WS 服务，如飞书长连接）。
	WSClient *WSClientBridge
	// DBQuery 内置数据库查询：db_query(driver, dsn, sql) -> JSON。
	// driver 白名单: sqlite3 / mysql / postgres。nil 时不注册该函数。
	DBQuery func(driver, dsn, sql string) (string, error)
	// DBExecBatch 事务批量执行：db_exec_batch(driver, dsn, sqlsJSON) -> JSON。
	// sqlsJSON 为 SQL 字符串数组，全部语句一个事务内执行，失败整体回滚。
	// nil 时不注册该函数。
	DBExecBatch func(driver, dsn, sqlsJSON string) (string, error)
	// DBClose 关闭连接：db_close(driver, dsn) -> JSON。丢弃会话残留（PRAGMA/SET/临时表），
	// 下次 db_query 调用会重建连接。nil 时不注册该函数。
	DBClose func(driver, dsn string) (string, error)
	// MCPCall 调用外部 MCP server 工具：mcp_call(server, tool, argsJSON) -> 文本。
	// 覆盖内置驱动之外的长尾能力（Redis/Kafka/MongoDB/ClickHouse 等）。
	// nil 时不注册该函数。
	MCPCall func(server, tool, argsJSON string) (string, error)
	// HookOn 注册挂载点回调：hook_on(eventName, jsCallback)。
	// eventName 归一化（大小写/连字符/下划线不敏感）; jsCallback 为 JS 函数 (ctxJSON)=>string。
	// nil 时不注册该函数。
	HookOn func(event string, cb func(ctxJSON string) (string, error)) error
}

// cjsShim 让 esbuild 的 CommonJS 输出能在 goja 中运行。
const cjsShim = `var module = {exports:{}};
var exports = module.exports;
`

// New 创建引擎并注册宿主函数。
func New(host *HostFuncs) (*Engine, error) {
	if host == nil {
		host = &HostFuncs{}
	}
	e := &Engine{vm: goja.New(), host: host}
	if err := e.registerHostFuncs(); err != nil {
		return nil, fmt.Errorf("register host funcs: %w", err)
	}
	if _, err := e.vm.RunScript("__cjs_shim__", cjsShim); err != nil {
		return nil, fmt.Errorf("cjs shim: %w", err)
	}
	return e, nil
}

func (e *Engine) registerHostFuncs() error {
	h := e.host
	reg := func(name string, fn any) {
		_ = e.vm.Set(name, fn)
	}
	// 纯函数标准库（无 I/O、零副作用），引擎创建时无条件注册
	registerStdlib(reg)
	if h.HTTPGet != nil {
		reg("http_get", h.HTTPGet)
	}
	if h.HTTPPost != nil {
		reg("http_post", h.HTTPPost)
	}
	if h.HTTPRequest != nil {
		reg("http_request", h.HTTPRequest)
	}
	if h.JSONDecode != nil {
		reg("json_decode", h.JSONDecode)
	}
	if h.JSONEncode != nil {
		reg("json_encode", h.JSONEncode)
	}
	if h.FSRead != nil {
		reg("fs_read", h.FSRead)
	}
	if h.FSReadRange != nil {
		reg("fs_read_range", h.FSReadRange)
	}
	if h.FSWrite != nil {
		reg("fs_write", h.FSWrite)
	}
	if h.FSList != nil {
		reg("fs_list", h.FSList)
	}
	if h.ShellExec != nil {
		reg("shell_exec", h.ShellExec)
	}
	if h.LSPRegisterDiagnostic != nil {
		reg("lsp_register_diagnostic", h.LSPRegisterDiagnostic)
	}
	if h.LSPRegisterCompletion != nil {
		reg("lsp_register_completion", h.LSPRegisterCompletion)
	}
	if h.LLMChat != nil {
		reg("llm_chat", h.LLMChat)
	}
	if h.Log != nil {
		reg("log", h.Log)
	}
	if h.Sleep != nil {
		reg("sleep", h.Sleep)
	}
	if h.WSEmit != nil {
		reg("ws_emit", h.WSEmit)
	}
	if h.DBQuery != nil {
		reg("db_query", h.DBQuery)
	}
	if h.DBExecBatch != nil {
		reg("db_exec_batch", h.DBExecBatch)
	}
	if h.DBClose != nil {
		reg("db_close", h.DBClose)
	}
	if h.MCPCall != nil {
		reg("mcp_call", h.MCPCall)
	}
	if h.HookOn != nil {
		// hook_on(eventName, jsCallback)：goja 对 (string,error) 多返回值
		// 自动适配不可靠，手动接收 goja.Value 并 AssertFunction 包装。
		reg("hook_on", func(event string, cb goja.Value) error {
			if cb == nil || goja.IsUndefined(cb) || goja.IsNull(cb) {
				return fmt.Errorf("hook_on: 回调不能为空")
			}
			fn, ok := goja.AssertFunction(cb)
			if !ok {
				return fmt.Errorf("hook_on: 第二个参数必须是函数")
			}
			return h.HookOn(event, func(ctxJSON string) (string, error) {
				res, err := fn(goja.Undefined(), e.vm.ToValue(ctxJSON))
				if err != nil {
					return "", err
				}
				if res == nil || goja.IsUndefined(res) || goja.IsNull(res) {
					return "", nil
				}
				return res.String(), nil
			})
		})
	}
	return nil
}

// FSReadRangeFn 是 fs_read_range 宿主函数的默认实现（seek 语义，单次 4MB）。
// 返回 [content, totalSize, error]。插件读大文件时用循环分片。
func FSReadRangeFn(path string, offset, length int64) (string, int64, error) {
	const maxChunk = 4 << 20
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := fi.Size()
	if offset < 0 {
		offset = 0
	}
	if offset > size {
		offset = size
	}
	if length <= 0 || length > maxChunk {
		length = maxChunk
	}
	if offset+length > size {
		length = size - offset
	}
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return "", size, err
	}
	return string(buf[:n]), size, nil
}

// RunScript 执行一段 JS 源码（已编译产物）。
func (e *Engine) RunScript(name, src string) error {
	if e.closed {
		return fmt.Errorf("engine closed")
	}
	_, err := e.vm.RunScript(name, src)
	if err != nil {
		return fmt.Errorf("run script %s: %w", name, err)
	}
	return nil
}

// Call 调用 JS 中的函数，args 为参数，返回 Go 值。
func (e *Engine) Call(fn string, args ...any) (any, error) {
	if e.closed {
		return nil, fmt.Errorf("engine closed")
	}
	f, ok := goja.AssertFunction(e.vm.Get(fn))
	if !ok {
		return nil, fmt.Errorf("function %q not found", fn)
	}
	var params []goja.Value
	for _, a := range args {
		params = append(params, e.vm.ToValue(a))
	}
	res, err := f(goja.Undefined(), params...)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", fn, err)
	}
	return res.Export(), nil
}

// Has 检查 JS 中是否存在某函数。
func (e *Engine) Has(fn string) bool {
	if e.closed {
		return false
	}
	_, ok := goja.AssertFunction(e.vm.Get(fn))
	return ok
}

// GlobalNames 返回全局对象上的所有属性名（含函数）。
func (e *Engine) GlobalNames() ([]string, error) {
	if e.closed {
		return nil, fmt.Errorf("engine closed")
	}
	val, err := e.vm.RunString("Object.getOwnPropertyNames(globalThis)")
	if err != nil {
		return nil, err
	}
	exp, ok := val.Export().([]any)
	if !ok {
		return nil, fmt.Errorf("global names: unexpected type %T", val.Export())
	}
	out := make([]string, 0, len(exp))
	for _, v := range exp {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// Close 释放引擎。
func (e *Engine) Close() {
	e.closed = true
}

// CompileTS 用 esbuild 把 TS 源码编译为 ES2018 JS（goja 兼容目标）。
func CompileTS(name, src string) (string, error) {
	result := api.Transform(src, api.TransformOptions{
		Loader:           api.LoaderTS,
		Target:           api.ES2018,
		Format:           api.FormatCommonJS,
		Sourcefile:       name,
		MinifyWhitespace: false,
		LegalComments:    api.LegalCommentsNone,
		LogLevel:         api.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Text)
		}
		return "", fmt.Errorf("esbuild transform %s: %v", name, msgs)
	}
	return string(result.Code), nil
}

// Timeout 包：给插件执行加超时（用 channel + timer 在外部实现，见 plugins.Loader）。
var _ = time.Second
