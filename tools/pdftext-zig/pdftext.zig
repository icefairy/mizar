//! pdftext.zig — 单文件、零依赖的 PDF 文本提取器（纯 Zig 标准库）。
//!
//! 用途：作为 `mizar read` 的轻量旁路，或独立命令行工具：
//!   zig build-exe pdftext.zig -OReleaseSmall --name pdftext
//!   ./pdftext file.pdf [选项]
//!
//! 依赖 Zig 0.15+（使用 std.Io / std.process.Init 新 API）。
//! 本项目内置工具链为 0.17.1，`mizar /zig` 可释放同版本。
//!
//! 支持的 PDF 特性（覆盖国内简历/公文类 WPS、iText 导出文件）：
//!   * 传统 xref 表 + xref 流（PDF 1.5+，含 /Prev 链）
//!   * 对象流 ObjStm（压缩对象）
//!   * 页面树（含继承的 Resources、缺 /Count 的畸形树）
//!   * 内容流为单个流或**流数组**
//!   * FlateDecode（含 PNG / TIFF predictor）
//!   * Form XObject 递归（`q ... /Fm Do`）
//!   * Type0/CID 字体 + Identity-H + ToUnicode CMap（bfchar/bfrange/数组目标）
//!   * 简单字体（WinAnsi/MacRoman/Differences/ToUnicode）
//!   * 水印图层（可选内容组名含 watermark/水印）自动跳过
//!
//! 不支持的：加密 PDF、LZW/JPX 等滤镜、扫描件 OCR（无文本层）。

const std = @import("std");

// ───────────────────────────── 数据结构 ─────────────────────────────

/// PDF 对象（解析后的值）。
const Value = union(enum) {
    null,
    boolean: bool,
    integer: i64,
    real: f64,
    string: []const u8, // 已解码转义的原始字节（非 UTF-8 文本）
    name: []const u8,
    array: []const Value,
    dict: *Dict,
    stream: *Stream,
    reference: struct { num: u32, gen: u16 },

    fn asInt(self: Value) ?i64 {
        return switch (self) {
            .integer => |v| v,
            .real => |v| @intFromFloat(v),
            else => null,
        };
    }
    fn asFloat(self: Value) ?f64 {
        return switch (self) {
            .integer => |v| @floatFromInt(v),
            .real => |v| v,
            else => null,
        };
    }
    fn asName(self: Value) ?[]const u8 {
        return switch (self) {
            .name => |v| v,
            else => null,
        };
    }
    fn isNull(self: Value) bool {
        return switch (self) {
            .null => true,
            else => false,
        };
    }
};

/// 统一错误集合（避免递归函数推断出的错误集互相依赖）。
const PdfError = error{
    OutOfMemory,
    MalformedPDF,
    NotAPDF,
    UnsupportedFilter,
};

const Dict = struct {
    entries: std.StringArrayHashMapUnmanaged(Value) = .empty,

    fn get(self: *const Dict, key: []const u8) ?Value {
        return self.entries.get(key);
    }
};

const Stream = struct {
    dict: *Dict,
    /// 原始（可能仍压缩）的流数据在文件中的字节区间。
    start: usize,
    len: usize,
};

/// 交叉引用表项。
const XrefEntry = union(enum) {
    free,
    /// 普通对象：文件偏移。
    offset: usize,
    /// 位于对象流中：objstm 的对象号 + 序号。
    in_stream: struct { stream_num: u32, index: u32 },
};

// ───────────────────────────── 词法分析 ─────────────────────────────

const Lexer = struct {
    data: []const u8,
    pos: usize = 0,

    const Token = union(enum) {
        eof,
        number: struct { v: f64, is_int: bool },
        string: []const u8,
        hex_string: []const u8,
        name: []const u8,
        keyword: []const u8,
        array_open,
        array_close,
        dict_open,
        dict_close,
    };

    fn isWhitespace(c: u8) bool {
        return switch (c) {
            ' ', '\t', '\r', '\n', 0x0c, 0x00 => true,
            else => false,
        };
    }
    fn isDelimiter(c: u8) bool {
        return switch (c) {
            '(', ')', '<', '>', '[', ']', '{', '}', '/', '%' => true,
            else => false,
        };
    }

    fn skipWhitespace(self: *Lexer) void {
        while (self.pos < self.data.len) {
            const c = self.data[self.pos];
            if (isWhitespace(c)) {
                self.pos += 1;
            } else if (c == '%') {
                // 单行注释
                while (self.pos < self.data.len and self.data[self.pos] != '\n' and self.data[self.pos] != '\r') {
                    self.pos += 1;
                }
            } else break;
        }
    }

    /// 读取下一个 token。字符串/十六进制串返回的是指向 data 的切片（未解码转义）。
    fn next(self: *Lexer) Token {
        self.skipWhitespace();
        if (self.pos >= self.data.len) return .eof;
        const c = self.data[self.pos];
        switch (c) {
            '[' => {
                self.pos += 1;
                return .array_open;
            },
            ']' => {
                self.pos += 1;
                return .array_close;
            },
            '<' => {
                if (self.pos + 1 < self.data.len and self.data[self.pos + 1] == '<') {
                    self.pos += 2;
                    return .dict_open;
                }
                return self.readHexString();
            },
            '>' => {
                if (self.pos + 1 < self.data.len and self.data[self.pos + 1] == '>') {
                    self.pos += 2;
                    return .dict_close;
                }
                self.pos += 1;
                return .{ .keyword = ">" };
            },
            '/' => return self.readName(),
            '(' => return self.readLiteralString(),
            else => {},
        }
        if (isWhitespace(c) or isDelimiter(c)) {
            self.pos += 1;
            return .{ .keyword = "" };
        }
        return self.readNumberOrKeyword();
    }

    fn readName(self: *Lexer) Token {
        // 跳过 '/'
        self.pos += 1;
        const start = self.pos;
        while (self.pos < self.data.len) {
            const c = self.data[self.pos];
            if (isWhitespace(c) or isDelimiter(c)) break;
            self.pos += 1;
        }
        return .{ .name = self.data[start..self.pos] };
    }

    fn readNumberOrKeyword(self: *Lexer) Token {
        const start = self.pos;
        while (self.pos < self.data.len) {
            const c = self.data[self.pos];
            if (isWhitespace(c) or isDelimiter(c)) break;
            self.pos += 1;
        }
        const tok = self.data[start..self.pos];
        if (tok.len == 0) {
            self.pos += 1;
            return .{ .keyword = "" };
        }
        // 判断是否为数字（整数或小数，允许前导 +- 和 '.'）
        var has_digit = false;
        var is_number = true;
        var is_int = true;
        for (tok, 0..) |ch, i| {
            switch (ch) {
                '0'...'9' => has_digit = true,
                '.' => is_int = false,
                '+', '-' => if (i != 0) {
                    is_number = false;
                    break;
                },
                else => {
                    is_number = false;
                    break;
                },
            }
        }
        if (is_number and has_digit) {
            const v = std.fmt.parseFloat(f64, tok) catch {
                return .{ .keyword = tok };
            };
            return .{ .number = .{ .v = v, .is_int = is_int } };
        }
        return .{ .keyword = tok };
    }

    fn readHexString(self: *Lexer) Token {
        // 跳过 '<'
        self.pos += 1;
        const start = self.pos;
        while (self.pos < self.data.len and self.data[self.pos] != '>') {
            self.pos += 1;
        }
        const raw = self.data[start..self.pos];
        if (self.pos < self.data.len) self.pos += 1;
        return .{ .hex_string = raw };
    }

    fn readLiteralString(self: *Lexer) Token {
        // 跳过 '('
        self.pos += 1;
        const start = self.pos;
        var depth: usize = 1;
        while (self.pos < self.data.len) {
            const c = self.data[self.pos];
            if (c == '\\') {
                self.pos += 2;
                continue;
            }
            if (c == '(') depth += 1;
            if (c == ')') {
                depth -= 1;
                if (depth == 0) break;
            }
            self.pos += 1;
        }
        const raw = self.data[start..self.pos];
        if (self.pos < self.data.len) self.pos += 1;
        return .{ .string = raw };
    }
};

/// 把 PDF 字面量字符串的转义序列解码为原始字节。
fn decodeLiteralString(a: std.mem.Allocator, raw: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);
    var i: usize = 0;
    while (i < raw.len) {
        const c = raw[i];
        if (c != '\\') {
            try out.append(a, c);
            i += 1;
            continue;
        }
        i += 1;
        if (i >= raw.len) break;
        const e = raw[i];
        i += 1;
        switch (e) {
            'n' => try out.append(a, '\n'),
            'r' => try out.append(a, '\r'),
            't' => try out.append(a, '\t'),
            'b' => try out.append(a, 0x08),
            'f' => try out.append(a, 0x0c),
            '(', ')', '\\' => try out.append(a, e),
            '\n' => {},
            '\r' => {
                if (i < raw.len and raw[i] == '\n') i += 1;
            },
            '0'...'7' => {
                var v: u32 = e - '0';
                var k: usize = 0;
                while (k < 2 and i < raw.len and raw[i] >= '0' and raw[i] <= '7') : (k += 1) {
                    v = v * 8 + (raw[i] - '0');
                    i += 1;
                }
                try out.append(a, @truncate(v));
            },
            else => try out.append(a, e),
        }
    }
    return out.toOwnedSlice(a);
}

/// 把十六进制串解码为原始字节。
fn decodeHexString(a: std.mem.Allocator, raw: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);
    var hi: ?u8 = null;
    for (raw) |c| {
        const v: u8 = switch (c) {
            '0'...'9' => c - '0',
            'a'...'f' => c - 'a' + 10,
            'A'...'F' => c - 'A' + 10,
            else => continue,
        };
        if (hi) |h| {
            try out.append(a, h << 4 | v);
            hi = null;
        } else {
            hi = v;
        }
    }
    if (hi) |h| try out.append(a, h << 4);
    return out.toOwnedSlice(a);
}

// ───────────────────────────── 语法分析 ─────────────────────────────

