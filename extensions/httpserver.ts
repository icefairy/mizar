// httpserver.ts - HTTP 文件服务器插件
// 将 /mnt/d/down 目录开放为 HTTP 静态文件服务，供其他主机访问。
//
// 工具列表：
//   httpserver_start   启动文件服务器（参数: {port, bind, dir}，全部可选）
//   httpserver_stop    停止文件服务器
//   httpserver_status  查询服务器状态
//
// 默认配置：port=8080, bind=0.0.0.0, dir=/mnt/d/down
//
// 使用示例：
//   tool_httpserver_start()                              // 用默认配置启动
//   tool_httpserver_start('{"port":8888,"dir":"/mnt/d/data"}')  // 自定义
//   tool_httpserver_status()
//   tool_httpserver_stop()

// ========== 宿主函数声明 ==========
declare const shell_exec: (cmd: string) => string;
declare const fs_read: (path: string) => string;
declare const fs_write: (path: string, content: string) => string;
declare const sleep: (ms: number) => void;

// ========== 默认配置 ==========
const DEFAULT_PORT = 8080;
const DEFAULT_BIND = "0.0.0.0";
const DEFAULT_DIR = "/mnt/d/down";

// 状态文件路径（存 PID）
const STATE_FILE = "/tmp/mizar_httpserver.pid";
// 写入的 Python 服务器脚本路径
const SERVER_SCRIPT = "/tmp/mizar_httpserver.py";
// 日志文件
const LOG_FILE = "/tmp/mizar_httpserver.log";

