// server.zig — 一个零依赖的 MCP server（Model Context Protocol），用 Zig 编写。
//
// 为什么用 Zig 写 MCP server：Zig 编译产物是单个静态二进制，无运行时依赖，
// 交叉编译一条命令搞定（-target aarch64-linux）。把它配进 Mizar 的
// config.json 的 mcp_servers 后，插件就能通过 mcp_call() 调用它的工具——
// 这是「Zig 二进制作为插件能力载体」的形态 B：
//   - 真并发、真系统调用（io_uring / epoll / mmap / 原始 socket）
//   - 重计算（编解码 / 压缩 / 加密 / 图像）不必在 goja 里慢慢解释
//   - 与 Mizar 主进程隔离，崩了不影响 Agent
//
// 协议：JSON-RPC 2.0，按行分隔（每行一个 JSON）。Mizar 的 stdio 传输就是这么实现的
// （见 internal/mcp/mcp.go 的 StdioTransport）。必须实现的方法：
//   initialize     握手（返回 protocolVersion / capabilities / serverInfo）
//   tools/list     返回工具清单（name / description / inputSchema）
//   tools/call     调用工具（参数 params.name + params.arguments）
//
// 构建（在 Mizar 里直接让 Agent 跑这条命令即可，zig 已在 PATH 中）：
//   zig build-exe server.zig -OReleaseFast -femit-bin=myserver
//
// 注册到 Mizar：
//   /mcp add myserver /绝对路径/myserver
// 或手改 ~/.mizar/config.json：
//   "mcp_servers": [{"name":"myserver","command":"/绝对路径/myserver"}]
//
// 改完代码重新编译覆盖同名文件后，执行 /mcp reload 即热生效（会杀掉旧子进程重连）。

const std = @import("std");

/// 一次分配器：MCP 消息都是短命的，整个进程用一个 arena 足够，
/// 每轮清空（deinit 后重新 init），不做逐条精细释放。
var arena_buf: [1 << 20]u8 = undefined;
var fba = std.heap.FixedBufferAllocator.init(&arena_buf);

fn writeAll(fd: std.posix.fd_t, data: []const u8) void {
    var off: usize = 0;
    while (off < data.len) {
        const n = std.posix.system.write(fd, data.ptr + off, data.len - off);
        if (n == 0) return;
        off += n;
    }
}

fn die(msg: []const u8) noreturn {
    writeAll(2, msg);
    writeAll(2, "\n");
    std.process.exit(1);
}

// ─────────────────────────────────────────────────────────────
// 你的工具实现放这里。返回值直接作为 MCP 的文本结果回给 Mizar。
//
// 参数从 arguments 对象里按名取；缺失时给默认值。
// 注意：这是纯 Zig，没有 GC、没有运行时——所有系统能力都能直接用。
// ─────────────────────────────────────────────────────────────
fn callTool(alloc: std.mem.Allocator, name: []const u8, args: ?std.json.Value) ![]const u8 {
    if (std.mem.eql(u8, name, "echo")) {
        const text = blk: {
            const a = args orelse break :blk "";
            const o = switch (a) { .object => |o| o, else => break :blk "" };
            const v = o.get("text") orelse break :blk "";
            break :blk switch (v) { .string => |s| s, else => "" };
        };
        return std.fmt.allocPrint(alloc, "echo: {s}", .{text});
    }
    if (std.mem.eql(u8, name, "hostname")) {
        var buf: [std.posix.HOST_NAME_MAX]u8 = undefined;
        const host = std.posix.gethostname(&buf) catch return "gethostname failed";
        return std.fmt.allocPrint(alloc, "{s}", .{host});
    }
    return std.fmt.allocPrint(alloc, "未知工具: {s}", .{name});
}