const Parser = struct {
    lex: Lexer,
    a: std.mem.Allocator,
    /// 对象号 -> 值 的缓存（避免重复解析）
    cache: *std.AutoHashMap(u32, Value),

    const ParseError = error{ OutOfMemory, MalformedPDF };

    /// 解析一个完整对象；遇到 "N G R" 形式返回 reference。
    fn parseValue(self: *Parser) ParseError!Value {
        const tok = self.lex.next();
        switch (tok) {
            .eof => return .null,
            .number => |num| {
                const v = num.v;
                // 可能是 "num gen R" 引用：向后看两个 token
                if (num.is_int) {
                    const save = self.lex.pos;
                    const t2 = self.lex.next();
                    if (t2 == .number and t2.number.is_int) {
                        const save2 = self.lex.pos;
                        const t3 = self.lex.next();
                        if (t3 == .keyword and std.mem.eql(u8, t3.keyword, "R")) {
                            return .{ .reference = .{
                                .num = @intFromFloat(@max(v, 0)),
                                .gen = @intFromFloat(@max(t2.number.v, 0)),
                            } };
                        }
                        self.lex.pos = save2;
                    }
                    self.lex.pos = save;
                }
                if (num.is_int) return .{ .integer = @intFromFloat(v) };
                return .{ .real = v };
            },
            .string => |raw| return .{ .string = try decodeLiteralString(self.a, raw) },
            .hex_string => |raw| return .{ .string = try decodeHexString(self.a, raw) },
            .name => |n| return .{ .name = n },
            .array_open => {
                var items: std.ArrayList(Value) = .empty;
                errdefer items.deinit(self.a);
                while (true) {
                    const save = self.lex.pos;
                    const t = self.lex.next();
                    if (t == .array_close) break;
                    if (t == .eof) break;
                    self.lex.pos = save;
                    try items.append(self.a, try self.parseValue());
                }
                return .{ .array = try items.toOwnedSlice(self.a) };
            },
            .dict_open => {
                const d = try self.a.create(Dict);
                d.* = .{};
                while (true) {
                    const save = self.lex.pos;
                    const t = self.lex.next();
                    if (t == .dict_close) break;
                    if (t == .eof) break;
                    self.lex.pos = save;
                    const kt = self.lex.next();
                    if (kt != .name) {
                        // 跳过无法识别的键
                        _ = try self.parseValue();
                        continue;
                    }
                    const v = try self.parseValue();
                    try d.entries.put(self.a, kt.name, v);
                }
                return .{ .dict = d };
            },
            .keyword => |kw| {
                if (std.mem.eql(u8, kw, "true")) return .{ .boolean = true };
                if (std.mem.eql(u8, kw, "false")) return .{ .boolean = false };
                if (std.mem.eql(u8, kw, "null")) return .null;
                return .{ .name = kw }; // 容错：裸关键字当名字
            },
            .array_close, .dict_close => return .null,
        }
    }
};

// ───────────────────────────── 主文档 ─────────────────────────────

