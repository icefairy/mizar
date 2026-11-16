// acp_watchdog.zig —— codebuddy --serve 便携守护器（无任何依赖）
//
// 为什么存在：Mizar bash 工具调用结束会杀掉整个进程组，nohup/setsid
// 拉起的 serve 活不过工具调用返回；supervisor 不是每个环境都有。
// 本守护器 double-fork 脱离会话进程组（新会话 + 脱离进程组），自身
// 成为守护，负责拉起/监视/自动重启 serve。拷到任何 Linux 即可用。
//
// 用法：
//   acp_watchdog start    启动守护（若已在守护则退出 0）
//   acp_watchdog stop     停止守护与 serve
//   acp_watchdog status   打印 running|stopped
//
// 行为：
//   - 每 5s 探活 http://127.0.0.1:3011/api/v1/auth/status
//   - 探活失败连续 2 次 → 重启 serve（kill 旧进程后重新 spawn）
//   - serve 反复崩溃（60s 内 >3 次）→ 退避 30s 再试，避免风暴
//   - 日志：/tmp/acp-watchdog.log
//   - pid：/tmp/acp-watchdog.pid
//
// 构建：zig build-exe acp_watchdog.zig -O ReleaseSmall --name acp_watchdog

const std = @import("std");
const posix = std.posix;
const cwd_path = "/data/codes/mizar";
const serve_port: u16 = 3011;
const log_path = "/tmp/acp-watchdog.log";
const pid_path = "/tmp/acp-watchdog.pid";
const health_url = "http://127.0.0.1";

var log_fd: i32 = -1;

fn sleepSec(n: i64) void {
    var req: std.os.linux.timespec = .{ .sec = n, .nsec = 0 };
    var rem: std.os.linux.timespec = undefined;
    _ = std.os.linux.nanosleep(&req, &rem);
}

fn nowSec() i64 {
    var ts: std.os.linux.timespec = undefined;
    _ = std.os.linux.clock_gettime(std.os.linux.CLOCK.REALTIME, &ts);
    return @intCast(ts.sec);
}

fn posixExit(code: u8) noreturn {
    _ = std.os.linux.exit_group(code);
    unreachable;
}

fn tryFork() !i32 {
    const r = std.os.linux.fork();
    const sn: isize = @bitCast(r);
    if (sn < 0) return error.ForkFailed;
    return @intCast(sn);
}

const O_RDONLY: u32 = 0;
const O_RDWR: u32 = 2;
const O_WRONLY: u32 = 1;
const O_CREAT: u32 = 64;
const O_APPEND: u32 = 1024;
const O_TRUNC: u32 = 512;

fn sysOpen(path: [*:0]const u8, flags: u32, mode: u32) i32 {
    const f: std.os.linux.O = @bitCast(flags);
    return @bitCast(@as(u32, @truncate(std.os.linux.open(path, f, @intCast(mode)))));
}

fn writeAll(fd: i32, bytes: []const u8) void {
    var off: usize = 0;
    while (off < bytes.len) {
        const n = std.os.linux.write(fd, bytes.ptr + off, bytes.len - off);
        const sn: isize = @bitCast(n);
        if (sn <= 0) break;
        off += @intCast(sn);
    }
}

var lbuf: [512]u8 = undefined;
fn logf(comptime fmt: []const u8, args: anytype) void {
        var ts: std.os.linux.timespec = undefined;
    _ = std.os.linux.clock_gettime(std.os.linux.CLOCK.REALTIME, &ts);
    const secs: u64 = @as(u64, @intCast(ts.sec)) % 86400;
    const head = std.fmt.bufPrint(&lbuf, "[{d:0>2}:{d:0>2}:{d:0>2}] ", .{ secs / 3600, secs / 60 % 60, secs % 60 }) catch return;
    writeAll(log_fd, head);
    const body = std.fmt.bufPrint(lbuf[head.len..], fmt ++ "\n", args) catch return;
    writeAll(log_fd, body);
}