/// 工具清单（JSON 片段）。新增工具时改这里 + callTool 里的分支。
const tools_json =
    \\[
    \\  {"name":"echo","description":"回显文本，用于连通性验证。参数: {text: string}",
    \\   "inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}},
    \\  {"name":"hostname","description":"返回本机主机名（演示系统调用能力）。",
    \\   "inputSchema":{"type":"object","properties":{}}}
    \\]
;

/// 极小 JSON 写出器：固定缓冲 + 手写转义，避免依赖 std.ArrayList（0.17 已改无管理式 API）。
const W = struct {
    buf: []u8,
    len: usize = 0,
    overflow: bool = false,

    fn init(buf: []u8) W {
        return .{ .buf = buf };
    }
    fn put(self: *W, s: []const u8) void {
        if (self.len + s.len > self.buf.len) {
            self.overflow = true;
            return;
        }
        @memcpy(self.buf[self.len..][0..s.len], s);
        self.len += s.len;
    }
    fn putEsc(self: *W, s: []const u8) void {
        for (s) |c| switch (c) {
            '"' => self.put("\\\""),
            '\\' => self.put("\\\\"),
            '\n' => self.put("\\n"),
            '\r' => self.put("\\r"),
            '\t' => self.put("\\t"),
            else => {
                if (c < 0x20) self.put("?") else self.put(&[_]u8{c});
            },
        };
    }
    fn putInt(self: *W, v: i64) void {
        var tmp: [24]u8 = undefined;
        const s = std.fmt.bufPrint(&tmp, "{d}", .{v}) catch return;
        self.put(s);
    }
    fn putId(self: *W, id: std.json.Value) void {
        switch (id) {
            .integer => |i| self.putInt(i),
            .float => |f| {
                var tmp: [40]u8 = undefined;
                self.put(std.fmt.bufPrint(&tmp, "{d}", .{f}) catch "null");
            },
            .string => |s| {
                self.put("\"");
                self.putEsc(s);
                self.put("\"");
            },
            else => self.put("null"),
        }
    }
    fn bytes(self: *const W) []const u8 {
        return self.buf[0..self.len];
    }
};

fn emit(w: W) void {
    if (w.overflow) writeAll(2, "warning: response truncated\n");
    writeAll(1, w.bytes());
    writeAll(1, "\n");
}

var resp_buf: [256 * 1024]u8 = undefined;

fn sendResult(id: std.json.Value, result_json: []const u8) void {
    var w = W.init(&resp_buf);
    w.put("{\"jsonrpc\":\"2.0\",\"id\":");
    w.putId(id);
    w.put(",\"result\":");
    w.put(result_json);
    w.put("}");
    emit(w);
}

fn sendError(id: std.json.Value, code: i64, msg: []const u8) void {
    var w = W.init(&resp_buf);
    w.put("{\"jsonrpc\":\"2.0\",\"id\":");
    w.putId(id);
    w.put(",\"error\":{\"code\":");
    w.putInt(code);
    w.put(",\"message\":\"");
    w.putEsc(msg);
    w.put("\"}}");
    emit(w);
}

fn handleLine(line: []const u8) void {
    const alloc = fba.allocator();

    const parsed = std.json.parseFromSlice(std.json.Value, alloc, line, .{}) catch {
        writeAll(2, "skip non-json line\n");
        return;
    };
    defer parsed.deinit();
    const root = switch (parsed.value) {
        .object => |o| o,
        else => return,
    };

    const id = root.get("id") orelse return; // 通知（无 id）不回复
    const method_v = root.get("method") orelse return;
    const method = switch (method_v) {
        .string => |s| s,
        else => return,
    };
    const params = root.get("params");

    if (std.mem.eql(u8, method, "initialize")) {
        sendResult(id, "{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{\"tools\":{}},\"serverInfo\":{\"name\":\"zig-mcp-server\",\"version\":\"0.1.0\"}}");
        return;
    }
    if (std.mem.eql(u8, method, "tools/list")) {
        sendResult(id, "{\"tools\":" ++ tools_json ++ "}");
        return;
    }
    if (std.mem.eql(u8, method, "tools/call")) {
        const p = switch (params orelse return) {
            .object => |o| o,
            else => return,
        };
        const name_v = p.get("name") orelse return;
        const name = switch (name_v) {
            .string => |s| s,
            else => return,
        };
        const args = p.get("arguments");
        const text = callTool(alloc, name, args) catch |e| {
            sendError(id, -32603, @errorName(e));
            return;
        };
        var w = W.init(&resp_buf);
        w.put("{\"jsonrpc\":\"2.0\",\"id\":");
        w.putId(id);
        w.put(",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"");
        w.putEsc(text);
        w.put("\"}]}}");
        emit(w);
        return;
    }
    // 其余（notifications/initialized 等）静默忽略
}

pub fn main() !void {
    var buf: [65536]u8 = undefined;
    var fill: usize = 0;
    while (true) {
        if (fill >= buf.len) {
            // 单行超长：丢弃到下一个换行，避免无限增长
            fill = 0;
        }
        const got = std.posix.read(0, buf[fill..]) catch |e| switch (e) {
            error.InputOutput => continue,
            else => return e,
        };
        if (got == 0) break; // EOF：Mizar 关闭了管道
        fill += got;
        while (true) {
            const idx = std.mem.indexOfScalar(u8, buf[0..fill], '\n') orelse break;
            var line = buf[0..idx];
            if (line.len > 0 and line[line.len - 1] == '\r') line = line[0 .. line.len - 1];
            if (line.len > 0) handleLine(line);
            const rest = fill - idx - 1;
            std.mem.copyForwards(u8, buf[0..rest], buf[idx + 1 .. fill]);
            fill = rest;
        }
    }
}