pub const Document = struct {
    a: std.mem.Allocator,
    data: []const u8,
    xref: std.AutoHashMap(u32, XrefEntry),
    trailer: Value,
    /// 已解析对象缓存（含对象流内的）
    cache: std.AutoHashMap(u32, Value),
    /// 对象流解压缓存：stream 对象号 -> 解析后的对象数组
    objstm_cache: std.AutoHashMap(u32, void),
    /// 对象流/临时流的解压缓冲保留表。
    /// 关键点：Parser 产出的 Value 里，名字/字符串都是**指向源缓冲的切片**，
    /// 若解压缓冲提前释放，这些切片就变成悬垂指针（use-after-free，会随机段错误）。
    /// 因此这些缓冲必须活到 Document 销毁为止。
    retained: std.ArrayList([]u8) = .empty,

    pub fn open(a: std.mem.Allocator, data: []const u8) !Document {
        var doc = Document{
            .a = a,
            .data = data,
            .xref = std.AutoHashMap(u32, XrefEntry).init(a),
            .trailer = .null,
            .cache = std.AutoHashMap(u32, Value).init(a),
            .objstm_cache = std.AutoHashMap(u32, void).init(a),
        };
        try doc.loadXref();
        return doc;
    }

    pub fn deinit(self: *Document) void {
        self.xref.deinit();
        self.cache.deinit();
        self.objstm_cache.deinit();
        for (self.retained.items) |buf| self.a.free(buf);
        self.retained.deinit(self.a);
    }

    /// 定位文件末尾的 startxref 并加载整条 xref 链。
    fn loadXref(self: *Document) !void {
        const data = self.data;
        if (!std.mem.startsWith(u8, data, "%PDF-")) return error.NotAPDF;
        const idx = std.mem.lastIndexOf(u8, data, "startxref") orelse return error.MalformedPDF;
        var lx: Lexer = .{ .data = data, .pos = idx + "startxref".len };
        const tok = lx.next();
        if (tok != .number) return error.MalformedPDF;
        var offset: usize = @intFromFloat(@max(tok.number.v, 0));

        var guard: usize = 0;
        while (guard < 64) : (guard += 1) {
            const next = try self.loadXrefSection(offset);
            if (next == null) break;
            offset = next.?;
        }
    }

    /// 加载单个 xref 段，返回 /Prev 偏移（若有）。
    fn loadXrefSection(self: *Document, offset: usize) PdfError!?usize {
        if (offset >= self.data.len) return error.MalformedPDF;
        var lx: Lexer = .{ .data = self.data, .pos = offset };
        const tok = lx.next();
        if (tok == .keyword and std.mem.eql(u8, tok.keyword, "xref")) {
            // 传统 xref 表
            while (true) {
                const save = lx.pos;
                const t = lx.next();
                if (t == .keyword and std.mem.eql(u8, t.keyword, "trailer")) break;
                if (t != .number) {
                    lx.pos = save;
                    break;
                }
                const start_num: u32 = @intFromFloat(@max(t.number.v, 0));
                const cnt_tok = lx.next();
                if (cnt_tok != .number) break;
                const count: u32 = @intFromFloat(@max(cnt_tok.number.v, 0));
                var i: u32 = 0;
                while (i < count) : (i += 1) {
                    const off_tok = lx.next();
                    const gen_tok = lx.next();
                    const type_tok = lx.next();
                    if (off_tok != .number or gen_tok != .number) break;
                    const num = start_num + i;
                    const is_free = type_tok == .keyword and type_tok.keyword.len > 0 and type_tok.keyword[0] == 'f';
                    if (!self.xref.contains(num)) {
                        if (is_free) {
                            try self.xref.put(num, .free);
                        } else {
                            try self.xref.put(num, .{ .offset = @intFromFloat(@max(off_tok.number.v, 0)) });
                        }
                    }
                }
            }
            // 解析 trailer 字典
            const save = lx.pos;
            var p: Parser = .{ .lex = lx, .a = self.a, .cache = &self.cache };
            const trailer = p.parseValue() catch .null;
            lx = p.lex;
            if (trailer == .dict) {
                if (self.trailer == .null) self.trailer = trailer;
                if (trailer.dict.get("XRefStm")) |xs| {
                    if (xs.asInt()) |o| {
                        _ = self.loadXrefSection(@intCast(o)) catch {};
                    }
                }
                if (trailer.dict.get("Prev")) |pv| {
                    if (pv.asInt()) |o| return @intCast(o);
                }
            } else {
                lx.pos = save;
            }
            return null;
        }

        // xref 流（PDF 1.5+）：跳过 "N G obj" 头
        lx.pos = offset;
        var head: Lexer = .{ .data = self.data, .pos = offset };
        const h1 = head.next();
        const h2 = head.next();
        const h3 = head.next();
        if (h1 == .number and h2 == .number and h3 == .keyword and std.mem.eql(u8, h3.keyword, "obj")) {
            lx.pos = head.pos;
        }
        var p: Parser = .{ .lex = lx, .a = self.a, .cache = &self.cache };
        const obj = try p.parseValue();
        if (obj != .dict) return error.MalformedPDF;
        // 期望 "stream" 关键字
        var lx2 = p.lex;
        const st = lx2.next();
        if (!(st == .keyword and st.keyword.len > 0 and st.keyword[0] == 's')) return error.MalformedPDF;
        // 跳过 EOL
        if (lx2.pos < self.data.len and self.data[lx2.pos] == '\r') {
            lx2.pos += 1;
            if (lx2.pos < self.data.len and self.data[lx2.pos] == '\n') lx2.pos += 1;
        } else if (lx2.pos < self.data.len and self.data[lx2.pos] == '\n') {
            lx2.pos += 1;
        }
        const dict = obj.dict;
        const stream = Stream{ .dict = dict, .start = lx2.pos, .len = 0 };
        // 解压并解析 xref 数据
        const raw = self.readStreamData(stream) catch return error.MalformedPDF;
        defer self.a.free(raw);
        try self.parseXrefStreamData(dict, raw);
        if (self.trailer == .null) self.trailer = obj;
        if (dict.get("Prev")) |pv| {
            if (pv.asInt()) |o| return @intCast(o);
        }
        return null;
    }

    fn parseXrefStreamData(self: *Document, dict: *Dict, raw: []const u8) PdfError!void {
        const w = dict.get("W") orelse return;
        if (w != .array or w.array.len < 3) return;
        var widths: [3]usize = .{ 0, 0, 0 };
        for (0..3) |i| {
            widths[i] = @intCast(@max(w.array[i].asInt() orelse 0, 0));
        }
        const size: u32 = @intCast(@max((dict.get("Size") orelse Value{ .integer = 0 }).asInt() orelse 0, 0));
        const row_len = widths[0] + widths[1] + widths[2];
        if (row_len == 0) return;

        const parseIndexRange = struct {
            fn run(doc: *Document, r: []const u8, start: u32, count: u32, widths_in: [3]usize) PdfError!void {
                const rl = widths_in[0] + widths_in[1] + widths_in[2];
                var pos: usize = 0;
                var i: u32 = 0;
                while (i < count and pos + rl <= r.len) : (i += 1) {
                    var a0: u64 = 0;
                    var a1: u64 = 0;
                    var a2: u64 = 0;
                    for (0..widths_in[0]) |k| a0 = a0 << 8 | r[pos + k];
                    pos += widths_in[0];
                    for (0..widths_in[1]) |k| a1 = a1 << 8 | r[pos + k];
                    pos += widths_in[1];
                    for (0..widths_in[2]) |k| a2 = a2 << 8 | r[pos + k];
                    pos += widths_in[2];
                    const typ = if (widths_in[0] == 0) @as(u64, 1) else a0;
                    const num = start + i;
                    if (doc.xref.contains(num)) continue;
                    switch (typ) {
                        0 => try doc.xref.put(num, .free),
                        1 => try doc.xref.put(num, .{ .offset = @intCast(a1) }),
                        2 => try doc.xref.put(num, .{ .in_stream = .{
                            .stream_num = @intCast(a1),
                            .index = @intCast(a2),
                        } }),
                        else => {},
                    }
                }
            }
        };

        if (dict.get("Index")) |idxv| {
            if (idxv == .array and idxv.array.len >= 2) {
                var i: usize = 0;
                while (i + 1 < idxv.array.len) : (i += 2) {
                    const s: u32 = @intCast(@max(idxv.array[i].asInt() orelse 0, 0));
                    const c: u32 = @intCast(@max(idxv.array[i + 1].asInt() orelse 0, 0));
                    try parseIndexRange.run(self, raw, s, c, widths);
                }
                return;
            }
        }
        try parseIndexRange.run(self, raw, 0, size, widths);
    }

    /// 读取流的解码后数据（支持 FlateDecode + predictor）。
    fn readStreamData(self: *Document, s: Stream) PdfError![]u8 {
        var len = s.len;
        if (len == 0) {
            // 从字典推长度
            const ln = (s.dict.get("Length") orelse Value{ .integer = 0 }).asInt() orelse 0;
            len = @intCast(@max(ln, 0));
            if (s.start + len > self.data.len) len = self.data.len - s.start;
        }
        if (s.start + len > self.data.len) len = self.data.len - s.start;
        const body = self.data[s.start .. s.start + len];

        const filters = s.dict.get("Filter");
        var filter_list: [4][]const u8 = undefined;
        var nf: usize = 0;
        if (filters) |f| switch (f) {
            .name => |n| {
                filter_list[0] = n;
                nf = 1;
            },
            .array => |arr| {
                for (arr) |x| {
                    if (nf >= filter_list.len) break;
                    if (x.asName()) |n| {
                        filter_list[nf] = n;
                        nf += 1;
                    }
                }
            },
            else => {},
        };

        const params = s.dict.get("DecodeParms");
        var cur = body;
        var allocated: ?[]u8 = null;
        for (0..nf) |i| {
            const fname = filter_list[i];
            if (std.mem.eql(u8, fname, "FlateDecode")) {
                const p = if (params) |pv| switch (pv) {
                    .array => |arr| if (i < arr.len) arr[i] else Value.null,
                    else => pv,
                } else Value.null;
                const out = try inflateWithPredictor(self.a, cur, p);
                if (allocated) |old| self.a.free(old);
                allocated = out;
                cur = out;
            } else if (std.mem.eql(u8, fname, "ASCIIHexDecode")) {
                const out = try decodeHexString(self.a, cur);
                if (allocated) |old| self.a.free(old);
                allocated = out;
                cur = out;
            } else {
                // 不支持的滤镜：原样返回
                break;
            }
        }
        if (allocated) |out| return out;
        // 返回一份拷贝，便于调用方统一 free
        return self.a.dupe(u8, cur);
    }

    /// 解析间接引用到具体值。
    fn resolve(self: *Document, v: Value) PdfError!Value {
        var cur = v;
        var guard: usize = 0;
        while (cur == .reference and guard < 32) : (guard += 1) {
            cur = try self.getObject(cur.reference.num);
        }
        return cur;
    }

    fn get(self: *Document, dict: *Dict, key: []const u8) PdfError!Value {
        const v = dict.get(key) orelse return .null;
        return self.resolve(v);
    }

    /// 获取指定对象号的值（含对象流内的压缩对象）。
    fn getObject(self: *Document, num: u32) PdfError!Value {
        if (self.cache.get(num)) |v| return v;
        const entry = self.xref.get(num) orelse {
            self.cache.put(num, .null) catch {};
            return .null;
        };
        const value: Value = switch (entry) {
            .free => .null,
            .offset => |off| blk: {
                if (off == 0 or off >= self.data.len) break :blk Value.null;
                const lx: Lexer = .{ .data = self.data, .pos = off };
                var p: Parser = .{ .lex = lx, .a = self.a, .cache = &self.cache };
                const obj_num_tok = p.lex.next();
                if (obj_num_tok != .number) break :blk Value.null;
                _ = p.lex.next(); // gen
                const kw = p.lex.next();
                if (!(kw == .keyword and std.mem.eql(u8, kw.keyword, "obj"))) break :blk Value.null;
                var val = p.parseValue() catch break :blk Value.null;
                // 检查是否紧跟 stream
                const save = p.lex.pos;
                const maybe_stream = p.lex.next();
                if (maybe_stream == .keyword and std.mem.eql(u8, maybe_stream.keyword, "stream")) {
                    var sp = p.lex.pos;
                    if (sp < self.data.len and self.data[sp] == '\r') {
                        sp += 1;
                        if (sp < self.data.len and self.data[sp] == '\n') sp += 1;
                    } else if (sp < self.data.len and self.data[sp] == '\n') {
                        sp += 1;
                    }
                    if (val == .dict) {
                        var slen: usize = 0;
                        if (val.dict.get("Length")) |lv| {
                            const rv = if (lv == .reference) (try self.getObject(lv.reference.num)) else lv;
                            if (rv.asInt()) |n| slen = @intCast(@max(n, 0));
                        }
                        if (slen == 0 or sp + slen > self.data.len) {
                            // 退化：找 endstream
                            if (std.mem.indexOfPos(u8, self.data, sp, "endstream")) |e| {
                                slen = e - sp;
                                if (slen > 0 and self.data[sp + slen - 1] == '\n') slen -= 1;
                                if (slen > 0 and self.data[sp + slen - 1] == '\r') slen -= 1;
                            }
                        }
                        const st = try self.a.create(Stream);
                        st.* = .{ .dict = val.dict, .start = sp, .len = slen };
                        val = .{ .stream = st };
                    }
                } else {
                    p.lex.pos = save;
                }
                break :blk val;
            },
            .in_stream => |is| blk: {
                // 从对象流里取出：需要解析整个 ObjStm
                break :blk (try self.getFromObjStm(is.stream_num, num)) orelse Value.null;
            },
        };
        self.cache.put(num, value) catch {};
        return value;
    }

    /// 从对象流中取出指定对象。
    fn getFromObjStm(self: *Document, stream_num: u32, wanted_num: u32) PdfError!?Value {
        const stream_obj = try self.getObject(stream_num);
        if (stream_obj != .stream) return null;
        // 缓冲必须保留（解析出的 Value 切片指向它），见 Document.retained 注释
        const raw = try self.readStreamData(stream_obj.stream.*);
        try self.retained.append(self.a, raw);

        const n = (stream_obj.stream.dict.get("N") orelse Value{ .integer = 0 }).asInt() orelse 0;
        const first = (stream_obj.stream.dict.get("First") orelse Value{ .integer = 0 }).asInt() orelse 0;

        // 头部是 N 对 "objnum offset"
        var lx: Lexer = .{ .data = raw[0..@min(raw.len, @as(usize, @intCast(@max(first, 0))))] };
        var offsets = std.AutoHashMap(u32, u32).init(self.a);
        defer offsets.deinit();
        var i: i64 = 0;
        while (i < n) : (i += 1) {
            const a_tok = lx.next();
            const b_tok = lx.next();
            if (a_tok != .number or b_tok != .number) break;
            const obj_num: u32 = @intFromFloat(@max(a_tok.number.v, 0));
            const off: u32 = @intFromFloat(@max(b_tok.number.v, 0));
            try offsets.put(obj_num, off);
        }
        const off = offsets.get(wanted_num) orelse return null;
        const base: usize = @intCast(@max(first, 0));
        const abs = base + @as(usize, off);
        if (abs >= raw.len) return null;
        const lx2: Lexer = .{ .data = raw, .pos = abs };
        var p: Parser = .{ .lex = lx2, .a = self.a, .cache = &self.cache };
        const v = p.parseValue() catch return null;
        return v;
    }

    // ── 页面遍历 ──

    fn catalog(self: *Document) !Value {
        if (self.trailer != .dict) return .null;
        return self.get(self.trailer.dict, "Root");
    }

    /// 返回页面对象（dict）列表，按文档顺序。
    pub fn pages(self: *Document) ![]Value {
        var out: std.ArrayList(Value) = .empty;
        errdefer out.deinit(self.a);
        const cat = try self.catalog();
        if (cat != .dict) return out.toOwnedSlice(self.a);
        const root = try self.get(cat.dict, "Pages");
        try self.walkPages(root, &out, 0);
        return out.toOwnedSlice(self.a);
    }

    fn walkPages(self: *Document, node: Value, out: *std.ArrayList(Value), depth: usize) PdfError!void {
        if (depth > 32) return;
        const v = try self.resolve(node);
        if (v != .dict) return;
        const typ = try self.get(v.dict, "Type");
        const is_page = if (typ.asName()) |n| std.mem.eql(u8, n, "Page") else false;
        if (is_page) {
            try out.append(self.a, v);
            return;
        }
        const kids = try self.get(v.dict, "Kids");
        if (kids == .array) {
            for (kids.array) |kid| {
                try self.walkPages(kid, out, depth + 1);
            }
        } else if (typ.isNull()) {
            // 缺 Type 但有 Kids 的畸形节点
            return;
        }
    }

    /// 沿 Parent 链继承属性（Resources、MediaBox）。
    fn inherited(self: *Document, page: *Dict, key: []const u8) PdfError!Value {
        var cur: ?*Dict = page;
        var depth: usize = 0;
        while (cur) |d| {
            if (depth > 32) break;
            depth += 1;
            const v = d.get(key) orelse {
                cur = null;
                break;
            };
            const rv = try self.resolve(v);
            if (!rv.isNull()) return rv;
            const pv = d.get("Parent") orelse break;
            const parent = try self.resolve(pv);
            cur = if (parent == .dict) parent.dict else null;
        }
        return .null;
    }
};

// ───────────────────────────── Flate 解压 + predictor ─────────────────────────────

fn inflateWithPredictor(a: std.mem.Allocator, input: []const u8, params: Value) error{OutOfMemory}![]u8 {
    var rd = std.Io.Reader.fixed(input);
    var buf: [std.compress.flate.max_window_len]u8 = undefined;
    var dec: std.compress.flate.Decompress = .init(&rd, .zlib, &buf);

    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);
    var wr = std.Io.Writer.Allocating.init(a);
    defer wr.deinit();
    _ = dec.reader.streamRemaining(&wr.writer) catch 0;
    const inflated = try wr.toOwnedSlice();
    defer a.free(inflated);

    // 处理 predictor
    if (params == .dict) {
        const pred = (params.dict.get("Predictor") orelse Value{ .integer = 1 }).asInt() orelse 1;
        if (pred > 1) {
            const columns: usize = @intCast(@max((params.dict.get("Columns") orelse Value{ .integer = 1 }).asInt() orelse 1, 1));
            const colors: usize = @intCast(@max((params.dict.get("Colors") orelse Value{ .integer = 1 }).asInt() orelse 1, 1));
            const bpc: usize = @intCast(@max((params.dict.get("BitsPerComponent") orelse Value{ .integer = 8 }).asInt() orelse 8, 1));
            const row_len = (columns * colors * bpc + 7) / 8;
            if (pred == 2) {
                return undoTIFF(a, inflated, row_len);
            }
            return undoPNG(a, inflated, row_len);
        }
    }
    return a.dupe(u8, inflated);
}

