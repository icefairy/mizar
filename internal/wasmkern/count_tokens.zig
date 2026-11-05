// count_tokens.zig — Zig wasm 计算内核：token 估算
//
// 语义与 internal/engine/stdlib.go 的 countTokensEstimate 完全一致：
//   CJK (U+4E00..U+9FFF) 按字符计 1 token，其余字符按 4 字符/token 向上取整。
// 之所以下沉到 wasm：该函数在压缩器（compactor.go）与压缩触发判断里都是热路径，
// 而 goja 解释执行同类循环比原生慢近 9 倍；wasm 能拿到接近原生的性能，
// 且天然沙箱、不破坏「单文件纯静态」的构建前提（无 cgo）。
//
// 构建（由 Makefile zig-kernels 目标调用）：
//   zig build-exe count_tokens.zig -target wasm32-freestanding -OReleaseFast \
//       --name count_tokens -fno-entry -rdynamic -femit-bin=count_tokens.wasm
//
// 导出契约（Go 侧 internal/wasmkern 依赖，改名会让内核失效）：
//   count_tokens(ptr: usize, len: usize) -> usize
//     ptr/len 指向 wasm 线性内存中的 UTF-8 字节；返回估算 token 数。

export fn count_tokens(ptr: [*]const u8, len: usize) usize {
    var cjk: usize = 0;
    var chars: usize = 0;
    var i: usize = 0;
    while (i < len) {
        const b = ptr[i];
        if (b < 0x80) {
            i += 1;
            chars += 1;
            continue;
        }
        // 解码 UTF-8 多字节序列；非法起始字节按单字符计入（与 Go range 的替换行为对齐）
        var n: usize = 1;
        var cp: u32 = 0;
        if (b & 0xE0 == 0xC0) {
            n = 2;
            cp = b & 0x1F;
        } else if (b & 0xF0 == 0xE0) {
            n = 3;
            cp = b & 0x0F;
        } else if (b & 0xF8 == 0xF0) {
            n = 4;
            cp = b & 0x07;
        } else {
            i += 1;
            chars += 1;
            continue;
        }
        if (i + n > len) break; // 截断的多字节序列：忽略，与 utf8 解码到结尾一致
        var k: usize = 1;
        while (k < n) : (k += 1) cp = (cp << 6) | (ptr[i + k] & 0x3F);
        if (cp >= 0x4E00 and cp <= 0x9FFF) cjk += 1;
        i += n;
        chars += 1;
    }
    return cjk + (chars - cjk + 3) / 4;
}

// 保留一个空 _start，便于 wasm 模块实例化时可用默认 start 函数（当前不启用）。
export fn _start() void {}
