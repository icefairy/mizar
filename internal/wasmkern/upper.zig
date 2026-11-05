// upper.zig — 通用 ABI 参考内核：把输入字节里的 ASCII 小写字母转大写。
//
// 这是 internal/wasmkern/generic.go 那套「通用 ABI」的最小可运行示例，
// 也是新内核的模板：复制它改 fn 名与算法即可。
//
// 构建（推荐用 make zig-kernels，会自动遍历本目录所有 .zig）：
//   zig build-exe upper.zig -target wasm32-freestanding -OReleaseFast \
//       --name upper -fno-entry -rdynamic -femit-bin=upper.wasm
//
// 导出契约（generic.go 依赖，改名即失效）：
//   kernel_data_end() -> u32      静态数据区结束地址（可选，此处导出以示范）
//   upper(ptr, len) -> u32        返回响应头地址；响应头是两个 u32: [out_ptr, out_len]
//
// 内存策略：静态数组当输出缓冲区，容量 64KB，超长输入截断。
// 这样内核不需要堆分配器，产物极小——对「大输入」场景足够，
// 需要动态扩容时用 zig 的 FixedBufferAllocator 或直接 std.heap.wasm_allocator。

const std = @import("std");

const OUT_CAP = 64 * 1024;

var out_buf: [OUT_CAP]u8 = undefined;
var header: [2]u32 = undefined;

/// 告诉 Go 侧静态数据区到哪结束，输入从这之后开始写。
export fn kernel_data_end() u32 {
    return @intFromPtr(&out_buf) + OUT_CAP;
}

/// 通用 ABI 的主函数：读入 in_ptr/in_len，结果写入 out_buf，返回 header 地址。
export fn upper(in_ptr: [*]const u8, in_len: usize) u32 {
    const n = @min(in_len, OUT_CAP);
    var i: usize = 0;
    while (i < n) : (i += 1) {
        const c = in_ptr[i];
        out_buf[i] = if (c >= 'a' and c <= 'z') c - 32 else c;
    }
    header[0] = @intFromPtr(&out_buf);
    header[1] = @intCast(n);
    return @intFromPtr(&header);
}