fn undoPNG(a: std.mem.Allocator, data: []const u8, row_len: usize) error{OutOfMemory}![]u8 {
    if (row_len == 0) return a.dupe(u8, data);
    const rows = data.len / (row_len + 1);
    var out = try a.alloc(u8, rows * row_len);
    errdefer a.free(out);
    const prev = try a.alloc(u8, row_len);
    defer a.free(prev);
    const cur = try a.alloc(u8, row_len);
    defer a.free(cur);
    @memset(prev, 0);

    var r: usize = 0;
    while (r < rows) : (r += 1) {
        const base = r * (row_len + 1);
        const ft = data[base];
        @memcpy(cur, data[base + 1 .. base + 1 + row_len]);
        switch (ft) {
            0 => {},
            1 => {
                var i: usize = 1;
                while (i < row_len) : (i += 1) cur[i] +%= cur[i - 1];
            },
            2 => {
                for (0..row_len) |i| cur[i] +%= prev[i];
            },
            3 => {
                for (0..row_len) |i| {
                    const left: u16 = if (i > 0) cur[i - 1] else 0;
                    cur[i] +%= @intCast((left + prev[i]) / 2);
                }
            },
            4 => {
                for (0..row_len) |i| {
                    const left: i16 = if (i > 0) cur[i - 1] else 0;
                    const up: i16 = prev[i];
                    const ul: i16 = if (i > 0) prev[i - 1] else 0;
                    cur[i] +%= @intCast(paeth(left, up, ul));
                }
            },
            else => {},
        }
        @memcpy(out[r * row_len .. (r + 1) * row_len], cur);
        @memcpy(prev, cur);
    }
    return out;
}

fn undoTIFF(a: std.mem.Allocator, data: []const u8, row_len: usize) error{OutOfMemory}![]u8 {
    if (row_len == 0) return a.dupe(u8, data);
    const rows = data.len / row_len;
    var out = try a.dupe(u8, data[0 .. rows * row_len]);
    errdefer a.free(out);
    var r: usize = 0;
    while (r < rows) : (r += 1) {
        const base = r * row_len;
        var i: usize = 1;
        while (i < row_len) : (i += 1) {
            out[base + i] +%= out[base + i - 1];
        }
    }
    return out;
}

fn paeth(a: i16, b: i16, c: i16) i16 {
    const p = a + b - c;
    const pa = @abs(p - a);
    const pb = @abs(p - b);
    const pc = @abs(p - c);
    if (pa <= pb and pa <= pc) return a;
    if (pb <= pc) return b;
    return c;
}

// ───────────────────────────── 字体解码 ─────────────────────────────

const TextEncoding = union(enum) {
    /// 每字节一个码，按 win/mac/pdfDoc 表映射
    simple: SimpleEncoding,
    /// CID 双字节
    cid: u16_len,
    /// 带 ToUnicode CMap
    cmap: *CMap,

    const u16_len = void;

    fn bytesPerCode(self: TextEncoding) usize {
        return switch (self) {
            .simple => 1,
            .cid => 2,
            .cmap => |m| m.bytes_per_code,
        };
    }
};

const SimpleEncoding = enum { win_ansi, mac_roman, pdf_doc, identity };

const CMap = struct {
    /// 原始字节码 -> UTF-8 文本
    map: std.StringHashMapUnmanaged([]u8) = .empty,
    bytes_per_code: usize = 2,

    fn decode(self: *CMap, a: std.mem.Allocator, raw: []const u8) ![]u8 {
        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(a);
        const step = self.bytes_per_code;
        var i: usize = 0;
        while (i < raw.len) {
            const end = @min(i + step, raw.len);
            const code = raw[i..end];
            if (self.map.get(code)) |txt| {
                try out.appendSlice(a, txt);
                i = end;
                continue;
            }
            // 无映射：按双字节大端兜底
            if (step == 2 and code.len == 2) {
                const u: u21 = @as(u21, code[0]) << 8 | code[1];
                if (u != 0) try appendCodepoint(&out, a, u);
            }
            i = end;
        }
        return out.toOwnedSlice(a);
    }
};

fn appendCodepoint(list: *std.ArrayList(u8), a: std.mem.Allocator, cp: u21) !void {
    var buf: [4]u8 = undefined;
    const n = std.unicode.utf8Encode(cp, &buf) catch {
        try list.append(a, '?');
        return;
    };
    try list.appendSlice(a, buf[0..n]);
}

/// 解析 ToUnicode CMap 数据。
fn parseCMap(a: std.mem.Allocator, data: []const u8) !*CMap {
    const m = try a.create(CMap);
    m.* = .{};

    // bfchar 与 bfrange 两类区块**各自独立扫描**：CMap 里两者顺序不固定
    // （TeX/ps2pdf 导出常是 bfrange 在前），用单一游标会漏掉其中一个。
    var detected_bf = false;
    var cur: usize = 0;
    while (std.mem.indexOfPos(u8, data, cur, "beginbfchar")) |p| {
        const end = std.mem.indexOfPos(u8, data, p, "endbfchar") orelse data.len;
        try parseBfCharSection(a, m, data[p + "beginbfchar".len .. end]);
        detected_bf = true;
        cur = end;
    }
    cur = 0;
    while (std.mem.indexOfPos(u8, data, cur, "beginbfrange")) |p| {
        const end = std.mem.indexOfPos(u8, data, p, "endbfrange") orelse data.len;
        try parseBfRangeSection(a, m, data[p + "beginbfrange".len .. end]);
        detected_bf = true;
        cur = end;
    }
    if (!detected_bf) {
        return m;
    }
    // 推断每个码位字节数（取映射键长度众数，缺省 2）
    var len_count: [5]usize = .{ 0, 0, 0, 0, 0 };
    var it = m.map.iterator();
    while (it.next()) |e| {
        if (e.key_ptr.*.len < len_count.len) len_count[e.key_ptr.*.len] += 1;
    }
    var best: usize = 2;
    var best_n: usize = 0;
    for (len_count, 0..) |c, l| {
        if (l == 0) continue;
        if (c > best_n) {
            best_n = c;
            best = l;
        }
    }
    m.bytes_per_code = best;
    return m;
}

/// 扫描一段文本中的所有 <hex> token。
fn hexTokens(a: std.mem.Allocator, body: []const u8) ![][]u8 {
    var out: std.ArrayList([]u8) = .empty;
    errdefer {
        for (out.items) |x| a.free(x);
        out.deinit(a);
    }
    var i: usize = 0;
    while (i < body.len) {
        if (body[i] != '<') {
            i += 1;
            continue;
        }
        var j = i + 1;
        while (j < body.len and body[j] != '>') j += 1;
        if (j >= body.len) break;
        try out.append(a, try decodeHexString(a, body[i + 1 .. j]));
        i = j + 1;
    }
    return out.toOwnedSlice(a);
}

/// UTF-16BE 字节串 -> UTF-8。
fn utf16beToUtf8(a: std.mem.Allocator, raw: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);
    var i: usize = 0;
    while (i + 1 < raw.len) : (i += 2) {
        const u: u21 = @as(u21, raw[i]) << 8 | raw[i + 1];
        if (u == 0) continue;
        if (u >= 0xD800 and u <= 0xDBFF and i + 3 < raw.len) {
            const lo: u21 = @as(u21, raw[i + 2]) << 8 | raw[i + 3];
            const cp: u21 = 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00);
            try appendCodepoint(&out, a, cp);
            i += 2;
            continue;
        }
        try appendCodepoint(&out, a, u);
    }
    return out.toOwnedSlice(a);
}

fn parseBfCharSection(a: std.mem.Allocator, m: *CMap, body: []const u8) !void {
    const toks = try hexTokens(a, body);
    defer {
        for (toks) |t| a.free(t);
        a.free(toks);
    }
    var i: usize = 0;
    while (i + 1 < toks.len) : (i += 2) {
        const src = toks[i];
        const dst = toks[i + 1];
        if (src.len == 0 or dst.len == 0) continue;
        // 目标为 0（占位）跳过
        var all_zero = true;
        for (dst) |b| {
            if (b != 0) {
                all_zero = false;
                break;
            }
        }
        if (all_zero) continue;
        const txt = try utf16beToUtf8(a, dst);
        if (txt.len == 0) {
            a.free(txt);
            continue;
        }
        const gop = try m.map.getOrPut(a, src);
        if (gop.found_existing) {
            a.free(txt);
        } else {
            gop.key_ptr.* = try a.dupe(u8, src);
            gop.value_ptr.* = txt;
        }
    }
}

fn parseBfRangeSection(a: std.mem.Allocator, m: *CMap, body: []const u8) !void {
    // 支持 <lo> <hi> <dst> 与 <lo> <hi> [<a> <b> ...]
    var i: usize = 0;
    var pending: [3][]u8 = undefined;
    var np: usize = 0;
    var dst_list = std.ArrayList([]u8).empty;
    defer dst_list.deinit(a);
    var in_array = false;

    while (i < body.len) {
        const c = body[i];
        if (c == '[') {
            in_array = true;
            i += 1;
            continue;
        }
        if (c == ']') {
            in_array = false;
            // 用完一轮
            if (np >= 2 and dst_list.items.len > 0) {
                try expandBfRange(a, m, pending[0], pending[1], dst_list.items);
            }
            for (dst_list.items) |x| a.free(x);
            dst_list.clearRetainingCapacity();
            np = 0;
            i += 1;
            continue;
        }
        if (c == '<') {
            var j = i + 1;
            while (j < body.len and body[j] != '>') j += 1;
            if (j >= body.len) break;
            const tok = try decodeHexString(a, body[i + 1 .. j]);
            i = j + 1;
            if (in_array) {
                try dst_list.append(a, tok);
                continue;
            }
            if (np < 2) {
                pending[np] = tok;
                np += 1;
            } else {
                // 单个目标
                const single = [_][]u8{tok};
                try expandBfRange(a, m, pending[0], pending[1], &single);
                a.free(tok);
                a.free(pending[0]);
                a.free(pending[1]);
                np = 0;
            }
            continue;
        }
        i += 1;
    }
    // 收尾
    if (np >= 2 and dst_list.items.len > 0) {
        try expandBfRange(a, m, pending[0], pending[1], dst_list.items);
    }
    if (np >= 1) a.free(pending[0]);
    if (np >= 2) a.free(pending[1]);
}

fn codeValueBytes(s: []const u8) u32 {
    var v: u32 = 0;
    for (s) |b| v = v << 8 | b;
    return v;
}

fn encodeCode(a: std.mem.Allocator, v: u32, width: usize) ![]u8 {
    const out = try a.alloc(u8, width);
    var x = v;
    var i: usize = width;
    while (i > 0) {
        i -= 1;
        out[i] = @truncate(x);
        x >>= 8;
    }
    return out;
}