// ========== 内嵌 Python HTTP 服务器脚本 ==========
const PYTHON_SERVER = `\nimport http.server\nimport socketserver\nimport os\nimport sys\nimport mimetypes\nimport urllib.parse\nimport datetime\n\nPORT = int(sys.argv[1])\nBIND = sys.argv[2]\nROOT = sys.argv[3]\nLOG_PATH = sys.argv[4]\n\nfor ext, mime in [".mp4","video/mp4"),(".mkv","video/x-matroska"),(".webm","video/webm"),(".mp3","audio/mpeg"),(".wav","audio/wav"),(".zip","application/zip"),(".rar","application/x-rar-compressed"),(".7z","application/x-7z-compressed"),(".doc","application/msword"),(".docx","application/vnd.openxmlformats-officedocument.wordprocessingml.document"),(".pdf","application/pdf"),(".png","image/png"),(".jpg","image/jpeg"),(".jpeg","image/jpeg"),(".webp","image/webp"),(".woff2","font/woff2"),(".woff","font/woff"),(".ttf","application/font-sfnt"),(".txt","text/plain"),(".md","text/plain"),(".json","text/plain"),(".html","text/html"),(".htm","text/html")]:\n    mimetypes.add_type(mime, ext)\n\nclass Handler(http.server.BaseHTTPRequestHandler):\n    def _cors(self):\n        self.send_header("Access-Control-Allow-Origin", "*")\n        self.send_header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")\n        self.send_header("Access-Control-Allow-Headers", "*")\n        self.send_header("Access-Control-Max-Age", "86400")\n\n    def _realpath(self, path):\n        rel = urllib.parse.unquote(path).lstrip("/")\n        abs_path = os.path.normpath(os.path.join(ROOT, rel))\n        if not abs_path.startswith(ROOT):\n            return None\n        return abs_path\n\n    def _send_file(self, path):\n        try:\n            st = os.stat(path)\n        except OSError:\n            self.send_error(404, "Not Found")\n            return\n        size = st.st_size\n        mtime = st.st_mtime\n        range_hdr = self.headers.get("Range", "")\n        start, length, status = 0, size, 200\n        if range_hdr:\n            try:\n                _, spec = range_hdr.split("=", 1)\n                parts = spec.split("-")\n                if parts[0]: start = int(parts[0])\n                if parts[1]: end = int(parts[1])\n                else: end = size - 1\n                length = end - start + 1\n                status = 206\n            except: pass\n        mime, _ = mimetypes.guess_type(path)\n        if mime is None: mime = "application/octet-stream"\n        self.send_response(status)\n        self.send_header("Content-Type", mime)\n        self.send_header("Content-Length", str(length))\n        self.send_header("Last-Modified", http.server.time_time.strftime("%a, %d %b %Y %H:%M:%S GMT", http.server.time_time.gmtime(mtime)))\n        self.send_header("Accept-Ranges", "bytes")\n        if status == 206:\n            self.send_header("Content-Range", f"bytes {start}-{start+length-1}/{size}")\n        self._cors()\n        self.send_header("Content-Disposition", f'attachment; filename="{os.path.basename(path)}"')\n        self.end_headers()\n        with open(path, "rb") as f:\n            f.seek(start)\n            remaining = length\n            while remaining > 0:\n                chunk = f.read(min(65536, remaining))\n                if not chunk: break\n                self.wfile.write(chunk)\n                remaining -= len(chunk)\n        ts = datetime.datetime.now().strftime("%H:%M:%S")\n        try:\n            with open(LOG_PATH, "a") as lf:\n                lf.write(f"[{ts}] {self.address_string()} {self.command} {self.path} -> {status} ({size}b)\\n")\n        except: pass\n\n    def do_OPTIONS(self):\n        self.send_response(204); self._cors(); self.end_headers()\n\n    def do_HEAD(self):\n        p = self._realpath(self.path)\n        if p is None or not os.path.exists(p): self.send_error(404); return\n        self._send_file(p)\n\n    def do_GET(self):\n        p = self._realpath(self.path)\n        if p is None:\n            self.send_error(403, "Blocked"); return\n        if not os.path.exists(p):\n            self.send_error(404); return\n        if os.path.isdir(p): self._list_dir(p)\n        else: self._send_file(p)\n\n    def _list_dir(self, path):\n        try: entries = sorted(os.listdir(path))\n        except OSError:\n            self.send_error(500); return\n        rel = os.path.relpath(path, ROOT)\n        html_parts = ["<!DOCTYPE html><html><head><meta charset=utf-8>"]\n        html_parts.append(f"<title>Index of /{rel}</title>")\n        html_parts.append("<style>body{font-family:monospace;margin:1em}a{color:#06c}td{padding:2px 8px}.size{color:#888;text-align:right}</style></head><body>")\n        html_parts.append(f"<h1>Index of /{rel}</h1><hr><table><tr><th>Name</th><th>Size</th><th>Modified</th></tr>")\n        for e in entries:\n            full = os.path.join(path, e)\n            try: st = os.stat(full)\n            except: continue\n            is_d = os.path.isdir(full)\n            sz = st.st_size if not is_d else 0\n            mt = datetime.datetime.fromtimestamp(st.st_mtime).strftime("%Y-%m-%d %H:%M")\n            href = urllib.parse.quote(e) + ("/" if is_d else "")\n            ico = "📁" if is_d else "📄"\n            html_parts.append(f"<tr><td><a href={href}>{ico} {e}</a></td><td class=size>{sz:,}</td><td>{mt}</td></tr>")\n        html_parts.append("</table><hr><small>Powered by Mizar</small></body></html>")\n        body = "\\n".join(html_parts).encode("utf-8")\n        self.send_response(200)\n        self.send_header("Content-Type", "text/html; charset=utf-8")\n        self.send_header("Content-Length", str(len(body)))\n        self._cors()\n        self.end_headers()\n        self.wfile.write(body)\n        try:\n            with open(LOG_PATH, "a") as lf:\n                lf.write(f"[{datetime.datetime.now().strftime('%H:%M:%S')}] {self.address_string()} GET {self.path} -> 200 (dir)\\n")\n        except: pass\n\n    def log_message(self, fmt, *args): pass\n\nclass TCPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):\n    allow_reuse_address = True\n    daemon_threads = True\n\nserver = TCPServer((BIND, PORT), Handler)\nprint(f"STARTED http://{BIND}:{PORT}/ root={ROOT}", flush=True)\ntry: server.serve_forever()\nexcept KeyboardInterrupt: pass\nfinally: server.server_close()\n`;

// ========== 辅助函数 ==========

function ensureScript(): void {
    fs_write(SERVER_SCRIPT, PYTHON_SERVER);
    shell_exec(`chmod +x ${SERVER_SCRIPT}`);
}

function readPid(): number {
    try { return parseInt(fs_read(STATE_FILE).trim()); } catch (e) { return -1; }
}

function writePid(pid: number): void { fs_write(STATE_FILE, String(pid)); }

function isAlive(pid: number): boolean {
    if (pid <= 0) return false;
    const out = shell_exec(`kill -0 ${pid} 2>&1`);
    return out.length === 0;
}

function cleanupStale(): void {
    const pid = readPid();
    if (pid > 0 && !isAlive(pid)) shell_exec(`rm -f ${STATE_FILE}`);
}

// ========== 公开工具 ==========

