package wasmkern

// generic.go —— 「通用 ABI」：让任意 Zig 内核都能被插件通过宿主函数调用。
//
// 前面 count_tokens / bigram 都是为特定算法硬编码的调用封装，插件用不了。
// 这里定义一套**统一、极小**的内存契约，任何 Zig→wasm 内核只要照做，
// 就能被 wasm_invoke() 直接调用，不需要改 Go 侧代码。
//
// ── 内核侧契约 ───────────────────────────────────────────────
//
//  1) 可选导出 kernel_data_end() -> u32
//     返回「内核静态数据区最后一个字节的地址 + 1」。
//     Go 侧把输入字节写在它之后，避免覆盖内核的全局变量/哈希表。
//     不导出时退回默认 dataOffset(1024)。
//
//  2) 导出目标函数，两种形态任选：
//     fn(in_ptr: u32, in_len: u32) -> u32   // 需要输入
//     fn() -> u32                            // 不需要输入
//     返回值是**响应头的地址**（0 表示空输出）。
//
//  3) 响应头是线性内存里的两个小端 u32：[out_ptr, out_len]。
//     Go 侧据此把 out_len 字节读出来作为返回给插件的结果。
//
// 这套契约的好处：
//   - 只有「一次边界跨越」，不来回调用；
//   - 内核自己决定输出放哪（栈/静态区/动态 grow 都行）；
//   - Go 侧无需知道内核内部结构，纯字节进出，天然支持二进制结果。
//
// 示例（internal/wasmkern/upper.zig 是可直接参照的最小实现）。

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// GenericDataEnd 是内核可选导出的函数名，用于告知静态数据区结束地址。
const GenericDataEnd = "kernel_data_end"

// InvokeBytes 用通用 ABI 调用内核任意导出函数：输入一段字节，返回一段字节。
//
// 契约见本文件顶部注释。内核行为异常（返回 0 以外的野地址、越界响应头）
// 一律返回 error，且该实例会被丢弃——出错后内核内部状态未知，不可复用。
func (m *Manager) InvokeBytes(name, fn string, in []byte) ([]byte, error) {
	if disabled() {
		return nil, errDisabled
	}
	if m == nil {
		return nil, errors.New("wasmkern: 管理器为 nil")
	}
	k, err := m.acquire(name)
	if err != nil {
		return nil, err
	}
	healthy := false
	defer func() {
		if healthy {
			m.release(name, k)
		} else {
			_ = k.Close()
		}
	}()

	base, err := k.regionBase()
	if err != nil {
		return nil, err
	}
	if err := k.ensure(base + uint32(len(in)) + 64); err != nil {
		return nil, err
	}
	if len(in) > 0 && !k.mem.Write(base, in) {
		return nil, errors.New("wasmkern: 写入线性内存失败")
	}

	f := k.mod.ExportedFunction(fn)
	if f == nil {
		return nil, fmt.Errorf("wasmkern: 内核 %q 未导出函数 %q", name, fn)
	}
	def := f.Definition()
	nparams := len(def.ParamTypes())

	var args []uint64
	switch nparams {
	case 0:
		// 无需输入的内核
	case 2:
		args = []uint64{uint64(base), uint64(len(in))}
	default:
		return nil, fmt.Errorf("wasmkern: 内核 %q 的函数 %q 参数个数为 %d，通用 ABI 只支持 0 或 2 个 u32 参数",
			name, fn, nparams)
	}

	res, err := f.Call(context.Background(), args...)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		healthy = true
		return nil, nil // 无返回值视为空输出
	}
	hdrPtr := uint32(res[0])
	if hdrPtr == 0 {
		healthy = true
		return nil, nil
	}

	hdr, ok := k.mem.Read(hdrPtr, 8)
	if !ok {
		return nil, fmt.Errorf("wasmkern: 内核 %q 的响应头地址 %d 越界", name, hdrPtr)
	}
	outPtr := binary.LittleEndian.Uint32(hdr[0:4])
	outLen := binary.LittleEndian.Uint32(hdr[4:8])
	if outLen == 0 {
		healthy = true
		return nil, nil
	}
	buf, ok := k.mem.Read(outPtr, outLen)
	if !ok {
		return nil, fmt.Errorf("wasmkern: 内核 %q 的输出区 [%d,+%d) 越界", name, outPtr, outLen)
	}
	// 必须拷贝：buf 指向模块线性内存，实例归还池后可能被下一次调用覆盖。
	out := make([]byte, len(buf))
	copy(out, buf)
	healthy = true
	return out, nil
}

// regionBase 返回数据传输区起点：优先用内核自报的 kernel_data_end，
// 否则用默认 dataOffset。
func (k *Kernel) regionBase() (uint32, error) {
	if k.mod.ExportedFunction(GenericDataEnd) == nil {
		return dataOffset, nil
	}
	return k.dataEnd(GenericDataEnd)
}

// InvokeBytes 是 Default() 上的便捷方法，供宿主函数直接调用。
func InvokeBytes(name, fn string, in []byte) ([]byte, error) {
	return std.InvokeBytes(name, fn, in)
}

// ListKernels 列出所有已内嵌的内核名（去掉 .wasm 后缀）。
// 插件可用它做能力探测：拿不到内核就降级到纯 JS 实现。
func ListKernels() []string { return std.ListKernels() }

// ListKernels 列出管理器可见的内核名。
func (m *Manager) ListKernels() []string {
	if m == nil {
		return nil
	}
	entries, err := assets.ReadDir(".")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if len(n) > 5 && n[len(n)-5:] == ".wasm" {
			names = append(names, n[:len(n)-5])
		}
	}
	return names
}