fn expandBfRange(a: std.mem.Allocator, m: *CMap, lo: []const u8, hi: []const u8, dsts: []const []u8) !void {
    if (lo.len == 0 or lo.len != hi.len) return;
    const lo_v = codeValueBytes(lo);
    const hi_v = codeValueBytes(hi);
    if (hi_v < lo_v) return;
    const count = hi_v - lo_v;
    if (count > 65535) {
        // 异常区间：只取第一个
        if (dsts.len > 0) {
            const txt = try utf16beToUtf8(a, dsts[0]);
            if (txt.len > 0) {
                const key = try a.dupe(u8, lo);
                if (!m.map.contains(key)) try m.map.put(a, key, txt) else a.free(txt);
            } else a.free(txt);
        }
        return;
    }
    var k: u32 = 0;
    while (k <= count) : (k += 1) {
        const key = try encodeCode(a, lo_v + k, lo.len);
        var txt: []u8 = undefined;
        if (dsts.len > 1 and k < dsts.len) {
            txt = try utf16beToUtf8(a, dsts[k]);
        } else if (dsts.len > 0) {
            // 目标递增：最后一个 16bit 单元 + k
            var tmp = try a.dupe(u8, dsts[0]);
            if (tmp.len >= 2 and k > 0) {
                var u: u32 = @as(u32, tmp[tmp.len - 2]) << 8 | tmp[tmp.len - 1];
                u += k;
                tmp[tmp.len - 2] = @truncate(u >> 8);
                tmp[tmp.len - 1] = @truncate(u);
            }
            defer a.free(tmp);
            txt = try utf16beToUtf8(a, tmp);
        } else {
            a.free(key);
            continue;
        }
        if (txt.len == 0) {
            a.free(key);
            a.free(txt);
            continue;
        }
        if (m.map.contains(key)) {
            a.free(key);
            a.free(txt);
        } else {
            try m.map.put(a, key, txt);
        }
    }
}

/// WinAnsi 编码表（0x20~0xFF 常用部分；其余按 Latin-1 兜底）。
fn winAnsiToUtf8(list: *std.ArrayList(u8), a: std.mem.Allocator, b: u8) !void {
    if (b < 0x80) {
        try list.append(a, b);
        return;
    }
    const cp: u21 = switch (b) {
        0x80 => 0x20AC,
        0x82 => 0x201A,
        0x83 => 0x0192,
        0x84 => 0x201E,
        0x85 => 0x2026,
        0x86 => 0x2020,
        0x87 => 0x2021,
        0x88 => 0x02C6,
        0x89 => 0x2030,
        0x8A => 0x0160,
        0x8B => 0x2039,
        0x8C => 0x0152,
        0x8E => 0x017D,
        0x91 => 0x2018,
        0x92 => 0x2019,
        0x93 => 0x201C,
        0x94 => 0x201D,
        0x95 => 0x2022,
        0x96 => 0x2013,
        0x97 => 0x2014,
        0x98 => 0x02DC,
        0x99 => 0x2122,
        0x9A => 0x0161,
        0x9B => 0x203A,
        0x9C => 0x0153,
        0x9E => 0x017E,
        0x9F => 0x0178,
        else => b,
    };
    try appendCodepoint(list, a, cp);
}

// ───────────────────────────── 内容流解释 ─────────────────────────────

const TextPiece = struct {
    x: f64,
    y: f64,
    /// 文本右端 X（用于判断同一行内是否需要补空格）
    x_end: f64,
    /// 字号（用于按比例判断间距）
    size: f64,
    text: []const u8,
};

