// bigram.zig — Zig wasm 计算内核：中文 bigram + 英文整词分词（零分配）
//
// 背景：plug-in memory.ts 里的 tokenize() 在 goja 中解释执行，2000 条记忆
// 的检索实测 1453ms（纯 Go 同算法 43ms，差 34×）。瓶颈是每次分 token 都
// 新建 JS 字符串：单次 tokenize 就 154 次分配 / 21µs。
//
// 本内核用「切出 span（offset,len）指回输入缓冲区」的方式做，全程零分配：
//   - 输入的 UTF-8 字节就在线性内存里，token 只记位置，不复制内容。
//   - 去重 + 停用词过滤都在内核内部的静态哈希表里完成，不产生任何映射对象。
//   - 输出是一串 u32 对 (offset, len)，Go 侧按需切出字符串。
//
// 语义与 memory.ts 的 tokenize() 严格对齐：
//   - CJK(U+4E00..U+9FFF, U+3400..U+4DBF)：输出 unigram，紧邻 CJK 时再输出 bigram
//   - 连续 ASCII 字母/数字：长度>=2 输出整词，长度==1 且是 [a-z0-9] 才输出
//   - 其他字符视为分隔符
//   - ASCII 比较按小写进行（等价于 JS 的 toLowerCase）
//   - 命中停用词表跳过；重复 token 只保留首次出现
//
// 构建（Makefile zig-kernels 调用）：
//   zig build-exe bigram.zig -target wasm32-freestanding -OReleaseFast \
//       --name bigram -fno-entry -rdynamic -femit-bin=bigram.wasm
//
// 导出契约（Go 侧 internal/wasmkern 依赖，改名即失效）：
//   bigram_tokenize(ptr, len, out, out_cap) -> usize
//     ptr/len  输入 UTF-8 字节
//     out      输出区，按 (offset, len) 成对写 u32，最多 out_cap 个 u32
//     返回     写入的 token 个数（不是 u32 字数）
//   _start()  空入口

/// 哈希表槽位数，必须是 2 的幂。取 8192 足够覆盖单次检索的 query + 单条记忆。
const SLOTS: usize = 8192;

/// 哈希表用「代际戳」做 O(1) 清空：
///   gen[i] 记录槽 i 属于哪一代，idx[i] 存 token 在 out 数组里的下标。
///   每次调用把 cur_gen 加一，旧代际的槽自动视为空——不必 memset 整张表。
/// 这很关键：每次调用清 32KB 在「2000 条记忆逐条分词」的场景下就是 64MB
/// 无谓的 memset，实测正是它吃掉了内核的全部优势。
var table_gen: [SLOTS]u32 = undefined;
var table_idx: [SLOTS]u32 = undefined;
var cur_gen: u32 = 0;
/// 首次调用时把两张表清零。放在函数里做而不是声明时初始化，
/// 是为了避免 64KB 的零值进 wasm 数据段撑大产物。
var inited: bool = false;

fn hashSpan(p: [*]const u8, start: usize, n: usize) u32 {
    // FNV-1a，同时把 ASCII 大写折成小写参与哈希，保证大小写不敏感
    var h: u32 = 2166136261;
    var i: usize = 0;
    while (i < n) : (i += 1) {
        var b = p[start + i];
        if (b >= 'A' and b <= 'Z') b += 32;
        h = (h ^ @as(u32, b)) *% 16777619;
    }
    return h;
}

/// 大小写不敏感比较两个 span。用于哈希命中后的精确比对。
fn spanEq(p: [*]const u8, a: usize, alen: usize, b: usize, blen: usize) bool {
    if (alen != blen) return false;
    var i: usize = 0;
    while (i < alen) : (i += 1) {
        var x = p[a + i];
        var y = p[b + i];
        if (x >= 'A' and x <= 'Z') x += 32;
        if (y >= 'A' and y <= 'Z') y += 32;
        if (x != y) return false;
    }
    return true;
}

fn isCJK(cp: u32) bool {
    return (cp >= 0x4E00 and cp <= 0x9FFF) or (cp >= 0x3400 and cp <= 0x4DBF);
}

/// 解码 ptr[i] 处的 UTF-8 码点，返回 (codepoint, 字节数)。
/// 非法序列返回 (0xFFFD, 1)，与 JS 逐字符遍历的行为接近。
fn decode(p: [*]const u8, i: usize, len: usize) struct { cp: u32, n: usize } {
    const b = p[i];
    if (b < 0x80) return .{ .cp = b, .n = 1 };
    var n: usize = 0;
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
    } else return .{ .cp = 0xFFFD, .n = 1 };
    if (i + n > len) return .{ .cp = 0xFFFD, .n = 1 };
    var k: usize = 1;
    while (k < n) : (k += 1) {
        const c = p[i + k];
        if (c & 0xC0 != 0x80) return .{ .cp = 0xFFFD, .n = 1 };
        cp = (cp << 6) | @as(u32, c & 0x3F);
    }
    return .{ .cp = cp, .n = n };
}