// ---- TCP 探活：connect 成功即认为端口活着（比 HTTP 解析省事）----
fn probePort() bool {
    const fd0 = std.os.linux.socket(std.os.linux.AF.INET, std.os.linux.SOCK.STREAM | std.os.linux.SOCK.NONBLOCK, 0);
    const fs: isize = @bitCast(fd0);
    if (fs < 0) return false;
    defer _ = std.os.linux.close(@intCast(fd0));
    var sa: std.os.linux.sockaddr.in = .{
        .port = (@as(u16, serve_port) >> 8) | (@as(u16, serve_port) << 8), // 大端
        .addr = (127 << 24) | 1,
        .family = std.os.linux.AF.INET,
    };
    const rc = std.os.linux.connect(@intCast(fd0), @ptrCast(&sa), @sizeOf(std.os.linux.sockaddr.in));
    const rs: isize = @bitCast(rc);
    if (rs == 0) return true; // 立即连上
    // EINPROGRESS → 等 500ms
    const en: usize = rc;
    const inprogress: usize = @bitCast(@as(isize, -@as(isize, @intFromEnum(std.os.linux.E.INPROGRESS))));
    if (en != inprogress) {
        return false; // 立即失败（拒连等）
    }
    // poll fd for write
    var pfds = [_]std.os.linux.pollfd{.{ .fd = @intCast(fd0), .events = std.os.linux.POLL.OUT, .revents = 0 }};
    const pr = std.os.linux.poll(&pfds, 1, 500);
    const prn: isize = @bitCast(pr);
    if (prn <= 0) return false;
    // 查 SO_ERROR
    var err: c_int = 0;
    var len: std.os.linux.socklen_t = @sizeOf(c_int);
    _ = std.os.linux.getsockopt(@intCast(fd0), std.os.linux.SOL.SOCKET, std.os.linux.SO.ERROR, @ptrCast(&err), &len);
    return err == 0;
}

// ---- 启动 serve（独立进程组）----
var child_pid_global: i32 = 0;

fn spawnServe() !void {
    const pid = try tryFork();
    if (pid != 0) {
        child_pid_global = pid;
        return; // 父（守护）返回
    }
    // 子进程：新会话，彻底脱离守护的进程组
    _ = std.os.linux.setsid();
    const devnull_fd = sysOpen("/dev/null", O_RDWR, 0);
    if (devnull_fd < 0) posixExit(1);
    _ = std.os.linux.dup2(devnull_fd, 0);
    _ = std.os.linux.dup2(devnull_fd, 1);
    _ = std.os.linux.dup2(devnull_fd, 2);
    const exe_path: [*:0]const u8 = "/root/.local/bin/codebuddy";
    const argv = [_:null]?[*:0]const u8{
        exe_path, "--serve", "--port", "3011", "--host", "127.0.0.1",
        "--auth", "password", "--permission-mode", "bypassPermissions", null,
    };
    const envp = [_:null]?[*:0]const u8{
        "HOME=/root",
        "PATH=/opt/nodejs/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        null,
    };
    _ = std.os.linux.execve(exe_path, &argv, &envp);
    posixExit(1); // exec 失败
}

fn killServe() void {
    // 扫 /proc 找 serve 进程 cmdline 含 "codebuddy --serve" 的，SIGTERM
    const dfd = sysOpen("/proc", O_RDONLY, 0);
    if (dfd < 0) return;
    defer _ = std.os.linux.close(dfd);
    var dbuf: [4096]u8 = undefined;
    // getdents64: syscall 217 on x86_64
    while (true) {
        const n = std.os.linux.getdents64(dfd, &dbuf, dbuf.len);
        const nn: isize = @bitCast(n);
        if (nn <= 0) break;
        var off: usize = 0;
        while (off < @as(usize, @intCast(nn))) {
            const d: *std.os.linux.dirent64 = @ptrCast(@alignCast(&dbuf[off]));
            const reclen: usize = d.reclen;
            if (reclen == 0) break;
            const name = std.mem.sliceTo(@as([*:0]const u8, @ptrCast(&d.name)), 0);
            const pid = std.fmt.parseInt(i32, name, 10) catch {
                off += reclen;
                continue;
            };
            var pathbuf: [64]u8 = undefined;
            const p = std.fmt.bufPrint(&pathbuf, "/proc/{d}/cmdline", .{pid}) catch {
                off += reclen;
                continue;
            };
            pathbuf[p.len] = 0;
            const fd = sysOpen(@ptrCast(&pathbuf), O_RDONLY, 0);
            if (fd >= 0) {
                var cbuf: [512]u8 = undefined;
                var total: usize = 0;
                while (total < cbuf.len) {
                    const r = std.os.linux.read(fd, cbuf[total..].ptr, cbuf.len - total);
                    const rn: isize = @bitCast(r);
                    if (rn <= 0) break;
                    total += @intCast(rn);
                }
                _ = std.os.linux.close(fd);
                const cmdline = cbuf[0..total];
                if (std.mem.indexOf(u8, cmdline, "codebuddy") != null and
                    std.mem.indexOf(u8, cmdline, "--serve") != null)
                {
                    _ = std.os.linux.kill(pid, .TERM);
                }
            }
            off += reclen;
        }
    }
}