const ContentParser = struct {
    doc: *Document,
    a: std.mem.Allocator,
    pieces: std.ArrayList(TextPiece) = .empty,
    /// 可选内容组（水印）嵌套标记栈：true 表示该段应忽略
    bdc_stack: std.ArrayList(bool) = .empty,
    /// 下一个文本片段前是否需要补一个词间空格（由 TJ 的负位移触发）
    pending_space: bool = false,
    /// 已解析字体编码缓存：资源名 -> encoding
    font_enc_cache: std.StringHashMapUnmanaged(TextEncoding) = .empty,
    /// 字体宽度缓存（CID 用 /W）
    font_obj_cache: std.StringHashMapUnmanaged(*Dict) = .empty,

    fn deinit(self: *ContentParser) void {
        self.pieces.deinit(self.a);
        self.bdc_stack.deinit(self.a);
        self.font_enc_cache.deinit(self.a);
        self.font_obj_cache.deinit(self.a);
    }

    fn suppressed(self: *ContentParser) bool {
        for (self.bdc_stack.items) |v| {
            if (v) return true;
        }
        return false;
    }

    fn interpret(self: *ContentParser, content: []const u8, resources: Value, init_ctm: [6]f64, depth: usize) PdfError!void {
        if (depth > 8) return;
        var lex: Lexer = .{ .data = content };
        var operands = std.ArrayList(Value).empty;
        defer operands.deinit(self.a);

        var ctm: [6]f64 = init_ctm;
        var tm: [6]f64 = .{ 1, 0, 0, 1, 0, 0 };
        var tlm: [6]f64 = .{ 1, 0, 0, 1, 0, 0 };
        var font_size: f64 = 0;
        var leading: f64 = 0;
        var char_space: f64 = 0;
        var word_space: f64 = 0;
        var hscale: f64 = 1;
        var cur_enc: TextEncoding = .{ .simple = .win_ansi };
        var cur_font: ?*Dict = null;

        var gstack = std.ArrayList(struct {
            ctm: [6]f64,
            font_size: f64,
            leading: f64,
            char_space: f64,
            word_space: f64,
            hscale: f64,
            enc: TextEncoding,
            font: ?*Dict,
        }).empty;
        defer gstack.deinit(self.a);

        while (true) {
            const save = lex.pos;
            const tok = lex.next();
            if (tok == .eof) break;

            // 操作数
            switch (tok) {
                .array_open, .dict_open, .string, .hex_string, .name, .number => {
                    lex.pos = save;
                    var p: Parser = .{ .lex = lex, .a = self.a, .cache = &self.doc.cache };
                    const v = p.parseValue() catch .null;
                    lex = p.lex;
                    try operands.append(self.a, v);
                    continue;
                },
                .array_close, .dict_close => continue,
                .eof => break,
                .keyword => {},
            }
            if (tok.keyword.len == 0) continue;
            const op = tok.keyword;

            if (std.mem.eql(u8, op, "q")) {
                try gstack.append(self.a, .{
                    .ctm = ctm,
                    .font_size = font_size,
                    .leading = leading,
                    .char_space = char_space,
                    .word_space = word_space,
                    .hscale = hscale,
                    .enc = cur_enc,
                    .font = cur_font,
                });
            } else if (std.mem.eql(u8, op, "Q")) {
                if (gstack.items.len > 0) {
                    const g = gstack.pop().?;
                    ctm = g.ctm;
                    font_size = g.font_size;
                    leading = g.leading;
                    char_space = g.char_space;
                    word_space = g.word_space;
                    hscale = g.hscale;
                    cur_enc = g.enc;
                    cur_font = g.font;
                }
            } else if (std.mem.eql(u8, op, "cm")) {
                if (operands.items.len >= 6) {
                    var m: [6]f64 = undefined;
                    for (0..6) |i| m[i] = operands.items[i].asFloat() orelse 0;
                    ctm = matMul(m, ctm);
                }
            } else if (std.mem.eql(u8, op, "BT")) {
                tm = .{ 1, 0, 0, 1, 0, 0 };
                tlm = tm;
            } else if (std.mem.eql(u8, op, "ET")) {
                // nothing
            } else if (std.mem.eql(u8, op, "Tf")) {
                if (operands.items.len >= 2) {
                    const name = operands.items[0].asName() orelse "";
                    font_size = operands.items[1].asFloat() orelse 0;
                    const font_res = try self.doc.get(resources.dict, "Font");
                    if (font_res == .dict) {
                        const fv = try self.doc.get(font_res.dict, name);
                        if (fv == .dict) {
                            cur_font = fv.dict;
                            cur_enc = try self.encodingFor(name, fv.dict);
                        }
                    }
                }
            } else if (std.mem.eql(u8, op, "Td")) {
                if (operands.items.len >= 2) {
                    const tx = operands.items[0].asFloat() orelse 0;
                    const ty = operands.items[1].asFloat() orelse 0;
                    tlm = matMul(.{ 1, 0, 0, 1, tx, ty }, tlm);
                    tm = tlm;
                }
            } else if (std.mem.eql(u8, op, "TD")) {
                if (operands.items.len >= 2) {
                    const tx = operands.items[0].asFloat() orelse 0;
                    const ty = operands.items[1].asFloat() orelse 0;
                    leading = -ty;
                    tlm = matMul(.{ 1, 0, 0, 1, tx, ty }, tlm);
                    tm = tlm;
                }
            } else if (std.mem.eql(u8, op, "Tm")) {
                if (operands.items.len >= 6) {
                    for (0..6) |i| tm[i] = operands.items[i].asFloat() orelse 0;
                    tlm = tm;
                }
            } else if (std.mem.eql(u8, op, "TL")) {
                if (operands.items.len >= 1) leading = operands.items[0].asFloat() orelse 0;
            } else if (std.mem.eql(u8, op, "Tc")) {
                if (operands.items.len >= 1) char_space = operands.items[0].asFloat() orelse 0;
            } else if (std.mem.eql(u8, op, "Tw")) {
                if (operands.items.len >= 1) word_space = operands.items[0].asFloat() orelse 0;
            } else if (std.mem.eql(u8, op, "Tz")) {
                if (operands.items.len >= 1) hscale = (operands.items[0].asFloat() orelse 100) / 100;
            } else if (std.mem.eql(u8, op, "T*")) {
                tlm = matMul(.{ 1, 0, 0, 1, 0, -leading }, tlm);
                tm = tlm;
            } else if (std.mem.eql(u8, op, "Tj")) {
                if (operands.items.len >= 1) {
                    try self.showText(operands.items[0], &tm, &ctm, font_size, cur_enc, cur_font, char_space, word_space, hscale);
                }
            } else if (std.mem.eql(u8, op, "TJ")) {
                if (operands.items.len >= 1 and operands.items[0] == .array) {
                    for (operands.items[0].array) |item| {
                        if (item == .string) {
                            try self.showText(item, &tm, &ctm, font_size, cur_enc, cur_font, char_space, word_space, hscale);
                        } else if (item.asFloat()) |adj| {
                            const tx = -adj / 1000 * font_size * hscale;
                            // 大幅位移等价于词间空格（PDF 常见做法）
                            if (tx > font_size * 0.12 and !self.suppressed()) {
                                self.pending_space = true;
                            }
                            tm = matMul(.{ 1, 0, 0, 1, tx, 0 }, tm);
                        }
                    }
                }
            } else if (std.mem.eql(u8, op, "'")) {
                tlm = matMul(.{ 1, 0, 0, 1, 0, -leading }, tlm);
                tm = tlm;
                if (operands.items.len >= 1) {
                    try self.showText(operands.items[operands.items.len - 1], &tm, &ctm, font_size, cur_enc, cur_font, char_space, word_space, hscale);
                }
            } else if (std.mem.eql(u8, op, "\"")) {
                if (operands.items.len >= 3) {
                    word_space = operands.items[0].asFloat() orelse word_space;
                    char_space = operands.items[1].asFloat() orelse char_space;
                }
                tlm = matMul(.{ 1, 0, 0, 1, 0, -leading }, tlm);
                tm = tlm;
                if (operands.items.len >= 1) {
                    try self.showText(operands.items[operands.items.len - 1], &tm, &ctm, font_size, cur_enc, cur_font, char_space, word_space, hscale);
                }
            } else if (std.mem.eql(u8, op, "BMC")) {
                try self.bdc_stack.append(self.a, false);
            } else if (std.mem.eql(u8, op, "BDC")) {
                var is_wm = false;
                if (operands.items.len >= 2) {
                    if (operands.items[0].asName()) |tag| {
                        if (std.mem.eql(u8, tag, "OC")) {
                            if (operands.items[1].asName()) |prop_name| {
                                if (resources == .dict) {
                                    const props = try self.doc.get(resources.dict, "Properties");
                                    if (props == .dict) {
                                        const ocg = try self.doc.get(props.dict, prop_name);
                                        if (ocg == .dict) {
                                            const label = try self.doc.get(ocg.dict, "Name");
                                            if (label == .string) {
                                                const utf8 = try utf16beOrRaw(self.a, label.string);
                                                defer self.a.free(utf8);
                                                is_wm = isWatermark(utf8);
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                try self.bdc_stack.append(self.a, is_wm);
            } else if (std.mem.eql(u8, op, "EMC")) {
                if (self.bdc_stack.items.len > 0) _ = self.bdc_stack.pop();
            } else if (std.mem.eql(u8, op, "Do")) {
                if (operands.items.len >= 1) {
                    if (operands.items[0].asName()) |xname| {
                        try self.doXObject(xname, resources, ctm, depth);
                    }
                }
            }
            operands.clearRetainingCapacity();
        }
    }

    fn doXObject(self: *ContentParser, xname: []const u8, resources: Value, ctm: [6]f64, depth: usize) PdfError!void {
        if (resources != .dict) return;
        const xobjs = try self.doc.get(resources.dict, "XObject");
        if (xobjs != .dict) return;
        const xo = try self.doc.get(xobjs.dict, xname);
        if (xo != .stream) return;
        const subtype = try self.doc.get(xo.stream.dict, "Subtype");
        const sn = subtype.asName() orelse return;
        if (!std.mem.eql(u8, sn, "Form")) return;

        const data = try self.doc.readStreamData(xo.stream.*);
        defer self.a.free(data);

        var form_ctm = ctm;
        const mat = try self.doc.get(xo.stream.dict, "Matrix");
        if (mat == .array and mat.array.len == 6) {
            var m: [6]f64 = undefined;
            for (0..6) |i| m[i] = mat.array[i].asFloat() orelse 0;
            form_ctm = matMul(m, form_ctm);
        }
        var form_res = try self.doc.get(xo.stream.dict, "Resources");
        if (form_res.isNull()) form_res = resources;
        try self.interpret(data, form_res, form_ctm, depth + 1);
    }

    fn encodingFor(self: *ContentParser, res_name: []const u8, font: *Dict) PdfError!TextEncoding {
        if (self.font_enc_cache.get(res_name)) |e| {
            // 同名字体在不同资源字典可能不同，但实践中够用
            return e;
        }
        const enc = try self.computeEncoding(font);
        try self.font_enc_cache.put(self.a, try self.a.dupe(u8, res_name), enc);
        return enc;
    }

    fn computeEncoding(self: *ContentParser, font: *Dict) PdfError!TextEncoding {
        // 1) ToUnicode 优先
        if (font.get("ToUnicode")) |tuv| {
            const resolved = try self.doc.resolve(tuv);
            if (resolved == .stream) {
                if (self.doc.readStreamData(resolved.stream.*)) |data| {
                    defer self.a.free(data);
                    const cm = parseCMap(self.a, data) catch null;
                    if (cm) |m| {
                        if (m.map.count() > 0) return .{ .cmap = m };
                    }
                } else |_| {}
            }
        }
        // 2) Type0 -> CID 双字节
        const subtype = font.get("Subtype");
        const is_type0 = if (subtype) |s| blk: {
            const sn = s.asName() orelse break :blk false;
            break :blk std.mem.eql(u8, sn, "Type0");
        } else false;
        if (is_type0) return .{ .cid = {} };
        // 3) 简单字体按 Encoding 名
        if (font.get("Encoding")) |e| {
            const rv = try self.doc.resolve(e);
            if (rv.asName()) |n| {
                if (std.mem.eql(u8, n, "MacRomanEncoding")) return .{ .simple = .mac_roman };
                if (std.mem.eql(u8, n, "WinAnsiEncoding")) return .{ .simple = .win_ansi };
            }
        }
        return .{ .simple = .pdf_doc };
    }

    fn showText(
        self: *ContentParser,
        operand: Value,
        tm: *[6]f64,
        ctm: *[6]f64,
        font_size: f64,
        enc: TextEncoding,
        font: ?*Dict,
        char_space: f64,
        word_space: f64,
        hscale: f64,
    ) PdfError!void {
        _ = word_space;
        if (self.suppressed()) return;
        if (operand != .string) return;
        const raw = operand.string;
        if (raw.len == 0) return;

        const step = enc.bytesPerCode();
        var i: usize = 0;
        var decoded: std.ArrayList(u8) = .empty;
        defer decoded.deinit(self.a);
        var advance: f64 = 0;

        while (i < raw.len) {
            const end = @min(i + step, raw.len);
            const code = raw[i..end];
            // 解码文本
            switch (enc) {
                .cmap => |m| {
                    if (m.map.get(code)) |txt| {
                        try decoded.appendSlice(self.a, txt);
                    } else if (step == 2 and code.len == 2) {
                        const u: u21 = @as(u21, code[0]) << 8 | code[1];
                        if (u != 0) try appendCodepoint(&decoded, self.a, u);
                    }
                },
                .simple => |se| {
                    for (code) |b| {
                        switch (se) {
                            .win_ansi, .pdf_doc => try winAnsiToUtf8(&decoded, self.a, b),
                            .mac_roman => try winAnsiToUtf8(&decoded, self.a, b),
                            .identity => try decoded.append(self.a, b),
                        }
                    }
                },
                .cid => {
                    if (code.len == 2) {
                        const u: u21 = @as(u21, code[0]) << 8 | code[1];
                        if (u != 0) try appendCodepoint(&decoded, self.a, u);
                    }
                },
            }
            // 推进文本矩阵
            const w = self.glyphWidth(font, code);
            const tx = (w / 1000 * font_size + char_space) * hscale;
            tm.* = matMul(.{ 1, 0, 0, 1, tx, 0 }, tm.*);
            advance += tx;
            i = end;
        }
        if (decoded.items.len == 0) return;

        // 计算位置
        const trm = matMul(.{ font_size * hscale, 0, 0, font_size, 0, 0 }, matMul(tm.*, ctm.*));
        const x = trm[4];
        const y = trm[5];
        var txt = try self.a.dupe(u8, decoded.items);
        if (self.pending_space) {
            self.pending_space = false;
            const starts_with_space = txt.len > 0 and txt[0] == ' ';
            const last_is_space = self.pieces.items.len > 0 and
                self.pieces.items[self.pieces.items.len - 1].text.len > 0 and
                self.pieces.items[self.pieces.items.len - 1].text[self.pieces.items[self.pieces.items.len - 1].text.len - 1] == ' ';
            if (!starts_with_space and !last_is_space) {
                const merged = try self.a.alloc(u8, txt.len + 1);
                merged[0] = ' ';
                @memcpy(merged[1..], txt);
                self.a.free(txt);
                txt = merged;
            }
        }
        const dir: f64 = if (ctm.*[0] < 0) -1 else 1;
        try self.pieces.append(self.a, .{
            .x = x,
            .y = y,
            .x_end = x + advance * dir,
            .size = font_size,
            .text = txt,
        });
    }

    /// 取字形宽度（1/1000 文本空间单位）。
    fn glyphWidth(self: *ContentParser, font: ?*Dict, code: []const u8) f64 {
        const f = font orelse return 500;
        // 简单字体：/Widths + /FirstChar
        if (f.get("Widths")) |wv| {
            const rv = self.doc.resolve(wv) catch return 500;
            if (rv == .array) {
                const first = (f.get("FirstChar") orelse Value{ .integer = 0 }).asInt() orelse 0;
                const c: i64 = if (code.len == 1) code[0] else @intCast(codeValueBytes(code));
                const idx = c - first;
                if (idx >= 0 and idx < rv.array.len) {
                    return rv.array[@intCast(idx)].asFloat() orelse 500;
                }
            }
            return 500;
        }
        // CID：/DescendantFonts[0] 的 /W 与 /DW
        if (f.get("DescendantFonts")) |dv| {
            const rv = self.doc.resolve(dv) catch return 500;
            if (rv == .array and rv.array.len > 0) {
                const d = self.doc.resolve(rv.array[0]) catch return 500;
                if (d == .dict) {
                    const cid: i64 = @intCast(codeValueBytes(code));
                    const dw = (d.dict.get("DW") orelse Value{ .integer = 1000 }).asFloat() orelse 1000;
                    const wv = self.doc.resolve(d.dict.get("W") orelse Value.null) catch return dw;
                    if (wv == .array) {
                        var i: usize = 0;
                        while (i + 1 < wv.array.len) {
                            const first = wv.array[i].asInt() orelse 0;
                            const next = wv.array[i + 1];
                            if (next == .array) {
                                if (cid >= first and cid - first < next.array.len) {
                                    return next.array[@intCast(cid - first)].asFloat() orelse dw;
                                }
                                i += 2;
                            } else {
                                if (i + 2 >= wv.array.len) break;
                                const last = next.asInt() orelse 0;
                                if (cid >= first and cid <= last) {
                                    return wv.array[i + 2].asFloat() orelse dw;
                                }
                                i += 3;
                            }
                        }
                    }
                    return dw;
                }
            }
        }
        return 500;
    }
};

/// 常见连字/兼容字符规范化为普通字符，避免出现 ﬁ ﬂ 等残片。
fn normalizeLigatures(buf: []u8) []u8 {
    var n: usize = 0;
    var i: usize = 0;
    while (i < buf.len) {
        const rest = buf[i..];
        const rep: ?[]const u8 = blk: {
            if (std.mem.startsWith(u8, rest, "\u{fb01}")) break :blk "fi";
            if (std.mem.startsWith(u8, rest, "\u{fb02}")) break :blk "fl";
            if (std.mem.startsWith(u8, rest, "\u{fb03}")) break :blk "ffi";
            if (std.mem.startsWith(u8, rest, "\u{fb04}")) break :blk "ffl";
            if (std.mem.startsWith(u8, rest, "\u{00a0}")) break :blk " ";
            break :blk null;
        };
        if (rep) |r| {
            for (r) |c| {
                buf[n] = c;
                n += 1;
            }
            i += 3;
            continue;
        }
        buf[n] = buf[i];
        n += 1;
        i += 1;
    }
    return buf[0..n];
}

fn isWatermark(label: []const u8) bool {
    var buf: [128]u8 = undefined;
    const n = @min(label.len, buf.len);
    for (label[0..n], 0..) |c, i| {
        buf[i] = std.ascii.toLower(c);
    }
    const lower = buf[0..n];
    return std.mem.indexOf(u8, lower, "watermark") != null or
        std.mem.indexOf(u8, lower, "water mark") != null or
        std.mem.indexOf(u8, label, "水印") != null;
}

fn utf16beOrRaw(a: std.mem.Allocator, s: []const u8) ![]u8 {
    // PDF 文本串：带 BOM FEFF 则为 UTF-16BE
    if (s.len >= 2 and s[0] == 0xFE and s[1] == 0xFF) {
        return utf16beToUtf8(a, s[2..]);
    }
    return a.dupe(u8, s);
}

fn matMul(m: [6]f64, n: [6]f64) [6]f64 {
    // PDF 2D 矩阵： [a b c d e f]
    return .{
        m[0] * n[0] + m[1] * n[2],
        m[0] * n[1] + m[1] * n[3],
        m[2] * n[0] + m[3] * n[2],
        m[2] * n[1] + m[3] * n[3],
        m[4] * n[0] + m[5] * n[2] + n[4],
        m[4] * n[1] + m[5] * n[3] + n[5],
    };
}

// ───────────────────────────── 高层：抽取文本 ─────────────────────────────

pub const Options = struct {
    /// 每页之间是否插入换页符 \f
    page_break: bool = true,
    /// 同一行的 Y 容差（pt）
    line_tolerance: f64 = 1.5,
    /// 是否输出坐标（调试用）
    debug_coords: bool = false,
};


/// 康熙部首 U+2F00..U+2FD5 的 NFKC 归一目标（同 UCS 统一表意字符）。
/// 部分 PDF 的 ToUnicode CMap 会把常用汉字映射到「康熙部首」码位（例如
/// 「大」→ U+2F24），显示上像方框/异常字形，检索与匹配全部失效。
/// 这里做一次等价归一，把它们还原成正常汉字。
const kangxi_radicals = [214]u21{
    0x4E00, 0x4E28, 0x4E36, 0x4E3F, 0x4E59, 0x4E85, 0x4E8C, 0x4EA0,
    0x4EBA, 0x513F, 0x5165, 0x516B, 0x5182, 0x5196, 0x51AB, 0x51E0,
    0x51F5, 0x5200, 0x529B, 0x52F9, 0x5315, 0x531A, 0x5338, 0x5341,
    0x535C, 0x5369, 0x5382, 0x53B6, 0x53C8, 0x53E3, 0x56D7, 0x571F,
    0x58EB, 0x5902, 0x590A, 0x5915, 0x5927, 0x5973, 0x5B50, 0x5B80,
    0x5BF8, 0x5C0F, 0x5C22, 0x5C38, 0x5C6E, 0x5C71, 0x5DDB, 0x5DE5,
    0x5DF1, 0x5DFE, 0x5E72, 0x5E7A, 0x5E7F, 0x5EF4, 0x5EFE, 0x5F0B,
    0x5F13, 0x5F50, 0x5F61, 0x5F73, 0x5FC3, 0x6208, 0x6236, 0x624B,
    0x652F, 0x6534, 0x6587, 0x6597, 0x65A4, 0x65B9, 0x65E0, 0x65E5,
    0x66F0, 0x6708, 0x6728, 0x6B20, 0x6B62, 0x6B79, 0x6BB3, 0x6BCB,
    0x6BD4, 0x6BDB, 0x6C0F, 0x6C14, 0x6C34, 0x706B, 0x722A, 0x7236,
    0x723B, 0x723F, 0x7247, 0x7259, 0x725B, 0x72AC, 0x7384, 0x7389,
    0x74DC, 0x74E6, 0x7518, 0x751F, 0x7528, 0x7530, 0x758B, 0x7592,
    0x7676, 0x767D, 0x76AE, 0x76BF, 0x76EE, 0x77DB, 0x77E2, 0x77F3,
    0x793A, 0x79B8, 0x79BE, 0x7A74, 0x7ACB, 0x7AF9, 0x7C73, 0x7CF8,
    0x7F36, 0x7F51, 0x7F8A, 0x7FBD, 0x8001, 0x800C, 0x8012, 0x8033,
    0x807F, 0x8089, 0x81E3, 0x81EA, 0x81F3, 0x81FC, 0x820C, 0x821B,
    0x821F, 0x826E, 0x8272, 0x8278, 0x864D, 0x866B, 0x8840, 0x884C,
    0x8863, 0x897E, 0x898B, 0x89D2, 0x8A00, 0x8C37, 0x8C46, 0x8C55,
    0x8C78, 0x8C9D, 0x8D64, 0x8D70, 0x8DB3, 0x8EAB, 0x8ECA, 0x8F9B,
    0x8FB0, 0x8FB5, 0x9091, 0x9149, 0x91C6, 0x91CC, 0x91D1, 0x9577,
    0x9580, 0x961C, 0x96B6, 0x96B9, 0x96E8, 0x9751, 0x975E, 0x9762,
    0x9769, 0x97CB, 0x97ED, 0x97F3, 0x9801, 0x98A8, 0x98DB, 0x98DF,
    0x9996, 0x9999, 0x99AC, 0x9AA8, 0x9AD8, 0x9ADF, 0x9B25, 0x9B2F,
    0x9B32, 0x9B3C, 0x9B5A, 0x9CE5, 0x9E75, 0x9E7F, 0x9EA5, 0x9EBB,
    0x9EC3, 0x9ECD, 0x9ED1, 0x9EF9, 0x9EFD, 0x9F0E, 0x9F13, 0x9F20,
    0x9F3B, 0x9F4A, 0x9F52, 0x9F8D, 0x9F9C, 0x9FA0,
};

/// 把单个码位按 NFKC 规则归一（仅覆盖实际会遇到的兼容区段）。
fn normalizeCodepoint(cp: u21) u21 {
    // 康熙部首
    if (cp >= 0x2F00 and cp <= 0x2FD5) {
        return kangxi_radicals[cp - 0x2F00];
    }
    // CJK 部首补充（只有 2 个有等价汉字）
    if (cp == 0x2E9F) return 0x6BCD; // ⺟ -> 母
    if (cp == 0x2EF3) return 0x9F9F; // ⻳ -> 龟
    // 全角数字/字母 -> 半角（中文简历常混用全角字母数字）。
    // 注意：**不转换全角标点**（，：；（）" 等）——中文正文里它们是正常标点，
    // 转成半角反而破坏排版与检索。
    if ((cp >= 0xFF10 and cp <= 0xFF19) or
        (cp >= 0xFF21 and cp <= 0xFF3A) or
        (cp >= 0xFF41 and cp <= 0xFF5A))
    {
        return cp - 0xFEE0;
    }
    if (cp == 0x3000) return ' '; // 全角空格
    return cp;
}

/// 判断 UTF-8 字符是否为 CJK（中日韩）字符。
fn isCJK(cp: u21) bool {
    return (cp >= 0x2E80 and cp <= 0x9FFF) or // CJK 部首/统一表意
        (cp >= 0xF900 and cp <= 0xFAFF) or // 兼容表意
        (cp >= 0xFF00 and cp <= 0xFFEF) or // 全角标点
        (cp >= 0x3000 and cp <= 0x303F); // CJK 标点
}

fn decodeRuneAt(s: []const u8, i: usize) struct { cp: u21, len: usize } {
    const b = s[i];
    if (b < 0x80) return .{ .cp = b, .len = 1 };
    const n = std.unicode.utf8ByteSequenceLength(b) catch return .{ .cp = 0xFFFD, .len = 1 };
    if (i + n > s.len) return .{ .cp = 0xFFFD, .len = 1 };
    const cp = std.unicode.utf8Decode(s[i .. i + n]) catch 0xFFFD;
    return .{ .cp = cp, .len = n };
}

/// 清理输出：去掉中文之间的多余空格、去掉行尾空格。
///
/// 中文 PDF 常用逐字定位，字间天然存在间隙；若一律保留空格，
/// 会出现「姓   名： 唐智旺」这类难读结果。这里只在两侧均为 CJK 时
/// 丢弃空格；拉丁文本之间的空格一律保留。
fn cleanupText(a: std.mem.Allocator, raw: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);

    var i: usize = 0;
    while (i < raw.len) {
        const c = raw[i];
        if (c != ' ') {
            const r = decodeRuneAt(raw, i);
            const cp = normalizeCodepoint(r.cp);
            if (cp == r.cp) {
                try out.appendSlice(a, raw[i .. i + r.len]);
            } else {
                try appendCodepoint(&out, a, cp);
            }
            i += r.len;
            continue;
        }
        // 空格串：看看前后字符
        var j = i;
        while (j < raw.len and raw[j] == ' ') j += 1;
        const prev_cp: ?u21 = blk: {
            if (out.items.len == 0) break :blk null;
            // 回溯到上一个字符起点
            var k = out.items.len - 1;
            var back: usize = 0;
            while (k > 0 and back < 3 and (out.items[k] & 0xC0) == 0x80) : (back += 1) k -= 1;
            const r = decodeRuneAt(out.items, k);
            break :blk r.cp;
        };
        const next_cp: ?u21 = blk: {
            if (j >= raw.len) break :blk null;
            break :blk decodeRuneAt(raw, j).cp;
        };
        const drop = (prev_cp != null and next_cp != null and
            isCJK(prev_cp.?) and isCJK(next_cp.?)) or
            next_cp == null or // 文本结尾
            next_cp == '\n' or // 行尾空格
            prev_cp == null or // 行首空格
            prev_cp == '\n';
        if (!drop) {
            // 连续空格压成一个
            try out.append(a, ' ');
        }
        i = j;
    }
    return out.toOwnedSlice(a);
}

pub fn extractText(a: std.mem.Allocator, data: []const u8, opts: Options) ![]u8 {
    var doc = try Document.open(a, data);
    defer doc.deinit();

    const pages = try doc.pages();
    defer a.free(pages);

    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(a);

    for (pages, 0..) |page, pi| {
        if (pi > 0 and opts.page_break) try out.append(a, 0x0c);
        if (page != .dict) continue;

        var cp: ContentParser = .{ .doc = &doc, .a = a };
        defer cp.deinit();

        const resources = try doc.inherited(page.dict, "Resources");

        // Contents：单流或流数组
        const contents = try doc.get(page.dict, "Contents");
        switch (contents) {
            .stream => |s| {
                const body = doc.readStreamData(s.*) catch continue;
                defer a.free(body);
                try cp.interpret(body, resources, .{ 1, 0, 0, 1, 0, 0 }, 0);
            },
            .array => |arr| {
                for (arr) |c| {
                    const rv = doc.resolve(c) catch continue;
                    if (rv != .stream) continue;
                    const body = doc.readStreamData(rv.stream.*) catch continue;
                    defer a.free(body);
                    try cp.interpret(body, resources, .{ 1, 0, 0, 1, 0, 0 }, 0);
                }
            },
            else => {},
        }

        try renderPage(a, &out, cp.pieces.items, opts, pi + 1);
    }
    const raw = try out.toOwnedSlice(a);
    defer a.free(raw);
    return cleanupText(a, raw);
}

/// 判断是否为拉丁字母/数字（用于决定是否补词间空格）。
fn isLatinWordChar(c: u8) bool {
    return (c >= 'a' and c <= 'z') or (c >= 'A' and c <= 'Z') or (c >= '0' and c <= '9');
}

fn renderPage(a: std.mem.Allocator, out: *std.ArrayList(u8), pieces: []TextPiece, opts: Options, page_no: usize) !void {
    if (pieces.len == 0) return;
    // 按 Y 降序（PDF Y 轴向上）→ X 升序 稳定排序
    const Ctx = struct {
        tol: f64,
        fn lessThan(ctx: @This(), lhs: TextPiece, rhs: TextPiece) bool {
            if (@abs(lhs.y - rhs.y) < ctx.tol) return lhs.x < rhs.x;
            return lhs.y > rhs.y;
        }
    };
    std.mem.sort(TextPiece, pieces, Ctx{ .tol = opts.line_tolerance }, Ctx.lessThan);

    var last_y: f64 = pieces[0].y;
    var last_end_x: f64 = pieces[0].x;
    var last_size: f64 = if (pieces[0].size > 0) pieces[0].size else 10;
    var first = true;
    for (pieces) |p| {
        if (p.text.len == 0) continue;
        if (first) {
            first = false;
        } else if (@abs(p.y - last_y) >= opts.line_tolerance) {
            try out.append(a, '\n');
            last_y = p.y;
        }
        last_end_x = p.x_end;
        if (p.size > 0) last_size = p.size;
        if (opts.debug_coords) {
            var tmp: [128]u8 = undefined;
            const coord = try std.fmt.bufPrint(&tmp, "[p{d} {d:.0},{d:.0}] ", .{ page_no, p.x, p.y });
            try out.appendSlice(a, coord);
        }
        try out.appendSlice(a, normalizeLigatures(@constCast(p.text)));
    }
    if (out.items.len > 0 and out.items[out.items.len - 1] != '\n') try out.append(a, '\n');
}

// ───────────────────────────── CLI ─────────────────────────────

pub fn main(init: std.process.Init) !void {
    const a = init.gpa;
    const io = init.io;

    var stdout_buf: [64 * 1024]u8 = undefined;
    var stdout = std.Io.File.stdout().writer(io, &stdout_buf);
    const out = &stdout.interface;

    const argv = try init.minimal.args.toSlice(a);
    defer a.free(argv);

    var file_path: ?[]const u8 = null;
    var opts: Options = .{};
    var show_help = false;
    var show_pages = false;

    var i: usize = 1;
    while (i < argv.len) : (i += 1) {
        const arg = argv[i];
        if (std.mem.eql(u8, arg, "-h") or std.mem.eql(u8, arg, "--help")) {
            show_help = true;
        } else if (std.mem.eql(u8, arg, "--debug-coords") or std.mem.eql(u8, arg, "-d")) {
            opts.debug_coords = true;
        } else if (std.mem.eql(u8, arg, "--no-page-break")) {
            opts.page_break = false;
        } else if (std.mem.eql(u8, arg, "--pages") or std.mem.eql(u8, arg, "-p")) {
            show_pages = true;
        } else if (std.mem.eql(u8, arg, "--line-tolerance")) {
            i += 1;
            if (i < argv.len) opts.line_tolerance = std.fmt.parseFloat(f64, argv[i]) catch opts.line_tolerance;
        } else if (file_path == null) {
            file_path = arg;
        }
    }

    if (show_help or file_path == null) {
        try out.writeAll(
            \\pdftext — 纯 Zig 单文件 PDF 文本提取器
            \\
            \\用法: pdftext <file.pdf> [选项]
            \\
            \\选项:
            \\  -p, --pages           只列出页数与页面对象信息
            \\  -d, --debug-coords    每段文本前打印坐标 (x,y)
            \\      --no-page-break   页面之间不插入 \f
            \\      --line-tolerance  判定同行的 Y 容差（默认 1.5pt）
            \\  -h, --help            显示本帮助
            \\
        );
        try out.flush();
        return;
    }

    const path = file_path.?;
    const data = std.Io.Dir.cwd().readFileAlloc(io, path, a, .limited(512 * 1024 * 1024)) catch |err| {
        try out.print("读取失败 {s}: {t}\n", .{ path, err });
        try out.flush();
        std.process.exit(1);
    };
    defer a.free(data);

    if (show_pages) {
        var doc = try Document.open(a, data);
        defer doc.deinit();
        const pages = try doc.pages();
        defer a.free(pages);
        try out.print("页数: {d}, 文件大小: {d} 字节\n", .{ pages.len, data.len });
        for (pages, 0..) |pg, idx| {
            if (pg != .dict) {
                try out.print("  page {d}: <非字典节点>\n", .{idx + 1});
                continue;
            }
            if (pg.dict.get("Contents")) |cv| {
                switch (cv) {
                    .reference => |r| try out.print("  page {d}: Contents={d} 0 R\n", .{ idx + 1, r.num }),
                    .stream => try out.print("  page {d}: Contents=<直接流>\n", .{idx + 1}),
                    .array => |arr| try out.print("  page {d}: Contents=<数组 {d} 项>\n", .{ idx + 1, arr.len }),
                    else => try out.print("  page {d}: Contents=<{s}>\n", .{ idx + 1, @tagName(cv) }),
                }
            } else {
                try out.print("  page {d}: 无 Contents\n", .{idx + 1});
            }
            const mb = try doc.inherited(pg.dict, "MediaBox");
            var box_str: [64]u8 = undefined;
            var box: []const u8 = "未知";
            if (mb == .array and mb.array.len == 4) {
                box = try std.fmt.bufPrint(&box_str, "{d:.0}x{d:.0}", .{
                    (mb.array[2].asFloat() orelse 0) - (mb.array[0].asFloat() orelse 0),
                    (mb.array[3].asFloat() orelse 0) - (mb.array[1].asFloat() orelse 0),
                });
            }
            try out.print("  page {d}: MediaBox {s}\n", .{ idx + 1, box });
        }
        try out.flush();
        return;
    }

    const text = extractText(a, data, opts) catch |err| {
        try out.print("解析失败: {t}\n", .{err});
        try out.flush();
        std.process.exit(2);
    };
    defer a.free(text);

    try out.writeAll(text);
    try out.flush();
}

// ───────────────────────────── 单元测试 ─────────────────────────────

test "lexer: 基本 token" {
    var lx: Lexer = .{ .data = "1 0 obj << /Type /Page >> [ (hi) <4142> ]" };
    try std.testing.expect(lx.next() == .number);
    try std.testing.expect(lx.next() == .number);
    try std.testing.expect(lx.next() == .keyword);
    try std.testing.expect(lx.next() == .dict_open);
    try std.testing.expect(lx.next() == .name);
    try std.testing.expect(lx.next() == .name);
    try std.testing.expect(lx.next() == .dict_close);
    try std.testing.expect(lx.next() == .array_open);
    try std.testing.expect(lx.next() == .string);
    try std.testing.expect(lx.next() == .hex_string);
    try std.testing.expect(lx.next() == .array_close);
}

test "decodeHexString / decodeLiteralString" {
    const a = std.testing.allocator;
    {
        const out = try decodeHexString(a, "4E2D 6587");
        defer a.free(out);
        try std.testing.expectEqualSlices(u8, "\x4e\x2d\x65\x87", out);
    }
    {
        const out = try decodeLiteralString(a, "a\\050b\\)c\\n");
        defer a.free(out);
        try std.testing.expectEqualSlices(u8, "a(b)c\n", out);
    }
}

test "parseCMap: bfrange 在 bfchar 之前也能全部解析" {
    const a = std.testing.allocator;
    const cmap = 
        \\1 begincodespacerange
        \\<00> <FF>
        \\endcodespacerange
        \\1 beginbfrange
        \\<61> <63> <0061>
        \\endbfrange
        \\1 beginbfchar
        \\<20> <4E2D>
        \\endbfchar
    ;
    const m = try parseCMap(a, cmap);
    defer {
        var it = m.map.iterator();
        while (it.next()) |e| {
            a.free(e.key_ptr.*);
            a.free(e.value_ptr.*);
        }
        m.map.deinit(a);
        a.destroy(m);
    }
    try std.testing.expectEqualStrings("a", m.map.get("a").?);
    try std.testing.expectEqualStrings("b", m.map.get("b").?);
    try std.testing.expectEqualStrings("c", m.map.get("c").?);
    try std.testing.expectEqualStrings("中", m.map.get("\x20").?);
    try std.testing.expectEqual(@as(usize, 1), m.bytes_per_code);
}

test "utf16beToUtf8: 代理对" {
    const a = std.testing.allocator;
    const out = try utf16beToUtf8(a, "\xd8\x3d\xde\x00");
    defer a.free(out);
    try std.testing.expectEqualStrings("😀", out);
}

test "isWatermark" {
    try std.testing.expect(isWatermark("zp_watermark"));
    try std.testing.expect(isWatermark("Watermark"));
    try std.testing.expect(isWatermark("我的水印"));
    try std.testing.expect(!isWatermark("Layer 1"));
}

test "matMul: 平移叠加" {
    const m = matMul(.{ 1, 0, 0, 1, 10, 20 }, .{ 1, 0, 0, 1, 5, 5 });
    try std.testing.expectEqual(@as(f64, 15), m[4]);
    try std.testing.expectEqual(@as(f64, 25), m[5]);
}