/// 停用词表：与 memory.ts 的 STOPWORDS 完全一致。
/// 以「内容 + 长度」形式内联，避免运行时构造字符串。
const STOP: []const []const u8 = &.{
    "的", "了", "是", "在", "和", "与", "我", "你", "他", "它", "这", "那",
    "有", "没", "不", "就", "也", "都", "一个", "什么", "怎么", "如何", "可以",
    "a",  "an", "the", "is", "are", "of", "to", "and", "or", "in", "on", "for",
};

fn isStopword(p: [*]const u8, start: usize, n: usize) bool {
    for (STOP) |w| {
        if (w.len != n) continue;
        var ok = true;
        var i: usize = 0;
        while (i < n) : (i += 1) {
            var x = p[start + i];
            var y = w[i];
            if (x >= 'A' and x <= 'Z') x += 32;
            if (y >= 'A' and y <= 'Z') y += 32;
            if (x != y) {
                ok = false;
                break;
            }
        }
        if (ok) return true;
    }
    return false;
}

/// 尝试把一个 span 加入结果集。重复或停用词返回 false。
fn push(p: [*]const u8, start: usize, n: usize, out: [*]u32, count: *usize, out_cap: usize) void {
    if (n == 0) return;
    if (isStopword(p, start, n)) return;
    if ((count.* + 1) * 2 > out_cap) return; // 输出区满，静默丢弃

    const h = hashSpan(p, start, n);
    var idx = h & (SLOTS - 1);
    while (true) {
        if (table_gen[idx] != cur_gen) break; // 本代未使用 → 新 token
        const ti = table_idx[idx] * 2;
        const ts = out[ti];
        const tl = out[ti + 1];
        if (spanEq(p, ts, tl, start, n)) return; // 已存在
        idx = (idx + 1) & (SLOTS - 1);
    }

    const pos = count.*;
    out[pos * 2] = @intCast(start);
    out[pos * 2 + 1] = @intCast(n);
    table_gen[idx] = cur_gen;
    table_idx[idx] = @intCast(pos);
    count.* = pos + 1;
}

export fn bigram_tokenize(ptr: [*]const u8, len: usize, out: [*]u32, out_cap: usize) usize {
    if (!inited) {
        var z: usize = 0;
        while (z < SLOTS) : (z += 1) {
            table_gen[z] = 0;
            table_idx[z] = 0;
        }
        inited = true;
        cur_gen = 0;
    }
    cur_gen +%= 1;
    if (cur_gen == 0) {
        // 回绕：清一次表，保证 0 不被当作有效代际
        var z: usize = 0;
        while (z < SLOTS) : (z += 1) table_gen[z] = 0;
        cur_gen = 1;
    }

    var count: usize = 0;
    var i: usize = 0;
    var ascii_start: usize = 0;
    var ascii_len: usize = 0;

    i = 0;
    while (i < len) {
        const d = decode(ptr, i, len);
        if (isCJK(d.cp)) {
            // 先冲刷累积的 ASCII 单词
            if (ascii_len > 0) {
                push(ptr, ascii_start, ascii_len, out, &count, out_cap);
                ascii_len = 0;
            }
            push(ptr, i, d.n, out, &count, out_cap);
            // 紧跟 CJK 时输出 bigram
            const j = i + d.n;
            if (j < len) {
                const d2 = decode(ptr, j, len);
                if (isCJK(d2.cp)) push(ptr, i, d.n + d2.n, out, &count, out_cap);
            }
        } else if (d.cp >= 'a' and d.cp <= 'z' or d.cp >= 'A' and d.cp <= 'Z' or d.cp >= '0' and d.cp <= '9') {
            if (ascii_len == 0) ascii_start = i;
            ascii_len += d.n;
        } else {
            if (ascii_len > 0) {
                push(ptr, ascii_start, ascii_len, out, &count, out_cap);
                ascii_len = 0;
            }
        }
        i += d.n;
    }
    if (ascii_len > 0) push(ptr, ascii_start, ascii_len, out, &count, out_cap);
    return count;
}

/// 返回内核静态数据（哈希表等）在线性内存中的结束地址。
/// Go 侧把数据传输区放在这个地址之后，避免覆盖内核自己的全局变量。
export fn kernel_data_end() usize {
    // 必须覆盖**所有**静态全局变量（两张表 + cur_gen + inited），
    // 否则 Go 侧从这块之后写输入会踩掉 cur_gen/inited，导致
    // 第一次调用正常、之后每次返回 0（历史 bug）。
    var end: usize = 0;
    end = @max(end, @intFromPtr(&table_gen) + @sizeOf(@TypeOf(table_gen)));
    end = @max(end, @intFromPtr(&table_idx) + @sizeOf(@TypeOf(table_idx)));
    end = @max(end, @intFromPtr(&cur_gen) + @sizeOf(@TypeOf(cur_gen)));
    end = @max(end, @intFromPtr(&inited) + @sizeOf(@TypeOf(inited)));
    return end;
}

export fn _start() void {}
