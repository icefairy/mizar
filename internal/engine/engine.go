// Package engine 封装 goja JS 引擎与 esbuild TS 编译管线。
package engine

import (
	"fmt"
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
	HTTPGet    func(url string) (string, error)
	HTTPPost   func(url, body string) (string, error)
	JSONDecode func(s string) (map[string]any, error)
	JSONEncode func(v any) (string, error)
	FSRead     func(path string) (string, error)
	FSWrite    func(path, content string) error
	FSList     func(dir string) ([]string, error)
	ShellExec  func(cmd string) (string, error)
	LLMChat  func(messagesJSON string) (string, error)
	Log        func(msg string)
	Sleep      func(ms int)
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
	if h.HTTPGet != nil {
		reg("http_get", h.HTTPGet)
	}
	if h.HTTPPost != nil {
		reg("http_post", h.HTTPPost)
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
	if h.FSWrite != nil {
		reg("fs_write", h.FSWrite)
	}
	if h.FSList != nil {
		reg("fs_list", h.FSList)
	}
	if h.ShellExec != nil {
		reg("shell_exec", h.ShellExec)
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
	return nil
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
		Loader:            api.LoaderTS,
		Target:            api.ES2018,
		Format:            api.FormatCommonJS,
		Sourcefile:        name,
		MinifyWhitespace:  false,
		LegalComments:     api.LegalCommentsNone,
		LogLevel:          api.LogLevelSilent,
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
