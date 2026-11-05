package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"mizar/internal/wasmkern"
)

// wasmhost.go —— 把 wasmkern 的「通用 ABI」暴露成插件可调用的宿主函数。
//
// 背景：形态 A（Zig→wasm 计算内核）此前只服务于内置算法（count_tokens、bigram），
// 插件作者完全用不上。有了 wasm_invoke 之后，插件可以自带 .zig 内核，
// 把重计算丢给编译好的 wasm，业务胶水仍用 JS 写——这是「Zig 编译产物
// 作为插件能力载体」在没有子进程、没有 cgo 前提下的落地方式。
//
// 注册的宿主函数：
//
//	wasm_invoke(kernel, fn, input)      -> string      文本进、文本出
//	wasm_invoke_b64(kernel, fn, b64)    -> string(base64) 二进制进、二进制出
//	wasm_kernels()                      -> string(JSON) 可用内核清单
//
// 内核契约见 internal/wasmkern/generic.go。

func registerWasm(reg func(name string, fn any)) {
	reg("wasm_invoke", func(kernel, fn, input string) (string, error) {
		if kernel == "" || fn == "" {
			return "", fmt.Errorf("wasm_invoke: kernel 与 fn 不能为空")
		}
		out, err := wasmkern.InvokeBytes(kernel, fn, []byte(input))
		if err != nil {
			return "", err
		}
		if !utf8.Valid(out) {
			return "", fmt.Errorf("wasm_invoke: %s/%s 输出不是合法 UTF-8（%d 字节），请用 wasm_invoke_b64", kernel, fn, len(out))
		}
		return string(out), nil
	})

	reg("wasm_invoke_b64", func(kernel, fn, inputB64 string) (string, error) {
		if kernel == "" || fn == "" {
			return "", fmt.Errorf("wasm_invoke_b64: kernel 与 fn 不能为空")
		}
		in, err := base64.StdEncoding.DecodeString(inputB64)
		if err != nil {
			return "", fmt.Errorf("wasm_invoke_b64: 输入不是合法 base64: %w", err)
		}
		out, err := wasmkern.InvokeBytes(kernel, fn, in)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(out), nil
	})

	reg("wasm_kernels", func() (string, error) {
		b, err := json.Marshal(wasmkern.ListKernels())
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}