/**
 * 启动 HTTP 文件服务器
 * 参数 JSON: {port: number, bind: string, dir: string}，全部可选
 */
export function tool_httpserver_start(args: string): string {
    let port = DEFAULT_PORT, bind = DEFAULT_BIND, dir = DEFAULT_DIR;
    try {
        const cfg = JSON.parse(args);
        if (cfg.port != null) port = cfg.port;
        if (cfg.bind != null) bind = cfg.bind;
        if (cfg.dir != null) dir = cfg.dir;
    } catch (e) {}

    cleanupStale();

    const pid = readPid();
    if (pid > 0 && isAlive(pid)) {
        return `服务器已在运行 (PID=${pid}, 端口=${port}, 目录=${dir})`;
    }

    const dirCheck = shell_exec(`test -d ${dir} && echo OK`);
    if (dirCheck.trim() !== "OK") return `错误: 目录不存在: ${dir}`;

    ensureScript();
    shell_exec(`> ${LOG_FILE}`);

    const portCheck = shell_exec(`ss -tlnp 2>/dev/null | grep ":${port} " | head -1 || true`);
    if (portCheck.length > 0) return `错误: 端口 ${port} 已被占用\\n${portCheck.trim()}`;

    const cmd = `nohup python3 ${SERVER_SCRIPT} ${port} "${bind}" "${dir}" "${LOG_FILE}" > /tmp/mizar_httpserver.out 2>&1 & echo $!`;
    const out = shell_exec(cmd);
    const m = out.match(/\d+/);
    if (!m) return `启动失败，无法获取 PID: ${out}`;

    const newPid = parseInt(m[0]);
    writePid(newPid);
    sleep(500);

    if (!isAlive(newPid)) {
        const log = shell_exec(`tail -5 ${LOG_FILE} 2>/dev/null`);
        return `启动失败，进程已退出:\\n${log}`;
    }

    const ip = shell_exec("hostname -I 2>/dev/null | awk '{print $1}' || echo 127.0.0.1").trim().split("\n")[0];

    return [
        `✅ HTTP 文件服务器已启动`,
        `  地址: http://${ip}:${port}/`,
        `  本机: http://127.0.0.1:${port}/`,
        `  PID: ${newPid}`,
        `  目录: ${dir}`,
        `  日志: ${LOG_FILE}`,
        ``,
        `其他主机访问:`,
        `  浏览器打开 http://${ip}:${port}/`,
    ].join("\n");
}

/** 停止 HTTP 文件服务器 */
export function tool_httpserver_stop(): string {
    cleanupStale();
    const pid = readPid();
    if (pid <= 0 || !isAlive(pid)) {
        shell_exec(`rm -f ${STATE_FILE}`);
        return `服务器未运行（已清理残留）`;
    }
    shell_exec(`kill ${pid} 2>/dev/null || true`);
    sleep(1000);
    if (!isAlive(pid)) { shell_exec(`rm -f ${STATE_FILE}`); return `服务器已停止 (PID=${pid})`; }
    shell_exec(`kill -9 ${pid} 2>/dev/null || true`);
    shell_exec(`rm -f ${STATE_FILE}`);
    return `服务器已强制停止 (PID=${pid})`;
}

/** 查询 HTTP 文件服务器状态 */
export function tool_httpserver_status(): string {
    cleanupStale();
    const pid = readPid();
    if (pid <= 0 || !isAlive(pid)) return `HTTP 文件服务器未运行`;

    let logContent = "";
    try { logContent = fs_read(LOG_FILE); } catch (e) { logContent = "(日志不可读)"; }

    const sizeOut = shell_exec(`du -sh ${DEFAULT_DIR} 2>/dev/null | awk '{print $1}' || echo '?'`).trim();
    const fileCount = shell_exec(`find ${DEFAULT_DIR} -maxdepth 1 -type f 2>/dev/null | wc -l || echo '?'`).trim();
    const lines = logContent.trim().split("\n").filter(l => l.trim());
    const lastLogs = lines.length > 5 ? lines.slice(-5).join("\n") : lines.join("\n");

    return [
        `HTTP 文件服务器: 运行中`,
        `  PID: ${pid}`,
        `  端口: ${DEFAULT_PORT}`,
        `  目录: ${DEFAULT_DIR}`,
        `  目录大小: ${sizeOut}`,
        `  文件数: ${fileCount}`,
        ``,
        `最近日志:`,
        lastLogs || "  (暂无)",
    ].join("\n");
}