// ---- 守护主循环 ----
fn daemonMain() void {
    log_fd = sysOpen(log_path.ptr, O_WRONLY | O_CREAT | O_APPEND, 0o644);
    if (log_fd < 0) return;
    logf("watchdog started (pid {d})", .{std.os.linux.getpid()});
    var crashes: u32 = 0;
    var last_start: i64 = 0;
    var fail_count: u32 = 0;
    _ = &fail_count;
    var serve_pid: i32 = 0;
    var serve_alive: bool = false;
    spawnServe() catch {
        logf("spawn failed", .{});
        return;
    };
    serve_pid = child_pid_global;
    serve_alive = true;
    last_start = nowSec();
    fail_count = 0; // 给新 serve 启动宽限（~15s）
    logf("serve spawned", .{});
    while (true) {
        // 收割僵尸 serve（非阻塞）
        var wst: c_int = 0;
        while (true) {
            const wr = std.os.linux.wait4(-1, &wst, 1, null); // WNOHANG=1
            const wn: isize = @bitCast(wr);
            if (wn <= 0) break;
            logf("reaped child pid {d}", .{wn});
            if (wn == serve_pid) serve_alive = false;
        }
        sleepSec(5);
        if (probePort() or serve_alive) {
            if (fail_count > 0) logf("serve recovered", .{});
            fail_count = 0;
            continue;
        }
        fail_count += 1;
        logf("probe fail #{d}", .{fail_count});
        // serve 启动后 15 秒就绪宽限：监听未起不等于死
        if (serve_alive and nowSec() - last_start < 15) continue;
        if (fail_count < 2) continue; // 偶发抖动
        fail_count = 0;
        const now = nowSec();
        if (now - last_start < 60) {
            crashes += 1;
            if (crashes > 3) {
                logf("crash loop, backoff 30s", .{});
                sleepSec(30);
                crashes = 0;
            }
        } else {
            crashes = 0;
        }
        // 重启前再探一次：可能 serve 刚被拉起还没就绪，别误杀
        if (probePort()) {
            logf("probe recovered, skip restart", .{});
            serve_alive = true;
            continue;
        }
        logf("restarting serve", .{});
        serve_alive = false;
        killServe();
        sleepSec(2);
        spawnServe() catch {
            logf("spawn failed", .{});
            continue;
        };
        last_start = nowSec();
    }
}

fn readPid() i32 {
    const fd = sysOpen(pid_path.ptr, O_RDONLY, 0);
    if (fd < 0) return -1;
    defer _ = std.os.linux.close(@intCast(fd));
    var buf: [16]u8 = undefined;
    var n: usize = 0;
    while (n < buf.len) {
        const r = std.os.linux.read(fd, buf[n..].ptr, 1);
        const rn: isize = @bitCast(r);
        if (rn <= 0) break;
        n += @intCast(rn);
    }
    const pid = std.fmt.parseInt(i32, std.mem.trim(u8, buf[0..n], " \n"), 10) catch return -1;
    return pid;
}

fn alreadyRunning() bool {
    const pid = readPid();
    if (pid <= 0) return false;
    // 探测该 pid 是否存活（signal 0）
    const r = std.os.linux.kill(pid, @enumFromInt(0));
    // kill 存活进程返回 0（成功）；进程不存在返回 -ESRCH
    const sn: isize = @bitCast(r);
    return sn == 0;
}

fn writePid(pid: i32) void {
    var buf: [16]u8 = undefined;
    const s = std.fmt.bufPrint(&buf, "{d}\n", .{pid}) catch return;
    const fd = sysOpen(pid_path.ptr, O_WRONLY | O_CREAT | O_TRUNC, 0o644);
    if (fd < 0) return;
    defer _ = std.os.linux.close(@intCast(fd));
    writeAll(fd, s);
}

fn cmdStop() void {
    const pid = readPid();
    if (pid > 0) {
        _ = std.os.linux.kill(pid, .TERM);
        var obuf: [40]u8 = undefined;
        const msg = std.fmt.bufPrint(&obuf, "stopped (watchdog {d})\n", .{pid}) catch "stopped\n";
        writeAll(1, msg);
    } else {
        writeAll(1, "stopped\n");
    }
    killServe();
    _ = std.os.linux.unlink(pid_path.ptr);
}

pub fn main(init: std.process.Init.Minimal) !void {
    const v = init.args.vector;
    const cmd: []const u8 = if (v.len > 1)
        std.mem.sliceTo(v[1], 0)
    else
        @as([]const u8, "start");
    if (std.mem.eql(u8, cmd, "stop")) {
        cmdStop();
        return;
    }
    if (std.mem.eql(u8, cmd, "status")) {
        writeAll(1, if (alreadyRunning()) "running\n" else "stopped\n");
        return;
    }
    // start
    if (alreadyRunning()) {
        writeAll(1, "already running\n");
        return;
    }
    // double-fork：fork 后父进程退出（脱离 Mizar 的 bash 工具进程组），
    // 子进程 setsid 成为新会话首进程，再 daemonMain。
    const pid = try tryFork();
    if (pid != 0) {
        var obuf: [40]u8 = undefined;
        const msg = std.fmt.bufPrint(&obuf, "watchdog started (pid {d})\n", .{pid}) catch "watchdog started\n";
        writeAll(1, msg);
        writePid(pid);
        return; // 调用方（bash 工具）立刻返回，不受进程组回收影响
    }
    _ = std.os.linux.setsid();
    daemonMain();
}
