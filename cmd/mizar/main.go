// Mizar (开阳) —— 极简自举 Agent CLI。
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	ctxpkg "context"
	"github.com/google/uuid"

	"mizar/internal/agent"
	"mizar/internal/assets"
	"mizar/internal/builtins"
	"mizar/internal/config"
	"mizar/internal/context"
	"mizar/internal/diag"
	"mizar/internal/engine"
	"mizar/internal/exporter"
	"mizar/internal/jobs"
	"mizar/internal/learninggraph"
	"mizar/internal/llm"
	"mizar/internal/lsp"
	"mizar/internal/memory"
	"mizar/internal/plugins"
	"mizar/internal/prompts"
	"mizar/internal/redact"
	"mizar/internal/reflect"
	"mizar/internal/schedule"
	"mizar/internal/server"
	"mizar/internal/session"
	"mizar/internal/sessionsearch"
	"mizar/internal/shell"
	"mizar/internal/skills"
	"mizar/internal/trust"
	"mizar/internal/userhooks"
)

var version = "v0.8.9"

// 进程内工具调用计数（供 /learn reflect 的「高频工具」洞察；不持久化）。
var (
	toolUsageMu sync.Mutex
	toolUsage   = map[string]int{}
)

// buildTag 标识 Windows 产物用的是哪条构建链，由 -ldflags -X main.buildTag=... 注入。
//
//	win10-native —— 官方 Go SDK 编出，Win8.1/10/11 用（长路径走系统原生处理）
//	win7-compat  —— 打过 go-win7 补丁的 SDK 编出，Win7 SP1/2008 R2 用
//
// 非 Windows 产物留空（Linux 侧两条链编出的东西完全一致，无需区分）。
// 现场排查时 -version 第一行就能看出装的是哪个包，省掉「你装的是哪份」的往返。
var buildTag = ""

// versionString 返回带构建标识的版本串，如 "v0.7.9 (win7-compat)"。
func versionString() string {
	if buildTag == "" {
		return version
	}
	return version + " (" + buildTag + ")"
}

// hostPort 把 ":3003" 转成 "127.0.0.1:3003" 便于浏览器访问（空 host 用本机）。
func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// randPassword 生成 n 位随机密码（Web UI 首次启动用）。
func randPassword(n int) string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "mizar-web-admin"
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// startupHint 首次运行未配置供应商时，banner 末尾追加的引导提示。
var startupHint string

// mcpListText 生成「/mcp list」输出：已配置的 server 清单 + 各 server 当前是否已连接。
// 连接是惰性的（首次 mcp_call 才握手），故同时展示「配置」与「已连」两项状态。
func mcpListText(cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("MCP servers：配置 %d 个（来源 %s）\n", len(cfg.MCPServers), config.DefaultPath()))
	if len(cfg.MCPServers) == 0 {
		sb.WriteString("  （未配置，使用 /mcp add <name> <url|command> 添加）\n")
		sb.WriteString("\n用法:\n")
		sb.WriteString("  /mcp list                       列出已配置的 server\n")
		sb.WriteString("  /mcp add <name> <url>           添加 HTTP server\n")
		sb.WriteString("  /mcp add <name> <command> [args] 添加 stdio server\n")
		sb.WriteString("  /mcp remove <name>              删除 server\n")
		sb.WriteString("  /mcp test <name>                测试连接并列出工具\n")
		sb.WriteString("  /mcp reload                     重新加载配置\n")
		return sb.String()
	}
	for _, s := range cfg.MCPServers {
		transport := "stdio"
		addr := s.Command
		if s.URL != "" {
			transport = "http"
			addr = s.URL
		}
		state := "未连接"
		if runtimeMCPConnected(s.Name) {
			state = "已连接"
		}
		if s.Timeout > 0 {
			sb.WriteString(fmt.Sprintf("  %-15s [%s] %s  timeout=%ds  状态: %s\n", s.Name, transport, addr, s.Timeout, state))
		} else {
			sb.WriteString(fmt.Sprintf("  %-15s [%s] %s  状态: %s\n", s.Name, transport, addr, state))
		}
		if len(s.Args) > 0 {
			sb.WriteString(fmt.Sprintf("    args: %s\n", strings.Join(s.Args, " ")))
		}
	}
	sb.WriteString("\n提示：连接为惰性建立（首次 mcp_call 时握手），未连接 ≠ 不可用。\n")
	sb.WriteString("\n用法:\n")
	sb.WriteString("  /mcp list                       列出已配置的 server\n")
	sb.WriteString("  /mcp add <name> <url>           添加 HTTP server\n")
	sb.WriteString("  /mcp add <name> <command> [args] 添加 stdio server\n")
	sb.WriteString("  /mcp remove <name>              删除 server\n")
	sb.WriteString("  /mcp test <name>                测试连接并列出工具\n")
	sb.WriteString("  /mcp reload                     重新加载配置\n")
	return sb.String()
}

// runtimeMCPRegistry 当前进程生效的 MCP 注册表（供 /mcp 命令与状态栏查询连接状态）。
var runtimeMCPRegistry *engine.MCPRegistry

// runtimeMCPConnected 判断某 server 是否已建立连接。
func runtimeMCPConnected(name string) bool {
	return runtimeMCPRegistry != nil && runtimeMCPRegistry.Connected(name)
}

// runtimeMCPStats 返回（配置数, 已连接数），供状态栏第一行展示。
func runtimeMCPStats() (int, int) {
	return runtimeMCPRegistry.Stats()
}

func main() {
	var (
		baseURL    = flag.String("base-url", "http://127.0.0.1:3002/v1", "OpenAI 兼容端点 (默认指向璇玑网关)")
		apiKey     = flag.String("api-key", "", "API Key (可选)")
		model      = flag.String("model", "deepseek-v4-flash", "模型名")
		extDir     = flag.String("ext", "", "插件目录 (默认 ~/.mizar/extensions)")
		skillDir   = flag.String("skills", "skills", "技能目录")
		workDir    = flag.String("workdir", "", "工作目录 (AGENTS.md 查找起点, 默认当前目录)")
		sessDir    = flag.String("sessions", "~/.mizar/sessions", "会话目录 (默认 ~/.mizar/sessions，按工作路径自动分目录)")
		sessionID  = flag.String("session", "", "会话 ID (续接对话)")
		contLast   = flag.Bool("c", false, "续接最后一次退出的会话（等同 -continue）")
		contLast2  = flag.Bool("continue", false, "续接最后一次退出的会话（-c 的长写法）")
		history    = flag.Int("history", 50, "会话恢复的最大历史消息数")
		ctxWindow  = flag.Int("ctx-window", 128000, "模型上下文窗口 (token，压缩触发线)")
		noCompact  = flag.Bool("no-compact", false, "禁用会话压缩")
		noMCP      = flag.Bool("no-mcp", false, "禁用 MCP（不初始化 mcp_call 宿主函数）")
		task       = flag.String("task", "", "任务内容 (非空则单次执行)")
		showVer    = flag.Bool("version", false, "显示版本")
		exportHTML = flag.Bool("export", false, "把会话导出为自包含 HTML（配合 -session <id> 或 -c；输出到 -export-out）")
		exportOut  = flag.String("export-out", "", "HTML 导出路径（空=stdout）")
		lspBinary  = flag.String("lsp", "", "LSP 语言服务器路径 (如 gopls/tsserver，空=禁用 LSP)")
		lspServer  = flag.Bool("lsp-server", false, "以 LSP server 模式运行 (stdio)")
		tui        = flag.Bool("tui", true, "使用 Bubble Tea TUI 界面（默认开启，--no-tui 用经典 readline）")
		// Server 模式（持久运行 daemon）
		serve  = flag.Bool("serve", false, "启动 Server 模式（持久运行）")
		addr   = flag.String("addr", ":3003", "Server 监听地址")
		token  = flag.String("token", "", "Server Bearer token (空=不认证)")
		httpOn = flag.Bool("http", true, "Server: 启用 OpenAI 兼容 HTTP")
		rpcOn  = flag.Bool("rpc", true, "Server: 启用 JSON-RPC")
		wsOn   = flag.Bool("ws", true, "Server: 启用 WebSocket")
		// 宿主函数文档生成
		genDoc    = flag.Bool("plugin-doc", false, "生成宿主函数文档（精简清单 + doc_get 指引）")
		genDocOut = flag.String("plugin-doc-out", "docs/host-funcs.md", "文档输出路径")
		// OpenAPI 生成（路由注册表 → docs/openapi.json + 路由 markdown 表）
		genOpenAPI = flag.Bool("gen-openapi", false, "从路由注册表生成 OpenAPI JSON（docs/openapi.json）")
		genOpenOut = flag.String("gen-openapi-out", "docs/openapi.json", "OpenAPI 输出路径")
		// 前端外部化助手：把内嵌前端释放到目录，改完 -serve 时外部目录生效
		webuiOut = flag.String("webui-out", "", "把内嵌 Web UI 前端释放到该目录（默认 ~/.mizar/webui/，改完无需重编译）")
		noAssets = flag.Bool("no-assets", false, "不释放内置插件/技能（默认首次运行自动释放到 ~/.mizar）")
	)
	// 兼容别名：-server → -serve（Go flag 无别名机制，解析前替换）
	// 避免用户拼写 -server 时直接 flag 报错
	for i, a := range os.Args[1:] {
		if a == "-server" || a == "--server" {
			os.Args[i+1] = "-serve"
		}
	}
	flag.Parse()

	if *showVer {
		fmt.Println("mizar", versionString())
		return
	}

	// -export：把指定会话导出为自包含 HTML（P-6），不启动交互循环。
	if *exportHTML {
		os.Exit(runExport(*sessionID, *contLast || *contLast2, *sessDir, *workDir, *exportOut, *model))
	}

	// 诊断日志：落盘到 ~/.mizar/logs/，用于排查「任务莫名中止/卡死」类问题。
	// 默认开启（TUI 下 stderr 即终端，诊断信息不能写那儿）；MIZAR_DIAG=0 或
	// config.json 的 diag_log=false 可关闭。
	diagOn := os.Getenv("MIZAR_DIAG") != "0"
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.DiagLog != nil {
		diagOn = *cfg.DiagLog
	}
	diag.Init(diagOn)
	defer diag.Close()
	diag.Logf("tui", "mizar %s 启动: args=%v", versionString(), os.Args[1:])

	// Windows：控制台代码页切 UTF-8（中文乱码），并预检 VT 能力。
	// tcell 在 Windows 只有 VT 一条实现路径，Win7/8 的 conhost 不支持，
	// 结果是 NewScreen 失败后「一闪而退」掉进经典模式 —— 需要提前讲明白。
	ensureConsoleUTF8()
	if *tui && !consoleVTEnabled() {
		fmt.Fprintln(os.Stderr, windowsVersionHint())
		fmt.Fprintln(os.Stderr, "\n已自动切换到经典交互模式。")
		diag.Logf("tui", "console VT unsupported → 降级经典模式")
		*tui = false
	}

	// LSP server 模式：通过 stdio 提供 LSP 能力
	if *lspServer {
		log.Print("LSP server 模式启动")
		ctx := ctxpkg.Background()
		if err := lsp.ServeStdio(ctx, lsp.ProviderConfig{}); err != nil {
			log.Fatalf("LSP server 退出: %v", err)
		}
		return
	}

	// 生成宿主函数文档（渐进式披露：精简清单 + 完整文档 md）
	if *genDoc {
		briefs := engine.HostDocBriefs()
		md := "# 开阳宿主函数参考\n\n写插件时的精简清单（~560 tokens）：\n\n" + briefs +
			"\n完整文档用宿主函数 doc_get(name) 按需查询。\n"
		out := *genDocOut
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			log.Fatalf("plugin-doc: %v", err)
		}
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			log.Fatalf("plugin-doc: %v", err)
		}
		fmt.Printf("宿主函数文档已生成: %s（%d 函数, %d 字符）\n", out, len(engine.HostDocList()), len(md))
		return
	}

	// 从路由注册表生成 OpenAPI JSON（-gen-openapi）。
	if *genOpenAPI {
		out := *genOpenOut
		jsonStr, err := server.OpenAPIFromTable(versionString())
		if err != nil {
			log.Fatalf("gen-openapi: %v", err)
		}
		if dir := filepath.Dir(out); dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		if err := os.WriteFile(out, []byte(jsonStr+"\n"), 0o644); err != nil {
			log.Fatalf("gen-openapi: %v", err)
		}
		fmt.Printf("OpenAPI 已生成: %s（路由数 %d）\n", out, len(server.PublicRoutes()))
		return
	}

	// 释放内嵌前端到外部目录（-webui-out <dir>），改完 -serve 即生效无需重编译。
	// 传 "-" 等价于默认目录 ~/.mizar/webui/。
	if *webuiOut != "" {
		dir := *webuiOut
		if dir == "-" {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, ".mizar", "webui")
			}
		}
		n, err := server.ReleaseFrontend(dir)
		if err != nil {
			log.Fatalf("webui-out: %v", err)
		}
		fmt.Printf("前端已释放到 %s（%d 文件）。-serve 时若该目录含 index.html 即优先服务它。\n", dir, n)
		return
	}

	// 自动加载 ~/.mizar/config.json（显式 flag 优先）
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.BaseURL != "" {
		explicit := map[string]bool{}
		flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		if !explicit["base-url"] {
			*baseURL = cfg.BaseURL
		}
		if !explicit["api-key"] {
			*apiKey = cfg.APIKey
		}
		if !explicit["model"] {
			*model = cfg.Model
		}
		if !explicit["ctx-window"] && cfg.ContextWindow > 0 {
			*ctxWindow = cfg.ContextWindow
		}
		log.Printf("已加载配置 %s: model=%s thinking=%s window=%d", config.DefaultPath(), *model, cfg.ThinkingStr(), *ctxWindow)
	}

	// 命令执行器（bash 工具 / ! 命令 / 插件 shell_exec 用哪个 shell）：
	// 优先恢复 /shell 持久化到 config 的选择，否则按平台自动探测
	// （Windows: pwsh → powershell → cmd；Unix: bash → sh）。
	if scfg, err := config.Load(config.DefaultPath()); err == nil {
		if scfg.Shell != "" {
			shell.Init(scfg.Shell)
			log.Printf("命令执行器已恢复: %s（%s）", shell.Current().String(), shell.Current().Path)
		}
		// 鼠标上报开关（/mouse 写入）：nil 时用平台默认（现各平台默认开启）。
		SetMouseFromConfig(scfg.Mouse)
	}

	// 宿主函数集
	host := &engine.HostFuncs{
		// Sleep：插件 sleep(ms) 原生休眠（time.Sleep，不开进程）。此前从未赋值，
		// sleep 宿主函数一直没注册，插件只能拿 powershell.exe 模拟——Linux 上
		// 必然 exit 127。上限 60s 防止插件写错单位把会话挂死。
		Sleep: func(ms int) {
			if ms < 0 {
				ms = 0
			}
			if ms > 60000 {
				ms = 60000
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
		},
		FSRead: func(p string) (string, error) {
			fi, err := os.Stat(p)
			if err != nil {
				return "", err
			}
			if fi.Size() > 10*1024*1024 {
				return "", fmt.Errorf("FSRead: %s is %.1fMB (limit 10MB); use fs_read_range for seek reads", p, float64(fi.Size())/1024/1024)
			}
			b, err := os.ReadFile(p)
			return string(b), err
		},
		// FSReadRange：有界 seek 读取。返回 [content, totalSize, error]。
		// 单次最多 4MB，大文件插件用循环分片读。
		FSReadRange: engine.FSReadRangeFn,
		FSWrite:     func(p, c string) error { return os.WriteFile(p, []byte(c), 0o644) },
		FSList: func(dir string) ([]string, error) {
			es, e := os.ReadDir(dir)
			if e != nil {
				return nil, e
			}
			out := make([]string, 0, len(es))
			for _, x := range es {
				out = append(out, x.Name())
			}
			return out, nil
		},
		// ShellExec：插件 shell_exec(cmd) —— 交给平台默认 shell（Unix=bash/sh，
		// Windows=PowerShell/cmd），保证插件在 Windows 上也能执行命令。
		ShellExec: func(cmd string) (string, error) {
			out, err := shell.Command(cmd).CombinedOutput()
			return shell.CleanOutput(string(out)), err
		},
		Log: func(msg string) { log.Print(msg) },
		// Env：插件读环境变量（定位 HOME 等），避免硬编码路径
		Env: func(name string) string { return os.Getenv(name) },
		// DBQuery：内置数据库查询（sqlite3/mysql/postgres），插件 db_query() 直达
		DBQuery: engine.DBQueryFn,
		// DBExecBatch：事务批量执行，插件 db_exec_batch() 直达（连接池复用）
		DBExecBatch: engine.DBExecBatchFn,
		// DBClose：关闭连接丢弃会话残留，插件 db_close() 直达
		DBClose: engine.DBCloseFn,
	}
	// 启动 db 连接池空闲回收（文件库 5min 未用自动 Close，:memory: 永久保留）
	engine.StartDBReaper()
	// MCPCall：外部 MCP server 长尾能力（Redis/Kafka/MongoDB 等），插件 mcp_call() 直达
	mcpReg := engine.NewMCPRegistry(nil)
	if *noMCP {
		log.Printf("已禁用 MCP（--no-mcp）: mcp_call 不可用")
	} else if cfg, err := config.Load(config.DefaultPath()); err == nil && len(cfg.MCPServers) > 0 {
		mcpReg = engine.NewMCPRegistry(cfg.MCPServers)
		log.Printf("MCP server 配置 %d 个: %s", len(cfg.MCPServers), mcpNames(cfg.MCPServers))
	} else if err != nil {
		log.Printf("MCP 配置读取失败（mcp_call 不可用）: %v", err)
	} else {
		log.Printf("未配置 MCP server（mcp_call 不可用，config.json 加 mcp_servers 启用）")
	}
	defer mcpReg.Close()
	host.MCPCall = mcpReg.CallFn()
	runtimeMCPRegistry = mcpReg
	// NetBridge：插件 TCP 网络能力（listen/dial/send/recv/close/stop），实现分布式通信/自定义协议
	host.NetBridge = engine.NewNetBridge()
	// FTPBridge：插件 FTP 客户端（connect/list/upload/download/mkdir/rmdir/delete/rename/close）
	host.FTPBridge = engine.NewFTPBridge()
	// WSClient：插件 WS 客户端（连外部长连接服务，如飞书），ws_connect/send/onmessage/close
	host.WSClient = engine.NewWSClientBridge()
	// 插件 HTTP 能力：统一 http_request(method,url,body,headers) + 薄封装 http_get/http_post
	httpDo := func(method, url, body, headersJSON string) (string, error) {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if headersJSON != "" {
			var hdr map[string]string
			if err := json.Unmarshal([]byte(headersJSON), &hdr); err != nil {
				return "", fmt.Errorf("http headers: %w", err)
			}
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
		}
		hc := &http.Client{Timeout: 30 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 400 {
			return string(b), fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return string(b), nil
	}
	host.HTTPRequest = httpDo
	host.HTTPGet = func(url string) (string, error) { return httpDo("GET", url, "", "") }
	host.HTTPPost = func(url, body string) (string, error) { return httpDo("POST", url, body, "") }
	// 提供真实 LLM 给插件 llm_chat
	client := llm.NewOpenAI(*baseURL, *apiKey, *model)
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		client.SetThinkingLevel(cfg.ThinkingStr())
		client.SetUserAgent(cfg.UserAgent)
	}
	host.LLMChat = func(messagesJSON string) (string, error) {
		var msgs []agent.Message
		if err := json.Unmarshal([]byte(messagesJSON), &msgs); err != nil {
			return "", err
		}
		return client.Chat(msgs)
	}
	// ai_chat / ai_chat_stream：插件直连 LLM（复用主程序 LLM 通道）
	host.AIChat = func(reqJSON string) (string, error) {
		var req llm.AIChatRequest
		if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
			return "", fmt.Errorf("ai_chat: %w", err)
		}
		return client.AIChat(req)
	}
	host.AIChatStream = func(reqJSON string, onDelta func(deltaJSON string)) (string, error) {
		var req llm.AIChatRequest
		if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
			return "", fmt.Errorf("ai_chat_stream: %w", err)
		}
		return client.AIChatStream(req, func(d agent.StreamDelta) {
			b, _ := json.Marshal(llm.AIChatStreamDelta{Thinking: d.Thinking, Content: d.Content})
			onDelta(string(b))
		})
	}
	// LSP 插件桥接：JS 插件用 lsp_register_diagnostic(name, fn) 注册诊断提供者。
	// fn(uri string) => jsonString（诊断数组 [{startLine,startChar,endLine,endChar,severity,message,source}]）
	host.LSPRegisterDiagnostic = func(name string, fn func(uri string) string) error {
		return lsp.RegisterDiagnosticProvider(lsp.NewJSDiagnosticProvider(name, fn))
	}
	host.LSPRegisterCompletion = func(name string, fn func(uri string, line, col int) string) error {
		return lsp.RegisterCompletionProvider(lsp.NewJSCompletionProvider(name, fn))
	}
	host.LSPUnregisterDiagnostic = lsp.UnregisterDiagnosticProvider
	host.LSPUnregisterCompletion = lsp.UnregisterCompletionProvider

	// host_listen：由插件 Manager 接管（internal/plugins/loader.go 中实现）

	extAbs, err := filepath.Abs(*extDir)
	if err != nil {
		log.Fatal(err)
	}
	// 插件目录默认 ~/.mizar/extensions（全局，跨项目共享）
	if *extDir == "" {
		extAbs = filepath.Join(config.ConfigDir(), "extensions")
	}
	if _, err := os.Stat(extAbs); os.IsNotExist(err) {
		if err := os.MkdirAll(extAbs, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	skAbs, _ := filepath.Abs(*skillDir)
	// 技能目录默认 ~/.mizar/skills（与全局 AGENTS.md/config.json 同根，避免相对路径漂移）
	if *skillDir == "skills" {
		skAbs = filepath.Join(config.ConfigDir(), "skills")
	}
	if _, err := os.Stat(skAbs); os.IsNotExist(err) {
		if err := os.MkdirAll(skAbs, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	// 释放内置插件/技能（随二进制发布，只补缺不覆盖）。--no-assets 可关闭。
	if !*noAssets {
		if r := assets.Release(extAbs, skAbs); len(r.Written) > 0 {
			log.Printf("已释放内置插件/技能 %d 个: %v", len(r.Written), r.Written)
		}
	}

	// 技能使用统计（默认开启，全局配置 skill_stats_enabled=false 关闭）
	stats := skills.LoadStats(filepath.Join(config.ConfigDir(), "skill_stats.json"))

	pm := plugins.NewManager(extAbs, host)
	// 读入插件禁用清单（.disabled.json）——被禁用的插件不加载
	pm.LoadDisabledPlugins()
	// 注意（2026-11-15 审计修复）：此处**不能**把技能禁用名单传给 SetDisabledTools
	// ——那个函数屏蔽的是「工具注册表」（按工具名），而 stats 里存的是技能名。
	// 曾经的接线导致「禁用技能」实际屏蔽了同名工具、且技能照旧注入。
	// 正确的禁用点在下方的 skm.SetDisabledFromList（技能加载后、渲染前）。
	// agent 实例先声明后赋值（builtins.All 的插件热重载回调需要引用它）
	a := agent.New(client, pm)
	// 权限门禁（GAP-S2）：默认 auto（现行为零破坏），config.json permission_mode
	// opt-in。主代理与子代理共用同一实例（进程级策略，/permission 运行时可切）。
	permGate := agent.NewPermissionGate(agent.PermAuto)
	if cfgPerm, err := config.Load(config.DefaultPath()); err == nil && cfgPerm.PermissionMode != "" {
		if m, ok := agent.ParsePermissionMode(cfgPerm.PermissionMode); ok {
			permGate.SetMode(m)
			log.Printf("权限门禁已启用: %s（config.json permission_mode）", m)
		}
	}
	a.Permission = permGate
	// 插件自动热重载：write/edit 写入插件目录内的 .ts/.js 文件后触发。
	// 解决“模型自己写插件 → 未重载就调用 → tool not found 错误循环”的自举断裂问题。
	onPluginChanged := onPluginFileWritten(extAbs, pm, a)
	// 注册 pi 式内置工具（bash/grep/find/read/write/edit/ls/skill_manage + dsh 复刻）
	for _, t := range builtins.All(skAbs, onPluginChanged) {
		pm.RegisterBuiltin(t)
	}
	for _, t := range lsp.All() {
		pm.RegisterBuiltin(t)
	}
	// 显式 reload 工具（自举兜底：模型用 bash 等方式改动插件文件后可主动调用 reload_plugins）
	pm.RegisterBuiltin(plugins.Tool{
		Name:        "reload_plugins",
		Description: "Reload all plugin files from the plugin directory (hot reload). Call this after creating/modifying a plugin (.ts/.js) file via bash or other means so its tool_*/command_* functions take effect. Returns loaded/failed list with compile errors. Args: {}.",
		Run: func(args string) (string, error) {
			return reloadPlugins(pm, a), nil
		},
	})
	lsp.Init(*lspBinary)
	loaded, failed := pm.LoadAll()
	// 同步插件导出的斜杠命令到注册表（NewAgent 时插件尚未加载，
	// 与 helpers.go reloadPlugins 的 SyncFromPlugins 同一套路径）
	pcmds := make([]agent.Command, 0, len(pm.Commands()))
	for _, c := range pm.Commands() {
		cc := c
		pcmds = append(pcmds, agent.Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
	}
	a.Commands.SyncFromPlugins(pcmds)
	for _, f := range loaded {
		log.Printf("插件加载: %s", f)
	}
	for f, e := range failed {
		log.Printf("插件失败: %s: %v", f, e)
	}
	tools := pm.Tools()
	log.Printf("工具 %d | 命令 %d", len(tools), len(pm.Commands()))
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	log.Printf("  工具: %s", strings.Join(names, ", "))

	// 技能使用统计回调（每次工具调用 +1；周报每周提示，距上次 ≥7 天触发）
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.SkillStatsOn() {
		pm.SetOnToolCall(func(name string) {
			stats.Incr(name)
			toolUsageMu.Lock()
			toolUsage[name]++
			toolUsageMu.Unlock()
		})
		// 每周提示使用情况（距上次 ≥7 天触发）
		if stats.DueReport(time.Now()) {
			fmt.Printf("\n%s\n", stats.Report(cfg.SkillStatsTop()))
			stats.MarkReported(time.Now())
		}
	} else if err != nil {
		log.Printf("统计配置读取失败（默认开启）: %v", err)
	}

	// 技能加载（全量渲染，System prompt 字节稳定——禁用只影响工具注册表）
	skm := skills.NewManager(skAbs)
	skLoaded, skFailed := skm.LoadAll()
	// 应用技能禁用名单（来自周报「禁用 xxx」，2026-11-15 审计修复）。
	// 必须在 RenderIndex/RenderAll 之前——禁用后不进系统提示、不出现在 /skills 列表。
	if disabledSkills := stats.DisabledNames(); len(disabledSkills) > 0 {
		skm.SetDisabledFromList(disabledSkills)
		builtins.SetDisabledSkills(disabledSkills) // 工具层复核（skill 工具拒读）
		log.Printf("已禁用 %d 个技能（skill_stats）：%s", len(disabledSkills), strings.Join(disabledSkills, ", "))
	}
	// 技能注册进统计（0 次使用也计入周报最低活跃）
	for _, s := range skm.All() {
		stats.Ensure(s.Name)
	}
	for _, f := range skLoaded {
		log.Printf("技能加载: %s", f)
	}
	for f, e := range skFailed {
		log.Printf("技能失败: %s: %v", f, e)
	}
	// 技能注入：默认索引模式（缓存友好，正文 read 按需加载）；配置 full = 全量正文
	skMode := "index"
	skPrompt := ""
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.SkillInjectIsIndex() {
		// 系统提示只放「常驻 + 按名序前 N」——刻意不放全部：
		// 技能会随项目数量增长，全量注入会把上下文吃光。
		// 与本次任务相关的那些在 Run 里按任务文本补一段（Agent.SkillRelevant）。
		skPrompt = skm.RenderRelevant("", skills.RelevanceOptions{})
	} else {
		skMode = "full"
		skPrompt = skm.RenderAll()
	}
	if skPrompt != "" {
		log.Printf("技能注入 %d 个 (%s)", len(skLoaded), skMode)
	}
	a.SkillsPrompt = skPrompt
	// 按本次任务再补一段相关技能（不破坏 system 前缀缓存：它作为消息插入）。
	a.SkillRelevant = func(task string) string {
		if cfg, err := config.Load(config.DefaultPath()); err == nil && !cfg.SkillInjectIsIndex() {
			return "" // full 模式已经全量注入，不重复
		}
		return skm.RenderRelevant(task, skills.RelevanceOptions{})
	}
	// AGENTS.md 自动读取（全局 ~/.mizar/AGENTS.md + 局部向上查找，相加注入）
	wd := *workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	// 项目信任（P-5 简化版）：工作目录里的 AGENTS.md/.cursorrules/USER.md 与
	// .mizar/prompts/*.md 属「项目级资源」，克隆来的仓库可借此注入指令。
	// 首次进入需确认一次（交互环境询问；非交互按拒绝跳过项目资源，不阻塞）。
	trustStore := trust.NewStore(config.ConfigDir())
	projectTrusted, hadProjectRes := trust.Ensure(trustStore, wd, func(res []string) bool {
		return askProjectTrust(res)
	})
	if hadProjectRes {
		if projectTrusted {
			log.Printf("项目信任: 已信任（%s），注入项目级资源", wd)
		} else {
			log.Printf("项目信任: 未信任（%s），跳过项目级 AGENTS.md/.cursorrules/USER.md/.mizar/prompts（/trust 管理）", wd)
		}
	}
	if hadProjectRes && !projectTrusted {
		// 只放行全局 ~/.mizar 下的文件；项目文件跳过
		globalDir := config.ConfigDir()
		a.ContextAllow = func(path string) bool {
			abs, err := filepath.Abs(path)
			if err != nil {
				return false
			}
			return strings.HasPrefix(abs, globalDir+string(filepath.Separator))
		}
	}
	contextPrompt := context.LoadContextFilesAllowing(wd, a.ContextAllow)
	if contextPrompt != "" {
		log.Printf("上下文文件注入: %s", strings.Join(context.ContextFiles(wd), ", "))
	}
	// SOUL.md 身份声明（跨会话稳定，注入 stable+context 层）
	a.Soul = context.LoadSoul()
	if a.Soul != "" {
		log.Printf("SOUL.md 注入: %s", context.SoulPath())
	}

	// 敏感信息脱敏（2026-11-15，默认开启）：会话落盘/导出/诊断日志里的
	// API key、token、私钥等会被掩码。config.json security_redact=false 可关闭。
	if cfgSec, err := config.Load(config.DefaultPath()); err == nil {
		redact.SetEnabled(cfgSec.SecurityRedactOn())
		if !cfgSec.SecurityRedactOn() {
			log.Printf("⚠ 敏感信息脱敏已关闭（config.json security_redact=false）")
		}
	}

	// 图片读取（P-7，opt-in）：config image_read=true 时 read 工具把图片作为
	// 视觉附件回传（需多模态网关）。默认关闭，避免向非多模态端点发 content 数组。
	if cfgImg, err := config.Load(config.DefaultPath()); err == nil && cfgImg.ImageReadOn() {
		agent.SetImageVisionEnabled(true)
		log.Printf("图片读取已启用（read 工具对图片文件回传视觉附件）")
	}

	// 结构化长期记忆（C-7，对齐 CodeBuddy 四分类）：~/.mizar/memory/MEMORY.md，
	// 每次会话自动注入系统提示；memory 工具可增删改。与 memory 插件（HMEM 语义
	// 检索）互补——这里是小而精、必见的事实清单。
	memStore := memory.NewStore(filepath.Join(config.ConfigDir(), "memory"))
	memInject := true
	if cfgMem, err := config.Load(config.DefaultPath()); err == nil {
		memInject = cfgMem.MemoryOn()
	}
	if err := memStore.Load(); err != nil {
		log.Printf("结构化记忆加载失败（忽略）: %v", err)
	} else if cnt, _ := memStore.Count(); cnt > 0 {
		if memInject {
			log.Printf("结构化记忆注入: %d 条（%s）", cnt, memStore.Path())
		} else {
			log.Printf("结构化记忆已加载 %d 条，但自动注入被关闭（config.json memory_enabled=false）", cnt)
		}
	}
	if memInject {
		a.MemoryPrompt = memStore.Render()
	}

	// 会话存储（模仿 pi Agent：按工作路径自动分目录，路径记忆）
	sessAbs := *sessDir
	if strings.HasPrefix(sessAbs, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			sessAbs = filepath.Join(home, strings.TrimPrefix(sessAbs, "~/"))
		}
	}
	st := session.New(sessAbs)
	// 会话全文搜索（H-1）：SQLite FTS5 增量索引，放在会话根目录（跨作用域共享）。
	// 打不开（磁盘只读/FTS5 不可用）时降级为 nil——session_search 工具不注册，
	// 其余功能完全不受影响。
	ssIdx, ssErr := sessionsearch.Open(filepath.Join(sessAbs, "search.db"))
	if ssErr != nil {
		log.Printf("会话搜索索引不可用（忽略）: %v", ssErr)
		ssIdx = nil
	}
	if ssIdx != nil {
		defer ssIdx.Close()
	}
	pathKey := session.EncodePathKey(wd)
	if pathKey != "" {
		st = st.Scoped(pathKey)
		log.Printf("路径记忆: 会话目录 %s/%s", sessAbs, pathKey)
	}
	// -c / -continue：续接「全局最后一次退出的会话」。
	// 状态记录在会话根目录（~/.mizar/sessions/last-session.json）这一唯一文件中，
	// 与当前工作路径无关（用户要求：全局最后一个，而不是某个路径下的最后一个）。
	// 未指定 -session 时生效。
	if (*contLast || *contLast2) && *sessionID == "" {
		if id, scope := st.Last(); id != "" {
			// 该会话可能位于其它路径作用域（或平铺存储），切换到对应作用域以便加载
			st = session.New(sessAbs)
			if scope != "" {
				st = st.Scoped(scope)
			}
			pathKey = scope
			if st.Exists(id) {
				*sessionID = id
				log.Printf("-c: 续接全局最后退出的会话 %s（作用域 %q）", id, scope)
			}
		}
		if *sessionID == "" {
			log.Printf("-c: 未找到上次退出的会话，改为新建会话")
		}
	}
	// 路径记忆：未指定 -session 时，自动创建新 UUID 会话（不再自动恢复 latest）
	// 用户需显式用 -session <id> 续接历史会话
	// （task 单次执行不自动创建，保持显式 -session 才落盘的原行为）
	if *sessionID == "" && pathKey != "" && *task == "" && !*serve {
		*sessionID = uuid.New().String()
		log.Printf("路径记忆: 新会话 %s（当前路径）", *sessionID)
	}

	a.PluginDir = extAbs
	a.WorkDir = wd
	// 默认启用弱模型宽容策略：死循环检测 + 解析失败降级 + LLM 故障重试
	a.Tuner = agent.DefaultTuner()
	// 完成度自检：模型在待办清单还有未完成项时就说「完成了」结束本轮，
	// 这里拦一下并注入提醒继续跑（修「没做完就停，必须手打继续」）。
	// Pending 注入而非在 agent 包里直接读 builtins，避免双向依赖。
	// 压缩后重注入待办（H-2，对齐 hermes format_for_injection）：压缩摘要会
	// 吃掉 todo 结果，模型随即遗忘未完成项。注入 pending 列表让主循环继续。
	a.TodoInject = builtins.RenderForInjection
	a.Completeness = agent.NewCompletenessGate(func() []string {
		var pending []string
		for _, t := range builtins.PiTodoList() {
			if !t.Done {
				pending = append(pending, t.Text)
			}
		}
		return pending
	})
	// 从配置读取重试参数（0 = 保持默认）
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		a.Tuner.WithRetryConfig(cfg.RetryMaxRetries, cfg.RetryBaseDelayMs)
	}
	// 后台任务注册表（bash run_in_background + job_* 工具共用）
	a.Jobs = jobs.NewRegistry()
	// 会话目标服务
	goalSvc := agent.NewGoalService()
	a.GoalService = goalSvc
	// GoalLoop：每轮后自动 judge，续跑直到目标完成（复刻 hermes Ralph loop）
	a.GoalLoop = agent.NewGoalLoop(goalSvc, agent.DefaultGoalLoopConfig())
	a.GoalLoop.SetCwd(wd)
	// 辅助 judge 函数：用主 LLM
	auxLLM := a.LLM
	a.AuxLLM = auxLLM
	goalSvc.SetJudgeFn(agent.BuildDefaultJudgeFn(auxLLM))
	// Subagent 管理器：共享 LLM + 插件注册表，结果通过 Jobs 完成通知回灌主循环。
	// 用 a.LLM（LLM 接口，Run 内部按能力选 tools/ctx），不再只取 ToolCallLLM——
	// 后者会把子 agent 困在「无工具、不可取消」的纯聊天壳里。
	a.SubAgents = agent.NewSubagentManager(agent.SubagentConfig{
		ParentLLM:  a.LLM,
		ParentSys:  a.System,
		Plugins:    pm,
		Permission: permGate,
		// 上下文压缩 + 弱模型宽容（2026-11-15 审计修复）：此前子代理这两项恒 nil，
		// 长任务会撞上下文上限直接失败。注意 Compactor 是共享实例：它只读取
		// ContextWindow/Reserve/Keep 策略，统计状态在每次 Compact 调用内局部计算。
		Compactor: a.Compactor,
		Tuner:     a.Tuner,
		JobNotify: func(note string) {
			// 复用 Jobs.DrainDone() 已挂进主循环(loop.go:655)的完成通知通道：
			// Start 一个瞬时即完的任务，DrainDone 会把 note 注入主对话。
			a.Jobs.Start("subagent", note, func() (string, error) { return note, nil })
		},
	})
	// 子任务句柄终态落盘（GAP-S4）：快照以 <!--subagent-state:--> 标记消息随会话
	// JSONL 持久化，恢复会话时回放重建（RestoreFromMessages），get_subagent /
	// list_subagents / /subagents 跨进程重启仍可查历史子任务与结论。
	a.SubAgents.SetPersister(func(snap agent.SubagentHandle) {
		if *sessionID == "" {
			return
		}
		_ = st.Append(*sessionID, agent.SubagentStateMessage(snap))
	})
	// BackgroundReview：后台自学习（每 3 轮起一个一次性 goroutine 复盘，写入技能/记忆）。
	//
	// 2026-11-15 审计修复：此前只 New 不 Start，而 Advance() 依赖 running 标志 →
	// 该功能实际从未工作。现在改成「只看配置」，无需常驻循环；
	// 记忆写入走 OnMemory 回调写进结构化 MEMORY.md（避免裸 append 破坏四分类格式）。
	homeDir, _ := os.UserHomeDir()
	skillsDir := filepath.Join(homeDir, ".mizar", "skills")
	bgReviewEnabled := true
	if cfgBg, err := config.Load(config.DefaultPath()); err == nil {
		bgReviewEnabled = cfgBg.SkillEvolutionEnabled()
	}
	a.BgReview = agent.NewBackgroundReview(agent.BackgroundReviewConfig{
		Enabled:                &bgReviewEnabled,
		MinTurnsBetweenReviews: 3,
		AuxLLM:                 auxLLM,
		SkillsDir:              skillsDir,
		// 项目技能沉淀：把当前工作目录带进去，review 会产出/续写
		// `project-<目录名>` 技能（落在全局技能库，不写进项目目录）。
		// 下次在同项目里开工，这条技能一加载即恢复该项目的相关记忆。
		WorkDir: wd,
		OnMemory: func(entry string) error {
			if !memInject {
				return nil // 记忆自动注入被关闭时不写，避免用户看不到却占存储
			}
			if err := memStore.Add(memory.CategoryProject, entry); err != nil {
				return err
			}
			a.SetMemoryPrompt(memStore.Render()) // 写入即生效
			return nil
		},
		OnReview: func(summary string) {
			log.Printf("[bg-review] %s", summary)
			// 技能可能刚被写入/更新：内存里的 skm 不会自动重扫，必须重载，
			// 否则新沉淀的项目技能要等下次启动才进系统提示（本该「下次聊到就恢复」
			// 的语义就断了）。重载后重建技能提示段并失效提示缓存。
			skm.LoadAll()
			a.SkillsPrompt = renderSkillsPrompt(skm, cfgSkillMode())
			a.ReloadTools()
		},
	})
	if bgReviewEnabled {
		log.Printf("后台自学习已启用：每 3 轮复盘一次，技能写入 %s，记忆写入 MEMORY.md(project)", skillsDir)
	} else {
		log.Printf("后台自学习已关闭（config.json skill_evolution=false）")
	}
	// 用户自定义子代理类型（GAP-S1 第二层）：~/.mizar/agents/*.md，
	// frontmatter(name/description/tools/model) + 正文 = system 附加层。
	// 加载须在 delegate_task 注册（下方 DelegateTool）之前——类型清单要写进工具描述。
	agentTypesDir := filepath.Join(homeDir, ".mizar", "agents")
	if agentTypes, errs := agent.LoadAgentTypeFiles(agentTypesDir); len(agentTypes) > 0 {
		a.SubAgents.SetAgentTypes(agentTypes)
		log.Printf("已加载 %d 个自定义子代理类型（%s/*.md）", len(agentTypes), agentTypesDir)
	} else if len(errs) > 0 {
		for _, e := range errs {
			log.Printf("%v", e)
		}
	}
	// StatsData
	a.StatsData = &agent.StatsDataRef{}
	// 注册后台任务工具（与 a.Jobs 同一 registry 实例，保证跨工具一致性）
	for _, t := range builtins.JobTools(a.Jobs) {
		pm.RegisterBuiltin(t)
	}
	// 技能检索工具（2026-11-15）：技能很多时系统提示只列最相关的几条，
	// 其余用 skill_search 按关键词找回（按需加载，不占常驻上下文）。
	pm.RegisterBuiltin(builtins.SkillSearchTool(skm))

	// 结构化长期记忆工具（C-7）：写入后刷新注入段，使新事实当轮即可见。
	pm.RegisterBuiltin(builtins.MemoryTool(memStore, func() {
		if memInject {
			a.SetMemoryPrompt(memStore.Render())
		}
	}))
	// 会话全文搜索工具（H-1）：跨会话按内容检索历史。
	if ssIdx != nil {
		pm.RegisterBuiltin(builtins.SessionSearchTool(ssIdx, sessAbs))
	}

	// worktree 隔离工具（C-8）：worktree_create/leave/list，为实验性改动提供
	// 独立 git worktree（配 delegate_task 的 working_dir 使用）。以当前工作目录
	// 作为仓库锚点；非 git 仓库时工具会在调用时返回明确错误（不影响启动）。
	for _, t := range builtins.WorktreeTools(wd) {
		pm.RegisterBuiltin(t)
	}
	// 延迟工具加载（C-9，opt-in）：config.json tool_inject_mode=index 时，
	// 系统提示只列核心工具 + 其余索引，模型用 tool_search 按需加载。
	// tool_search 自身永远注册（index 模式下它被核心集保留）。
	pm.RegisterBuiltin(agent.ToolSearchTool(a))
	if cfgTI, err := config.Load(config.DefaultPath()); err == nil && cfgTI.ToolInjectIsIndex() {
		a.SetToolInjectMode(agent.ToolInjectIndex)
		log.Printf("工具注入: index（按需加载，其余走 tool_search）")
	}

	// ask_user_question：TUI 模式下由 tui.go 设置回调；非 TUI 保持 nil（降级提示）
	// 全局可访问的计划模式实例（供 builtins 工具引用）
	agent.CurrentPlanMode = a.PlanMode
	// 定时提醒注册表（投递回调：到期时打印到终端）
	schedReg := schedule.NewRegistry(func(id, prompt string) {
		log.Printf("[schedule] 提醒到期: %s — %s", id, prompt)
		// TUI 模式下不能直接 Printf 到终端：那会盖在 tcell 画出的界面上，
		// 留下永远擦不掉的残留文字（见 tui_output.go）。改走聊天区。
		if tuiDeliverReminder != nil && tuiOutputActive() {
			tuiDeliverReminder(prompt)
			return
		}
		fmt.Printf("\n⏰ 【定时提醒】%s\n", prompt)
	})
	// 注册 goal + schedule 工具
	for _, t := range builtins.GoalTools(goalSvc) {
		pm.RegisterBuiltin(t)
	}
	// 新增 goal 工具：add_gate / add_subgoal / clear_subgoals
	for _, t := range builtins.GoalExtendedTools(goalSvc) {
		pm.RegisterBuiltin(t)
	}
	// 子代理委派工具（支持 tasks[] 并行 fan-out）
	pm.RegisterBuiltin(agent.DelegateTool(a.SubAgents))
	pm.RegisterBuiltin(agent.GetSubagentTool(a.SubAgents))
	pm.RegisterBuiltin(agent.ListSubagentsTool(a.SubAgents))
	pm.RegisterBuiltin(agent.CancelSubagentTool(a.SubAgents))
	pm.RegisterBuiltin(agent.SteerSubagentTool(a.SubAgents))
	// P2 自动分解：把委派策略注入主 system prompt（何时该拆/怎么拆/何时别拆）。
	// 拼在用户自定义指令之后，不覆盖 a.System 本身。
	if g := agent.DelegationGuide(); g != "" {
		a.System = a.System + "\n" + g
	}
	// 用户级 hooks（C-6）：config.json hooks 段配 shell 命令响应工具事件，
	// pre_tool_use 可 deny/rewrite（exit 2 或 stdout JSON），post_tool_use 只观察。
	// a.Hooks 需在此确保非空（Run 内才惰性创建，注册挂载点必须先有实例）。
	if a.Hooks == nil {
		a.Hooks = agent.NewHooks()
	}
	if cfgH, err := config.Load(config.DefaultPath()); err == nil && !cfgH.Hooks.Empty() {
		hookLogf := func(format string, args ...any) { diag.Logf("userhooks", format, args...) }
		if fn := cfgH.Hooks.PreToolDecision(hookLogf); fn != nil {
			a.Hooks.OnToolCallDecision(func(ctx *agent.HookContext) (agent.Decision, string) {
				r := fn(ctx.Tool, ctx.Args, ctx.SessionID)
				switch r.Decision {
				case userhooks.DecisionDeny:
					return agent.DecisionDeny, r.Reason
				case userhooks.DecisionRewrite:
					if strings.TrimSpace(r.NewTool) != "" {
						ctx.Tool = r.NewTool
					}
					if r.NewArgs != "" {
						ctx.Args = r.NewArgs
					}
					return agent.DecisionRewrite, r.Reason
				}
				return agent.DecisionAllow, ""
			})
		}
		if fn := cfgH.Hooks.PostToolObserve(hookLogf); fn != nil {
			a.Hooks.OnToolResult(func(ctx *agent.HookContext) error {
				fn(ctx.Tool, ctx.Args, ctx.Result, ctx.Err, ctx.SessionID)
				return nil
			})
		}
		log.Printf("用户级 hooks 已接线: pre_tool_use=%d post_tool_use=%d",
			len(cfgH.Hooks.PreToolUse), len(cfgH.Hooks.PostToolUse))
	}
	// Skill Manager 工具（agent 可自创 skill）
	pm.RegisterBuiltin(builtins.SkillManagerTool(skillsDir))
	// Session Insights 工具
	pm.RegisterBuiltin(builtins.InsightsTool(a.StatsData.ToInsightsData()))
	for _, t := range builtins.ScheduleTools(schedReg) {
		pm.RegisterBuiltin(t)
	}
	// 会话标题缓存
	titleCache, _ := session.NewTitleCache(*sessDir)
	// 从配置读取最大步数（0=默认 60）
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.MaxSteps != 0 {
		a.MaxSteps = cfg.MaxSteps
	}
	a.System = `你是开阳(Mizar) Agent，一个极简自举的智能体。你可以调用工具完成任务，工具出错时尝试修复或换一种方式。请用简洁的中文回答。

## 任务聚焦（重要）
- 工具拿来即用：可用工具列表中的工具（如 mem_write/bash/read/write 等）描述已说明用途与参数，直接调用即可，不要为了确认功能而先读取插件源码或二进制。插件源码只在「开发新工具」时读。
- 工具出错先看错误信息：根据错误修正调用参数重试，而不是一开始就修改工具本身的实现（插件改动写入插件目录会自动重载生效；用 bash 改动插件后需调用 reload_plugins 工具；日常任务不要中途改已加载的插件）。
- 写代码/插件时：按系统提示末尾「插件」段给出的宿主函数清单与最小示例直接创建文件，不要在源码或二进制里搜索 API 定义。
- 避免空转：同一方向最多探索 2 次；连续 2 次工具调用未获得新信息立即换更直接的方案。strings/find/grep 换花样搜同一目标属于空转。
- 分步交付：任务无法在几步内完成时，先完成核心部分并及时给出阶段性回复。`
	a.VerboseLog = func(msg string) { log.Print(msg) }
	// Prompt Templates：~/.mizar/prompts/*.md + 项目 .mizar/prompts/*.md
	promptReg := prompts.NewRegistry()
	promptReg.AddDir(filepath.Join(config.ConfigDir(), "prompts"))
	// 项目级 prompts 只在项目被信任时加载（P-5：防克隆仓库注入斜杠命令）
	if projDir := findMizarDir(wd); projDir != "" && projectTrusted {
		promptReg.AddDir(filepath.Join(projDir, ".mizar", "prompts"))
	}
	promptReg.Reload()
	log.Printf("Prompt 模板加载 %d 个", len(promptReg.List()))
	// 注入 /handoff 命令：快速生成交接文档（固化进程序，非插件）。
	// 传入 a 以纳入会话上下文（当前任务/历史/工具轨迹/todo/目标）——
	// 仅 git 信息无法回答「刚才在干什么、下一步做什么」。
	a.Commands.Register(agent.Command{
		Name:        "handoff",
		Description: "生成交接文档 handoff.md（含当前任务、最近对话与工具轨迹；/handoff [备注/下一步]）",
		Run: func(args string) (string, error) {
			return writeHandoff(wd, args, a), nil
		},
	})
	// 注入 /templates 命令
	a.Commands.Register(agent.Command{
		Name:        "templates",
		Description: "列出可用 prompt 模板（/name [args] 展开）",
		Run: func(args string) (string, error) {
			return promptReg.Help(), nil
		},
	})
	// 注入 /template 命令（别名 /prompt）
	a.Commands.Register(agent.Command{
		Name:        "template",
		Description: "展开 prompt 模板（/template <name> [args]）",
		Run: func(args string) (string, error) {
			fields := strings.Fields(strings.TrimSpace(args))
			if len(fields) == 0 {
				return promptReg.Help(), nil
			}
			name := fields[0]
			rest := ""
			if len(fields) > 1 {
				rest = strings.Join(fields[1:], " ")
			}
			out, err := promptReg.Expand(name, rest)
			if err != nil {
				return "", err
			}
			return out, nil
		},
	})

	// 桥接 Agent Hooks → 插件 hook_on 回调：所有挂载点转发给插件
	a.Hooks = agent.NewHooks()
	bridgeHook := func(evt string) agent.HookFunc {
		return func(ctx *agent.HookContext) error {
			_, errs := pm.FireHook(evt, plugins.MarshalCtx(ctx))
			for _, e := range errs {
				log.Printf("插件 hook %s: %v", evt, e)
			}
			return nil
		}
	}
	a.Hooks.
		OnRunStart(bridgeHook("RunStart")).
		OnRunEnd(bridgeHook("RunEnd")).
		OnStepStart(bridgeHook("StepStart")).
		OnStepEnd(bridgeHook("StepEnd")).
		OnLLMRequest(bridgeHook("LLMRequest")).
		OnLLMResponse(bridgeHook("LLMResponse")).
		OnToolCall(bridgeHook("ToolCall")).
		OnToolResult(bridgeHook("ToolResult")).
		OnCompactionBefore(bridgeHook("CompactionBefore")).
		OnCompactionAfter(bridgeHook("CompactionAfter")).
		OnError(bridgeHook("Error"))

	// 内置 /provider 命令：多供应商收藏夹（add/del/use/list）与切换（实现见 providercmd.go）
	a.Commands.Register(newProviderCommand(client, a, ctxWindow))
	// 内置 /model 命令：查看/切换模型（实现见 modelcmd.go）。
	// TUI 下输入 /model 会额外弹出 ↑↓ 选择器（见 tui.go 的 modelPickerActive 分支）。
	a.Commands.Register(newModelCommand(
		"查看/列出模型（/model 列出供应商可用模型，TUI 下可 ↑↓ 选择）或切换（/model <name>）",
		func() ([]string, error) { return config.ListModels(client.BaseURL, client.APIKey) },
		func() string { return client.Model },
		func(name string) (string, error) {
			old := client.Model
			client.Model = name
			// 持久化到 config.json
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
			cfg.UserAgent = client.UserAgent()
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				log.Printf("/model 持久化失败: %v", err)
				return "", fmt.Errorf("模型已切换为 %s 但写入配置失败: %w", name, err)
			}
			// 若当前生效的是收藏夹里的 provider，把模型名同步进去（下次 use 时恢复）
			syncActiveProviderModel(client.Model)
			return fmt.Sprintf("✓ 模型已切换: %s → %s", old, name), nil
		},
	))
	// 内置 /shell 命令：手动切换命令执行器（详见 shellcmd.go）。
	// 切换后刷新系统提示（「命令行环境」一节要跟着变），并持久化到 config.json。
	a.Commands.Register(newShellCommand(persistShell, a.ReloadTools))
	// 内置 /mouse 命令：运行期开关鼠标上报（详见 mousecmd.go）。
	a.Commands.Register(newMouseCommand(persistMouse))
	// 内置 /zig 命令：释放/管理内嵌 Zig 工具链（详见 zigcmd.go）
	registerZigCommand(a)
	// 内置 /apikey 命令：查看/设置 API Key（与 /provider 配合使用）
	a.Commands.Register(agent.Command{
		Name:        "apikey",
		Description: "查看当前 API Key（/apikey）或设置（/apikey <key>）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				keyMask := "<未设置>"
				if client.APIKey != "" {
					keyMask = fmt.Sprintf("%s...%s", client.APIKey[:2], client.APIKey[len(client.APIKey)-2:])
				}
				return fmt.Sprintf("当前 API Key: %s\n当前供应商: %s\n设置方法: /apikey <key>（或 /provider <baseURL> <key> 一步到位）", keyMask, client.BaseURL), nil
			}
			old := client.APIKey
			client.APIKey = args
			// 持久化（并同步当前生效 provider 收藏项）
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
			cfg.UserAgent = client.UserAgent()
			if p := cfg.FindProvider(cfg.ActiveProvider); p != nil {
				p.APIKey = client.APIKey
			}
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				log.Printf("/apikey 持久化失败: %v", err)
			}
			if old == "" {
				return "✓ API Key 已设置", nil
			}
			return "✓ API Key 已更新", nil
		},
	})
	// 内置 /reload 命令：重载 ~/.mizar/config.json + 插件热重载
	a.Commands.Register(agent.Command{
		Name:        "reload",
		Description: "重载 ~/.mizar/config.json 与插件（/reload）",
		Run: func(args string) (string, error) {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil || cfg.BaseURL == "" {
				return "", fmt.Errorf("重载失败: %v（检查 %s）", err, config.DefaultPath())
			}
			// 应用配置到客户端
			client.BaseURL = cfg.BaseURL
			client.APIKey = cfg.APIKey
			client.Model = cfg.Model
			client.SetUserAgent(cfg.UserAgent)
			if cfg.ThinkingStr() != "off" {
				client.SetThinkingLevel(cfg.ThinkingStr())
			} else {
				client.SetThinkingLevel("")
			}
			// 应用上下文窗口
			if cfg.ContextWindow > 0 {
				*ctxWindow = cfg.ContextWindow
				if a.Compactor != nil {
					a.Compactor.ContextWindow = cfg.ContextWindow
				}
			}
			// 插件热重载 + 命令同步 + 系统提示缓存失效（统一逻辑见 reloadPlugins）
			pluginSummary := reloadPlugins(pm, a)
			// Prompt 模板热重载
			promptReg.Reload()
			// 汇总
			var sb strings.Builder
			fmt.Fprintf(&sb, "✓ 配置已重载: model=%s window=%d thinking=%s\n", cfg.Model, cfg.ContextWindow, cfg.ThinkingStr())
			fmt.Fprintf(&sb, "%s\n", pluginSummary)
			return sb.String(), nil
		},
	})
	// 内置 /list_plugins：列出插件及其状态（已加载/已禁用）
	a.Commands.Register(agent.Command{
		Name:        "list_plugins",
		Description: "列出插件及状态（已加载/已禁用）",
		Run: func(args string) (string, error) {
			all := pm.PluginFiles()
			if len(all) == 0 {
				return fmt.Sprintf("插件目录 %s 无插件", pm.Dir()), nil
			}
			loaded := map[string]bool{}
			for _, f := range pm.LoadedPlugins() {
				loaded[f] = true
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "插件目录: %s\n共 %d 个:\n", pm.Dir(), len(all))
			for _, f := range all {
				status := "✅ 已加载"
				if pm.PluginDisabled(f) {
					status = "⛔ 已禁用"
				} else if !loaded[f] {
					status = "⚠️ 未加载（加载失败？）"
				}
				fmt.Fprintf(&sb, "  %s %s\n", status, f)
			}
			sb.WriteString("\n命令: /enable_plugin <名> · /disable_plugin <名> · /reload_plugins")
			return sb.String(), nil
		},
	})
	// 内置 /enable_plugin：启用插件（取消禁用并加载）
	a.Commands.Register(agent.Command{
		Name:        "enable_plugin",
		Description: "启用插件（/enable_plugin <文件名，可省略 .ts>）",
		Run: func(args string) (string, error) {
			name := resolvePluginName(pm, args)
			if name == "" {
				return "用法: /enable_plugin <文件名>（用 /list_plugins 查看）", nil
			}
			if !pm.PluginDisabled(name) {
				return fmt.Sprintf("插件 %s 本就处于启用状态", name), nil
			}
			pm.SetPluginDisabled(name, false)
			summary := reloadPlugins(pm, a)
			return fmt.Sprintf("✅ 已启用 %s\n%s", name, summary), nil
		},
	})
	// 内置 /skills 命令：列出技能、启用/禁用（2026-11-15 审计修复：
	// 周报里写着「回复『禁用 <技能名>』可将其禁用」，但此前没有任何代码解析这句话，
	// 禁用名单还被错传给 SetDisabledTools（那是屏蔽工具用的）。现在提供正式入口。）
	a.Commands.Register(agent.Command{
		Name:        "skills",
		Description: "技能列表与启停（/skills 列出 ｜ /skills disable <名> 禁用 ｜ /skills enable <名> 启用 ｜ /skills stats 使用统计）",
		Run: func(args string) (string, error) {
			fields := strings.Fields(strings.TrimSpace(args))
			sub := ""
			if len(fields) > 0 {
				sub = strings.ToLower(fields[0])
			}
			switch sub {
			case "", "list":
				all := skm.All()
				if len(all) == 0 {
					return "（无技能）", nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "技能 %d 个（禁用的不列出；/skills disable <名> 可禁用）：\n", len(all))
				for _, sk := range all {
					cat := sk.Category
					if cat == "" {
						cat = "未分类"
					}
					fmt.Fprintf(&sb, "  • %-24s [%s] %s\n", sk.Name, cat, truncateStr(sk.Description, 60))
				}
				if dis := stats.DisabledNames(); len(dis) > 0 {
					fmt.Fprintf(&sb, "\n🔕 已禁用 %d 个：%s\n", len(dis), strings.Join(dis, ", "))
				}
				return strings.TrimSpace(sb.String()), nil
			case "stats":
				cfg, _ := config.Load(config.DefaultPath())
				topN := 10
				if cfg != nil {
					topN = cfg.SkillStatsTop()
				}
				return stats.Report(topN), nil
			case "disable", "off":
				if len(fields) < 2 {
					return "", fmt.Errorf("用法: /skills disable <技能名>")
				}
				name := fields[1]
				if _, ok := skm.Get(name); !ok && !stats.IsDisabled(name) {
					return "", fmt.Errorf("技能 %q 不存在（/skills 查看）", name)
				}
				stats.SetDisabled(name, true)
				skm.SetDisabledFromList(stats.DisabledNames())
				builtins.SetDisabledSkills(stats.DisabledNames())
				a.SkillsPrompt = renderSkillsPrompt(skm, cfgSkillMode())
				a.ReloadTools() // 让系统提示重建（技能索引段变化）
				return fmt.Sprintf("✓ 已禁用技能 %s（不再注入系统提示，skill 工具也拒读；/skills enable %s 可恢复）", name, name), nil
			case "enable", "on":
				if len(fields) < 2 {
					return "", fmt.Errorf("用法: /skills enable <技能名>")
				}
				name := fields[1]
				stats.SetDisabled(name, false)
				skm.SetDisabledFromList(stats.DisabledNames())
				builtins.SetDisabledSkills(stats.DisabledNames())
				skm.LoadAll() // 重新加载磁盘技能（可能之前根本没加载到内存）
				a.SkillsPrompt = renderSkillsPrompt(skm, cfgSkillMode())
				a.ReloadTools()
				return fmt.Sprintf("✓ 已启用技能 %s", name), nil
			default:
				return "", fmt.Errorf("用法: /skills ｜ /skills disable <名> ｜ /skills enable <名> ｜ /skills stats")
			}
		},
	})
	// 内置 /disable_plugin：禁用插件（卸载并持久化）
	a.Commands.Register(agent.Command{
		Name:        "disable_plugin",
		Description: "禁用插件（/disable_plugin <文件名，可省略 .ts>）",
		Run: func(args string) (string, error) {
			name := resolvePluginName(pm, args)
			if name == "" {
				return "用法: /disable_plugin <文件名>（用 /list_plugins 查看）", nil
			}
			if pm.PluginDisabled(name) {
				return fmt.Sprintf("插件 %s 本就处于禁用状态", name), nil
			}
			pm.SetPluginDisabled(name, true)
			// 同步命令注册表（该插件导出的命令需移除）
			cmds := make([]agent.Command, 0, len(pm.Commands()))
			for _, c := range pm.Commands() {
				cc := c
				cmds = append(cmds, agent.Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
			}
			a.Commands.SyncFromPlugins(cmds)
			a.ReloadTools()
			return fmt.Sprintf("⛔ 已禁用 %s（已卸载，配置已持久化）", name), nil
		},
	})
	// 内置 /reload_plugins：强制重载全部插件（跳过已禁用）
	a.Commands.Register(agent.Command{
		Name:        "reload_plugins",
		Description: "重载全部插件（/reload_plugins，已禁用的仍保持禁用）",
		Run: func(args string) (string, error) {
			return reloadPlugins(pm, a), nil
		},
	})
	// 内置 /new 命令：直接创建新会话（= /session new 快捷方式）
	a.Commands.Register(agent.Command{
		Name:        "new",
		Description: "直接创建新会话（清空上下文并换新 session id，相当于 /session new）",
		Timeout:     10 * time.Second,
		Run: func(args string) (string, error) {
			*sessionID = uuid.New().String()
			a.Initial = nil
			short := *sessionID
			if len(short) > 8 {
				short = short[:8]
			}
			log.Printf("切换到新会话: %s", short)
			return fmt.Sprintf("✓ 新会话: %s", short), nil
		},
	})
	// 内置 /reset 命令：清空会话上下文（参照 pi 的 /new）
	a.Commands.Register(agent.Command{
		Name:        "reset",
		Description: "重置会话上下文（清空历史，保留配置/插件）",
		Run: func(args string) (string, error) {
			a.Reset()
			return "✓ 会话已重置（历史已清空）", nil
		},
	})
	// 内置 /plan 命令：进入/退出计划模式（复刻 dsh plan-mode）
	a.Commands.Register(agent.Command{
		Name:        "plan",
		Description: "计划模式（/plan 进入 ｜ /plan status 查看 ｜ /plan off 或 /plan exit 退出 ｜ /plan <指令> 附带规划指引）",
		Run: func(args string) (string, error) {
			arg := strings.TrimSpace(args)
			switch {
			case agent.IsModeExitArg(arg):
				a.PlanMode.Exit()
				return "✓ 已退出计划模式。模型可直接执行任务（之后仍可 /plan 重新进入）。", nil
			case strings.EqualFold(arg, "status") || arg == "状态" || arg == "查看":
				if !a.PlanMode.IsActive() {
					return "当前未处于计划模式。输入 /plan 进入。", nil
				}
				return "当前处于计划模式。\n  · 正常流程：模型调用 exit_plan_mode 工具提交计划，你在弹窗里选 Approve / Keep planning\n  · 想强行退出（不审批）：输入 /plan off（或 /plan exit）\n  · 当前规划指引：" + a.PlanMode.Guidance(), nil
			}
			guidance := ""
			if !agent.IsModeEnterArg(arg) {
				guidance = arg
			}
			a.PlanMode.Enter(guidance)
			msg := "✓ 已进入计划模式。模型将先探索设计方案，再调用 exit_plan_mode 工具提交计划供你审批。"
			if guidance != "" {
				msg += fmt.Sprintf("\n规划指引：%s", guidance)
			}
			msg += "\n\n退出方式：\n  · 审批时选 Approve（自动退出并开始执行）\n  · 随时输入 /plan off（或 /plan exit）强制退出"
			return msg, nil
		},
	})
	// 内置 /grill 命令：拷问模式（动手前把需求问到没有歧义）
	a.Commands.Register(agent.Command{
		Name:        "grill",
		Description: "拷问模式（/grill 进入 ｜ /grill status 查看 ｜ /grill off 或 /grill exit 退出 ｜ /grill <指令> 自定义拷问重点）",
		Run: func(args string) (string, error) {
			arg := strings.TrimSpace(args)
			switch {
			case agent.IsModeExitArg(arg):
				a.GrillMode.Exit()
				return "✓ 已退出拷问模式。模型可直接按现有理解动手（之后仍可 /grill 重新进入）。", nil
			case strings.EqualFold(arg, "status") || arg == "状态" || arg == "查看":
				if !a.GrillMode.IsActive() {
					return "当前未处于拷问模式。输入 /grill 进入。", nil
				}
				return "当前处于拷问模式：模型会先用 ask_user_question 追问需求（每轮最多 3 问，每题带编号选项），\n需求收敛后输出「确认清单 + 风险与默认假设」并停下等指令。\n  · 退出：/grill off（或 /grill exit）\n  · 想直接看方案：先 /grill off 再 /plan", nil
			}
			guidance := ""
			if !agent.IsModeEnterArg(arg) {
				guidance = arg
			}
			a.GrillMode.Enter(guidance)
			msg := "✓ 已进入拷问模式。模型会先复述理解，再用 ask_user_question 逐层追问（每轮最多 3 问，每题给编号选项，直接回数字即可）。"
			if guidance != "" {
				msg += fmt.Sprintf("\n拷问重点：%s", guidance)
			}
			msg += "\n\n退出方式：\n  · 需求问完会自动停（输出确认清单，不会自己开工）\n  · 随时输入 /grill off（或 /grill exit）强制退出"
			return msg, nil
		},
	})
	// 内置 /goal 命令：会话目标管理（复刻 pi-goal）
	a.Commands.Register(agent.Command{
		Name:        "goal",
		Description: "会话目标管理（/goal <目标> 创建 ｜ /goal status 查看 ｜ /goal pause/resume/complete/clear 操作）",
		Run: func(args string) (string, error) {
			if a.GoalService == nil {
				return "⚠ GoalService 未初始化", nil
			}
			arg := strings.TrimSpace(args)
			// 无参数或 status：显示当前目标
			if arg == "" || arg == "status" || arg == "查看" || arg == "状态" {
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 当前没有活跃目标。使用 /goal <目标描述> 创建新目标。", nil
				}
				phaseLabel := map[agent.GoalPhase]string{
					agent.GoalPending:    "待处理",
					agent.GoalInProgress: "进行中",
					agent.GoalPaused:     "已暂停",
					agent.GoalCompleted:  "已完成",
					agent.GoalBlocked:    "已阻塞",
				}
				label := phaseLabel[g.Phase]
				if g.BlockedReason != "" {
					label += fmt.Sprintf("（%s）", g.BlockedReason)
				}
				msg := fmt.Sprintf("🎯 当前目标\n  目标：%s\n  状态：%s\n  轮次：%d",
					g.Objective, label, g.RoundsStarted)
				if g.MaxGoalRounds > 0 {
					msg += fmt.Sprintf("（上限 %d 轮）", g.MaxGoalRounds)
				}
				return msg, nil
			}
			fields := strings.Fields(arg)
			action := fields[0]
			switch action {
			case "pause", "暂停":
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。先用 /goal <目标> 创建。", nil
				}
				if err := a.GoalService.Update(g.ID, fmt.Sprintf("%d", g.Revision), "pause", "", 0, ""); err != nil {
					return fmt.Sprintf("✗ 暂停失败：%v", err), nil
				}
				return "✓ 目标已暂停。使用 /goal resume 恢复。", nil
			case "resume", "恢复":
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				if err := a.GoalService.Update(g.ID, fmt.Sprintf("%d", g.Revision), "resume", "", 0, ""); err != nil {
					return fmt.Sprintf("✗ 恢复失败：%v", err), nil
				}
				return "✓ 目标已恢复执行。", nil
			case "complete", "完成":
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				if err := a.GoalService.Update(g.ID, fmt.Sprintf("%d", g.Revision), "complete", "", 0, ""); err != nil {
					return fmt.Sprintf("✗ 标记完成失败：%v", err), nil
				}
				return "✓ 目标已标记为完成。", nil
			case "clear", "清除", "删除":
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				// 将目标设为 completed 来清除
				if err := a.GoalService.Update(g.ID, fmt.Sprintf("%d", g.Revision), "complete", "", 0, ""); err != nil {
					return fmt.Sprintf("✗ 清除失败：%v", err), nil
				}
				return "✓ 目标已清除。", nil
			case "blocked", "阻塞":
				reason := ""
				if len(fields) > 1 {
					reason = strings.Join(fields[1:], " ")
				}
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				if err := a.GoalService.Update(g.ID, fmt.Sprintf("%d", g.Revision), "blocked", "", 0, reason); err != nil {
					return fmt.Sprintf("✗ 标记阻塞失败：%v", err), nil
				}
				return fmt.Sprintf("✓ 目标已标记为阻塞。原因：%s", reason), nil
			case "gate", "门禁":
				// /goal gate add <command> 或 /goal gate remove
				if len(fields) < 2 {
					return "用法: /goal gate add <命令>  或  /goal gate remove", nil
				}
				sub := fields[1]
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				if sub == "add" {
					cmd := strings.Join(fields[2:], " ")
					if err := a.GoalService.AddGate(cmd, 0, 0); err != nil {
						return fmt.Sprintf("✗ 添加门禁失败：%v", err), nil
					}
					return fmt.Sprintf("✓ 已添门禁: %s", cmd), nil
				} else if sub == "remove" {
					if err := a.GoalService.RemoveGate(); err != nil {
						return fmt.Sprintf("✗ 移除门禁失败：%v", err), nil
					}
					return "✓ 已移除最后一条门禁。", nil
				}
				return "用法: /goal gate add <命令>  或  /goal gate remove", nil
			case "subgoal", "子目标":
				if len(fields) < 2 {
					return "用法: /goal subgoal add <描述>  或  /goal subgoal clear", nil
				}
				g := a.GoalService.Get()
				if g == nil {
					return "✗ 没有活跃目标。", nil
				}
				sub := fields[1]
				if sub == "add" {
					text := strings.Join(fields[2:], " ")
					if err := a.GoalService.AddSubgoal(text); err != nil {
						return fmt.Sprintf("✗ 添加子目标失败：%v", err), nil
					}
					return fmt.Sprintf("✓ 已添加子目标: %s", text), nil
				} else if sub == "clear" {
					if err := a.GoalService.ClearSubgoals(); err != nil {
						return fmt.Sprintf("✗ 清除子目标失败：%v", err), nil
					}
					return "✓ 已清除所有子目标。", nil
				}
				return "用法: /goal subgoal add <描述>  或  /goal subgoal clear", nil
			default:
				// 当作新目标 objective 处理
				objective := arg
				// 解析可选的 max_goal_rounds：/goal <目标> --rounds 10
				maxRounds := 0
				for i, f := range fields {
					if f == "--rounds" && i+1 < len(fields) {
						var n int
						fmt.Sscanf(fields[i+1], "%d", &n)
						if n > 0 {
							maxRounds = n
						}
					}
				}
				if err := a.GoalService.Create(objective, maxRounds); err != nil {
					return fmt.Sprintf("✗ 创建目标失败：%v\n提示：创建目标需要当前回合有直接的人类消息（不要通过工具调用触发）。", err), nil
				}
				g := a.GoalService.Get()
				msg := fmt.Sprintf("✓ 目标已创建\n  ID：%s\n  目标：%s\n  状态：待处理", g.ID, g.Objective)
				if maxRounds > 0 {
					msg += fmt.Sprintf("\n  轮次上限：%d", maxRounds)
				}
				return msg, nil
			}
		},
	})
	// 内置 /todos 命令：查看待办清单（pi 式 todo 工具的状态展示）
	a.Commands.Register(agent.Command{
		Name:        "todos",
		Description: "查看当前待办清单（todo 工具创建的待办）",
		Run: func(args string) (string, error) {
			return builtins.RenderPiTodos(), nil
		},
	})
	// 内置 /subagents 命令：查看子任务派发记录与状态（delegate_task 派发的子代理）
	a.Commands.Register(agent.Command{
		Name:        "subagents",
		Description: "查看子任务列表与状态（delegate_task 派发的子代理，含运行中/已完成/失败与结果摘要）",
		Run: func(args string) (string, error) {
			if a.SubAgents == nil {
				return "子代理功能未启用", nil
			}
			return a.SubAgents.RenderSubagentsForHuman(), nil
		},
	})
	// 内置 /stop 命令：立即停止当前任务（TUI 与 WebUI 通用；WebUI 在前端拦截，不进入这里）
	a.Commands.Register(agent.Command{
		Name:        "stop",
		Description: "立即停止当前任务（等同 ESC 取消，但无需确认；同时丢弃排队消息）。别名 /abort",
		Run: func(args string) (string, error) {
			// TUI 下由 tuiStopTask 在事件循环里打印提示（含丢弃排队条数），
			// 这里返回空串避免重复打印。
			if tuiStopTask != nil {
				tuiStopTask()
				return "", nil
			}
			// 非 TUI（经典交互 / server）：直接中止循环
			if !a.Running() {
				return "当前没有正在运行的任务", nil
			}
			a.Abort()
			a.ClearSteer()
			return "⏹ 已请求停止当前任务（当前步骤结束后退出）", nil
		},
	})
	// /abort 别名
	a.Commands.Register(agent.Command{
		Name:        "abort",
		Description: "同 /stop：立即停止当前任务",
		Run: func(args string) (string, error) {
			if tuiStopTask != nil {
				tuiStopTask()
				return "", nil
			}
			if !a.Running() {
				return "当前没有正在运行的任务", nil
			}
			a.Abort()
			a.ClearSteer()
			return "⏹ 已请求停止当前任务（当前步骤结束后退出）", nil
		},
	})
	// 内置 /quit 命令：退出交互模式
	// 注意：真正退出由 TUI/interactive 的拦截器负责（先打印续接提示再退出），
	// 此命令只作为文档/帮助入口与兜底返回值，不再 os.Exit（否则续接提示永不显示）。
	a.Commands.Register(agent.Command{
		Name:        "quit",
		Description: "退出交互模式",
		Run: func(args string) (string, error) {
			return "再见", nil
		},
	})
	// 内置 /color 命令：查看/设置聊天颜色（/color 查看 ｜ /color user <颜色> ｜ /color ai <颜色>）
	a.Commands.Register(agent.Command{
		Name:        "color",
		Description: "查看/设置聊天颜色（/color 查看 ｜ /color user <颜色> ｜ /color ai <颜色> ｜ /color reset 恢复默认）",
		Run: func(args string) (string, error) {
			cfg, _ := config.Load(config.DefaultPath())
			userColor := cfg.UserColor
			aiColor := cfg.AiColor
			if userColor == "" {
				userColor = "white"
			}
			if aiColor == "" {
				aiColor = "green"
			}
			args = strings.TrimSpace(args)
			if args == "" {
				return fmt.Sprintf(`当前颜色设置:
  用户消息颜色: %s
  AI 回复颜色: %s

可用颜色: black, red, green, yellow, blue, magenta, cyan, white, grey, default

示例:
  /color user blue    设置用户消息为蓝色
  /color ai cyan      设置 AI 回复为青色
  /color reset        恢复默认颜色（用户 white, AI green）`, userColor, aiColor), nil
			}
			fields := strings.Fields(args)
			if len(fields) < 2 {
				return "", fmt.Errorf("用法: /color <user|ai|reset> [<颜色>]\n示例: /color user blue  或 /color reset")
			}
			target := fields[0]
			color := ""
			if len(fields) > 1 {
				color = fields[1]
			}
			// 验证颜色值
			validColors := map[string]bool{
				"black": true, "red": true, "green": true, "yellow": true,
				"blue": true, "magenta": true, "cyan": true, "white": true,
				"grey": true, "default": true,
			}
			if color != "" && !validColors[color] {
				return "", fmt.Errorf("无效颜色 %q，可用: black, red, green, yellow, blue, magenta, cyan, white, grey, default", color)
			}
			switch target {
			case "user":
				if color == "" {
					return "", fmt.Errorf("用法: /color user <颜色>，示例: /color user blue")
				}
				cfg.UserColor = color
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %v", err)
				}
				return fmt.Sprintf("✓ 用户消息颜色已设置为: %s（下次启动生效，当前会话需重启 TUI）", color), nil
			case "ai":
				if color == "" {
					return "", fmt.Errorf("用法: /color ai <颜色>，示例: /color ai cyan")
				}
				cfg.AiColor = color
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %v", err)
				}
				return fmt.Sprintf("✓ AI 回复颜色已设置为: %s（下次启动生效，当前会话需重启 TUI）", color), nil
			case "reset":
				cfg.UserColor = ""
				cfg.AiColor = ""
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %v", err)
				}
				return "✓ 颜色已恢复默认（用户 white, AI green）", nil
			default:
				return "", fmt.Errorf("未知目标 %q，支持: user, ai, reset", target)
			}
		},
	})
	// 内置 /think 命令：查看/切换思考等级（auto/off/low/medium/high）
	a.Commands.Register(agent.Command{
		Name:        "think",
		Description: "查看/切换思考等级（/think 查看 ｜ /think auto/off/low/medium/high 设置 ｜ /think cycle 循环切换）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				return fmt.Sprintf("当前思考等级: %s\n等级说明:\n  auto   — 不传 thinking 参数，由模型自行决定\n  off    — 关闭思考\n  low    — 低强度思考（最快，推理 effort=low）\n  medium — 中等强度思考（默认推荐）\n  high   — 高强度思考（推理 effort=high）\n  xhigh  — 超高强度思考（推理 effort=xhigh，需网关支持）\n  max    — 最大强度思考（推理 effort=max，需网关支持）", client.ThinkingEnabled()), nil
			}
			supported := []string{"auto", "off", "low", "medium", "high", "xhigh", "max", "cycle"}
			for _, s := range supported {
				if args == s {
					switch s {
					case "cycle":
						newLevel := client.ToggleThinking()
						// 持久化
						cfg, _ := config.Load(config.DefaultPath())
						cfg.ThinkingLevel = newLevel
						if err := config.Save(config.DefaultPath(), cfg); err != nil {
							log.Printf("/think 持久化失败: %v", err)
						}
						return fmt.Sprintf("✓ 思考等级已切换: %s", newLevel), nil
					default:
						client.SetThinking(s)
						cfg, _ := config.Load(config.DefaultPath())
						cfg.ThinkingLevel = s
						if err := config.Save(config.DefaultPath(), cfg); err != nil {
							log.Printf("/think 持久化失败: %v", err)
						}
						return fmt.Sprintf("✓ 思考等级已设置为: %s", s), nil
					}
				}
			}
			return "", fmt.Errorf("未知参数 %q，支持: auto/off/low/medium/high/xhigh/max/cycle", args)
		},
	})
	// 内置 /help 命令：列出所有可用命令（纯文本格式，TUI/readline 通用）
	a.Commands.Register(agent.Command{
		Name:        "help",
		Description: "列出所有可用命令",
		Run: func(args string) (string, error) {
			cmds := a.Commands.List()
			var sb strings.Builder
			sb.WriteString("可用命令：\n")
			for _, c := range cmds {
				sb.WriteString(fmt.Sprintf("  /%-12s %s", c.Name, c.Description))
				if c.PluginFile != "" {
					sb.WriteString("（插件：" + c.PluginFile + "）")
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		},
	})
	// 内置 /config 命令：查看/修改运行配置（对齐 pi 的 /config）
	a.Commands.Register(agent.Command{
		Name:        "checkupdate",
		Description: "检查是否有新版本（/checkupdate，只提醒不自动更新）",
		Timeout:     45 * time.Second,
		Run: func(args string) (string, error) {
			info, err := fetchSiteInfo(updateSiteURL, 20*time.Second)
			return formatUpdateReport(version, info, err), nil
		},
	})
	a.Commands.Register(agent.Command{
		Name:        "chat",
		Description: "裸聊：不注入任何提示词直接问模型（/chat <内容>，不带会话历史、不落盘）",
		Timeout:     10 * time.Minute,
		Run: func(args string) (string, error) {
			q := strings.TrimSpace(args)
			if q == "" {
				return "用法: /chat <内容>\n\n不注入系统提示词/工具/技能/会话历史，只把这句话直接发给当前模型（沿用当前思考等级）。\n适合临时问答、模型自检、对照「无提示词」下的原始回答。", nil
			}
			reply, err := client.AIChat(llm.AIChatRequest{
				Messages: []llm.AIChatMsg{{Role: "user", Content: q}},
			})
			if err != nil {
				return "", fmt.Errorf("裸聊请求失败: %w", err)
			}
			return strings.TrimSpace(reply), nil
		},
	})
	// 内置 /config 命令：查看/修改运行配置
	a.Commands.Register(agent.Command{
		Name:        "config",
		Description: "查看/修改运行配置（/config 查看 ｜ /config max_steps 30 ｜ /config user_agent MyApp/1.0）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			cfg, _ := config.Load(config.DefaultPath())
			if args == "" {
				// 查看当前生效配置（内存中的 a.MaxSteps 优先，因为可能被 Run 兜底改写）
				ms := a.MaxSteps
				msStr := fmt.Sprintf("%d", ms)
				if ms < 0 {
					msStr = "-1 (无限)"
				}
				// UA 以内存中的客户端为准（可能刚被 /config user_agent 改写）
				ua := client.UserAgent()
				if ua == "" {
					ua = "(默认 Go-http-client)"
				}
				return fmt.Sprintf(`当前运行配置:
  最大步数 max_steps: %s
  上下文窗口 context_window: %d (token, 配置:%d)
  思考等级 thinking_level: %s
  模型 model: %s
  供应商 base_url: %s
  自定义UA user_agent: %s
可修改项: max_steps ｜ context_window ｜ user_agent ｜ 例: /config max_steps 30 /config context_window 128000 /config user_agent MyApp/1.0`, msStr, *ctxWindow, cfg.ContextWindow, cfg.ThinkingStr(), client.Model, client.BaseURL, ua), nil
			}
			fields := strings.Fields(args)
			if len(fields) == 2 && fields[0] == "max_steps" {
				n, err := strconv.Atoi(fields[1])
				if err != nil || (n < 1 && n != -1) || n > 1000 {
					return "", fmt.Errorf("max_steps 必须是 -1(无限) 或 1-1000 的整数")
				}
				a.MaxSteps = n
				cfg.MaxSteps = n
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("持久化失败: %v", err)
				}
				return fmt.Sprintf("✓ 最大步数已设置为 %d，已持久化到 %s", n, config.DefaultPath()), nil
			}
			if len(fields) == 2 && (fields[0] == "context_window" || fields[0] == "窗口") {
				n, err := strconv.Atoi(fields[1])
				if err != nil || n < 4096 || n > 10_000_000 {
					return "", fmt.Errorf("context_window 必须是 4096-10000000 的整数")
				}
				cfg.ContextWindow = n
				*ctxWindow = n
				if a.Compactor != nil {
					a.Compactor.ContextWindow = n
					// 同步小窗口兜底（与启动逻辑一致）：reserve/keep 不超过窗口合理比例
					if a.Compactor.ReserveTokens > n/5 {
						a.Compactor.ReserveTokens = n / 5
					}
					if a.Compactor.KeepRecentTokens > n/3 {
						a.Compactor.KeepRecentTokens = n / 3
					}
					// 下限兜底（同启动逻辑）：keepRecent 过小会让压缩后仍超阈值，反复触发
					if a.Compactor.KeepRecentTokens < 500 {
						a.Compactor.KeepRecentTokens = 500
					}
				}
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("持久化失败: %v", err)
				}
				ratio := ""
				if a.Compactor != nil {
					ratio = fmt.Sprintf("\n  压缩阈值: %d (窗口-reserve %d, keepRecent %d)",
						a.Compactor.ContextWindow-a.Compactor.ReserveTokens,
						a.Compactor.ReserveTokens, a.Compactor.KeepRecentTokens)
				}
				return fmt.Sprintf("✓ 上下文窗口已设置为 %d，已持久化到 %s%s", n, config.DefaultPath(), ratio), nil
			}
			// /config user_agent <字符串>：自定义 User-Agent（支持含空格，取键后的整段）
			// /config user_agent -  或 reset + 空值 = 清除（恢复 Go 默认）
			if len(fields) >= 1 && (fields[0] == "user_agent" || fields[0] == "ua") {
				ua := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
				if ua == "-" || ua == "reset" || ua == "default" {
					ua = ""
				}
				if strings.ContainsAny(ua, "\r\n") {
					return "", fmt.Errorf("user_agent 不能包含换行符")
				}
				client.SetUserAgent(ua)
				cfg.UserAgent = ua
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("持久化失败: %v", err)
				}
				if ua == "" {
					return fmt.Sprintf("✓ 自定义 UA 已清除，恢复默认（Go-http-client），已持久化到 %s", config.DefaultPath()), nil
				}
				return fmt.Sprintf("✓ 自定义 UA 已设置为 %q，已立即生效并持久化到 %s", ua, config.DefaultPath()), nil
			}
			return "", fmt.Errorf("用法: /config 查看 ｜ /config max_steps 30 ｜ /config context_window 128000 ｜ /config user_agent MyApp/1.0（清除用 /config user_agent -）")
		},
	})
	// 内置 /permission 命令：查看/切换权限门禁（GAP-S2，内存即时生效 + config 持久化）
	a.Commands.Register(agent.Command{
		Name:        "permission",
		Description: "查看/切换权限门禁（/permission 查看 ｜ /permission auto|default|acceptEdits|plan ｜ /permission reset 清审批缓存）。注意：plan 权限模式 ≠ /plan 计划模式",
		Run: func(args string) (string, error) {
			if a.Permission == nil {
				return "权限门禁未启用", nil
			}
			args = strings.TrimSpace(args)
			if args == "" {
				mode := a.Permission.Mode()
				hint := ""
				switch mode {
				case agent.PermAuto:
					hint = "全部工具放行（现行为，出厂默认）"
				case agent.PermDefault:
					hint = "只读工具放行；写类（edit/write 等）与 bash 危险命令执行前弹确认"
				case agent.PermAcceptEdits:
					hint = "写类放行；仅 bash 危险命令（rm -rf / git push --force 等）执行前弹确认"
				case agent.PermPlan:
					hint = "只读放行；写类与危险命令直接拒绝（不询问）。注意：这是权限模式，与 /plan 计划模式是两回事"
				}
				return fmt.Sprintf(`权限门禁: %s
  %s
  已批准「本会话允许」: %d 条（/permission reset 清空）
切换: /permission auto|default|acceptEdits|plan（立即生效并持久化到 config.json）`, mode, hint, a.Permission.ApprovalCount()), nil
			}
			if args == "reset" {
				n := a.Permission.ResetApprovals()
				return fmt.Sprintf("✓ 已清空 %d 条「本会话允许」审批记录", n), nil
			}
			if m, ok := agent.ParsePermissionMode(args); ok {
				a.Permission.SetMode(m)
				cfg, _ := config.Load(config.DefaultPath())
				cfg.PermissionMode = string(m)
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return fmt.Sprintf("✓ 模式已切换为 %s（内存生效），但持久化失败: %v", m, err), nil
				}
				return fmt.Sprintf("✓ 权限模式已切换为 %s（立即生效，主代理与子代理共用同一门禁；已持久化到 %s）", m, config.DefaultPath()), nil
			}
			return "", fmt.Errorf("用法: /permission 查看 ｜ /permission auto|default|acceptEdits|plan ｜ /permission reset")
		},
	})
	// 内置 /hooks 命令：查看用户级 hooks 配置（C-6，只读展示 + 用法提示）
	a.Commands.Register(agent.Command{
		Name:        "hooks",
		Description: "查看用户级 hooks（config.json hooks 段，shell 命令响应工具事件；修改 config 后重启生效）",
		Run: func(args string) (string, error) {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return "", fmt.Errorf("读取配置失败: %v", err)
			}
			if cfg.Hooks.Empty() {
				return `（未配置用户级 hooks）
在 config.json（` + config.DefaultPath() + `）加 hooks 段，用 shell 命令响应工具事件：
  "hooks": {
    "pre_tool_use":  [{"command": "检查脚本.sh", "timeout_ms": 10000}],
    "post_tool_use": [{"command": "审计脚本.sh"}]
  }
上下文经 stdin 传 JSON（tool/args/session/cwd；另注入 MIZAR_HOOK=1 环境变量）。
pre_tool_use 裁决：stdout 输出 {"decision":"deny","reason":"..."} 或 {"decision":"rewrite","tool":"...","args":"..."}；
exit 2 = 拒绝（stderr 作为原因）；其它非零/超时 = 记日志放行，不阻塞主流程。`, nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "用户级 hooks（%s）：\n", config.DefaultPath())
			for _, h := range cfg.Hooks.PreToolUse {
				fmt.Fprintf(&sb, "  pre_tool_use:  %s（超时 %dms）\n", h.Command, h.TimeoutMs)
			}
			for _, h := range cfg.Hooks.PostToolUse {
				fmt.Fprintf(&sb, "  post_tool_use: %s（超时 %dms）\n", h.Command, h.TimeoutMs)
			}
			sb.WriteString("\n裁决: stdout JSON {\"decision\":\"deny|rewrite\",\"reason\":...} 或 exit 2；失败放行不阻塞主流程")
			return sb.String(), nil
		},
	})
	// 内置 /learn 命令：学习闭环可视化（H-7）——技能图谱 + 反思。
	a.Commands.Register(agent.Command{
		Name: "learn",
		Description: "学习闭环：/learn graph 看技能与记忆的关联图谱 ｜ /learn reflect 反思哪些技能是孤岛/该沉淀。" +
			"（技能图谱收录「被使用过或 agent 创建」的技能 + 全部结构化记忆）",
		Run: func(args string) (string, error) {
			sub := strings.TrimSpace(args)
			// 重新加载技能目录（模型/用户可能刚用 skill_manage 写了新技能，
			// 内存里的 skm 不会自动刷新——图谱要看最新状态必须重扫）。
			skm.LoadAll()
			// 组装当前图谱
			memMap, _ := memStore.List("")
			gr := learninggraph.Build(skm.All(), stats.UsesSnapshot(), memMap)
			switch {
			case sub == "" || sub == "graph" || sub == "图谱":
				return reflect.RenderGraph(gr), nil
			case sub == "reflect" || sub == "反思":
				// 统计最近工具使用（从 stats 里拿不到工具计数，用 u 的 Pending 无法——
				// 这里以技能使用次数为代理，工具计数缺省为空，仍能覆盖技能/记忆维度）
				toolUsageMu.Lock()
				tu := make(map[string]int, len(toolUsage))
				for k, v := range toolUsage {
					tu[k] = v
				}
				toolUsageMu.Unlock()
				act := reflect.Activity{Graph: gr, SkillCounts: stats.UsesSnapshot(), ToolCounts: tu}
				ins := reflect.Heuristic(act)
				return reflect.Render(ins), nil
			case sub == "json":
				b, err := json.MarshalIndent(gr, "", "  ")
				if err != nil {
					return "", err
				}
				return string(b), nil
			default:
				return "", fmt.Errorf("用法: /learn graph ｜ /learn reflect ｜ /learn json")
			}
		},
	})
	// 内置 /search 命令：跨会话全文搜索（H-1，session_search 工具的斜杠入口）
	a.Commands.Register(agent.Command{
		Name:        "search",
		Description: "跨会话全文搜索（/search <关键词> ｜ /search 列出已索引会话）。会话内容按 FTS5 增量索引，中文/英文可搜",
		Run: func(args string) (string, error) {
			if ssIdx == nil {
				return "", fmt.Errorf("会话搜索索引不可用（见启动日志）")
			}
			q := strings.TrimSpace(args)
			if _, err := ssIdx.Sync(sessAbs); err != nil {
				return "", fmt.Errorf("索引同步失败: %v", err)
			}
			if q == "" {
				list, err := ssIdx.Sessions(30)
				if err != nil {
					return "", err
				}
				if len(list) == 0 {
					return "（尚无已索引会话）", nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "已索引 %d 个会话（/search <关键词> 检索）：\n", len(list))
				for _, si := range list {
					scope := si.Scope
					if scope == "" {
						scope = "(平铺)"
					}
					short := si.SessionID
					if len(short) > 8 {
						short = short[:8]
					}
					fmt.Fprintf(&sb, "  %s  %s  %d 条  %s\n", short, scope, si.Messages, si.LastTs)
				}
				return strings.TrimSpace(sb.String()), nil
			}
			hits, err := ssIdx.Search(q, 20)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return fmt.Sprintf("未找到与「%s」相关的历史消息", q), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "找到 %d 条命中：\n", len(hits))
			for _, h := range hits {
				role := h.Role
				if h.Tool != "" {
					role = "tool:" + h.Tool
				}
				short := h.SessionID
				if len(short) > 8 {
					short = short[:8]
				}
				fmt.Fprintf(&sb, "  [%s#%d] (%s) %s\n", short, h.Seq, role, strings.TrimSpace(h.Snippet))
			}
			return strings.TrimSpace(sb.String()), nil
		},
	})
	// 内置 /export 命令：把当前会话导出为自包含 HTML（P-6）
	a.Commands.Register(agent.Command{
		Name:        "export",
		Description: "把当前会话导出为自包含 HTML（/export [输出路径]，默认 ~/mizar-session-<id>.html）",
		Run: func(args string) (string, error) {
			out := strings.TrimSpace(args)
			sid := *sessionID
			if sid == "" {
				return "", fmt.Errorf("当前无会话（尚未产生消息）")
			}
			msgs, err := st.Load(sid)
			if err != nil {
				return "", fmt.Errorf("读取会话失败: %v", err)
			}
			title := "Mizar 会话"
			if titleCache != nil {
				if t := titleCache.Get(sid); t != "" {
					title = t
				}
			}
			html := exporter.ToHTML(msgs, exporter.Options{Title: title, SessionID: sid})
			if out == "" {
				out = "~/mizar-session-" + shortSID(sid) + ".html"
			}
			if strings.HasPrefix(out, "~/") {
				if home, herr := os.UserHomeDir(); herr == nil {
					out = filepath.Join(home, strings.TrimPrefix(out, "~/"))
				}
			}
			if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
				return "", fmt.Errorf("写入失败: %v", err)
			}
			return fmt.Sprintf("✓ 已导出 %d 条消息到 %s", len(msgs), out), nil
		},
	})
	// 内置 /trust 命令：查看/管理项目信任记录（P-5 简化版）
	a.Commands.Register(agent.Command{
		Name:        "trust",
		Description: "查看/管理项目信任记录（/trust 查看 ｜ /trust reset 重问当前项目 ｜ /trust path <路径> 查看指定项目）",
		Run: func(args string) (string, error) {
			fields := strings.Fields(strings.TrimSpace(args))
			sub := ""
			if len(fields) > 0 {
				sub = strings.ToLower(fields[0])
			}
			switch sub {
			case "", "list", "show":
				return trust.Describe(trustStore), nil
			case "reset":
				target := wd
				if len(fields) > 1 {
					target = fields[1]
				}
				if err := trustStore.Reset(target); err != nil {
					return "", err
				}
				return fmt.Sprintf("✓ 已清除 %s 的信任记录，下次启动重新询问", target), nil
			default:
				return "", fmt.Errorf("用法: /trust ｜ /trust reset [路径]")
			}
		},
	})
	// 内置 /memory 命令：查看/管理结构化长期记忆（C-7，~/.mizar/memory/MEMORY.md）
	a.Commands.Register(agent.Command{
		Name: "memory",
		Description: "查看/管理结构化长期记忆（/memory 查看 ｜ /memory add <类别> <内容> ｜ " +
			"/memory rm <类别> <序号> ｜ /memory clear [类别]）。类别: user/feedback/project/reference",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			fields := strings.Fields(args)
			sub := ""
			if len(fields) > 0 {
				sub = strings.ToLower(fields[0])
			}
			switch sub {
			case "", "list", "show":
				m, err := memStore.List("")
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "结构化记忆（%s）：\n", memStore.Path())
				total := 0
				for _, cat := range memory.Categories() {
					items := m[cat]
					if len(items) == 0 {
						continue
					}
					fmt.Fprintf(&sb, "\n[%s]\n", cat)
					for i, it := range items {
						fmt.Fprintf(&sb, "  %d. %s\n", i+1, it)
						total++
					}
				}
				if total == 0 {
					return "（结构化记忆为空）\n用 /memory add <类别> <内容> 或让模型调用 memory 工具记录；" +
						"\n类别: user/feedback/project/reference。每次会话自动注入系统提示。", nil
				}
				sb.WriteString("\n（修改后用 /memory 核对；模型也可通过 memory 工具读写）")
				return sb.String(), nil
			case "add":
				if len(fields) < 3 {
					return "", fmt.Errorf("用法: /memory add <类别> <内容>（类别: user/feedback/project/reference）")
				}
				cat := memory.NormalizeCategory(fields[1])
				text := strings.TrimSpace(strings.TrimPrefix(args, fields[0]+" "+fields[1]))
				if err := memStore.Add(cat, text); err != nil {
					return "", err
				}
				a.SetMemoryPrompt(memStore.Render())
				return fmt.Sprintf("✓ 已记入 %s 分类（当轮起注入系统提示）", cat), nil
			case "rm", "remove", "delete":
				if len(fields) < 3 {
					return "", fmt.Errorf("用法: /memory rm <类别> <序号>（序号取自 /memory 列表）")
				}
				idx, err := strconv.Atoi(fields[2])
				if err != nil {
					return "", fmt.Errorf("序号必须是数字: %q", fields[2])
				}
				ok, err := memStore.Remove(fields[1], idx)
				if err != nil {
					return "", err
				}
				if !ok {
					return "", fmt.Errorf("删除失败")
				}
				a.SetMemoryPrompt(memStore.Render())
				return "✓ 已删除", nil
			case "clear":
				target := ""
				if len(fields) > 1 {
					target = fields[1]
				}
				if err := memStore.Clear(target); err != nil {
					return "", err
				}
				a.SetMemoryPrompt(memStore.Render())
				if target == "" {
					return "✓ 已清空全部结构化记忆", nil
				}
				return fmt.Sprintf("✓ 已清空 %s 分类", memory.NormalizeCategory(target)), nil
			default:
				return "", fmt.Errorf("用法: /memory ｜ /memory add <类别> <内容> ｜ /memory rm <类别> <序号> ｜ /memory clear [类别]")
			}
		},
	})
	// 内置 /compact 命令：手动触发上下文压缩
	a.Commands.Register(agent.Command{
		Name:        "compact",
		Description: "压缩上下文（/compact 或 /compact <摘要指引>）",
		Timeout:     10 * time.Minute,
		Run: func(args string) (string, error) {
			if a.Compactor == nil {
				return "✗ 会话压缩未启用（启动时使用了 --no-compact）", nil
			}
			msgs := a.Initial
			if len(msgs) <= 1 {
				return "✗ 消息不足，无需压缩", nil
			}
			est := agent.EstimateMessages(msgs)
			var sb strings.Builder
			fmt.Fprintf(&sb, "当前估算: %d tokens（%d 条消息）\n", est, len(msgs))
			// 手动压缩：无条件执行。用户主动敲 /compact 即表示需要压缩，
			// 不再判断阈值、不需要 force 关键字。参数（若有）一律视为摘要指引。
			customPrompt := strings.TrimSpace(args)
			// 用自定义指令替换 Summarize 以生成结构化摘要
			origSummarize := a.Compactor.Summarize
			customUsed := false
			if customPrompt != "" {
				a.Compactor.Summarize = func(ms []agent.Message) (string, error) {
					base, err := origSummarize(ms)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("%s\n\n额外要求：%s", base, customPrompt), nil
				}
				customUsed = true
			}
			// 强制压缩：保留最近约 KeepRecentTokens；估算不足时按条数折半兜底。
			newMsgs, err := a.Compactor.CompactForce(msgs, a.Compactor.KeepRecentTokens)
			if customUsed {
				a.Compactor.Summarize = origSummarize // 恢复
			}
			if err != nil {
				return "", fmt.Errorf("压缩失败: %v", err)
			}
			// 检查是否实际切分（新列表包含 KindSummary 消息则已切分）
			cutOccurred := false
			for _, m := range newMsgs {
				if m.Kind == agent.KindSummary {
					cutOccurred = true
					break
				}
			}
			if !cutOccurred {
				return sb.String() + "  无需压缩（消息结构不允许切分）", nil
			}
			a.Initial = newMsgs
			newEst := agent.EstimateMessages(newMsgs)
			reduced := est - newEst
			fmt.Fprintf(&sb, "✓ 已压缩: %d → %d tokens（节省 %d tokens, %.1f%%）\n", est, newEst, reduced, float64(reduced)/float64(est)*100)
			return sb.String(), nil
		},
	})
	// 内置 /save 命令：将当前配置持久化到 ~/.mizar/config.json
	a.Commands.Register(agent.Command{
		Name:        "save",
		Description: "将当前配置保存到 ~/.mizar/config.json",
		Run: func(args string) (string, error) {
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
			cfg.UserAgent = client.UserAgent()
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			return fmt.Sprintf("✓ 配置已保存: %s", config.DefaultPath()), nil
		},
	})
	// 内置 /mcp 命令：查看/管理 MCP server
	a.Commands.Register(agent.Command{
		Name:        "mcp",
		Description: "MCP server 管理（/mcp list 列出现状 ｜ /mcp add <name> <url|command> [args...] ｜ /mcp remove <name> ｜ /mcp test <name> ｜ /mcp reload）",
		Run: func(args string) (string, error) {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return "", fmt.Errorf("读取配置失败: %v", err)
			}
			args = strings.TrimSpace(args)
			if args == "" {
				return mcpListText(cfg), nil
			}
			fields := strings.Fields(args)
			cmd := fields[0]
			switch cmd {
			case "list", "ls", "status":
				return mcpListText(cfg), nil
			case "add":
				if len(fields) < 3 {
					return "", fmt.Errorf("用法: /mcp add <name> <url|command> [args...]\n示例:\n  /mcp add myserver http://127.0.0.1:3001/mcp\n  /mcp add sqlite npx -y @modelcontextprotocol/server-sqlite")
				}
				name := fields[1]
				addr := fields[2]
				rest := fields[3:]
				// 判断是 URL 还是 command
				var serverConf engine.MCPServerConf
				serverConf.Name = name
				if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
					serverConf.URL = addr
				} else {
					serverConf.Command = addr
					serverConf.Args = rest
				}
				// 检查是否已存在
				found := false
				for i := range cfg.MCPServers {
					if cfg.MCPServers[i].Name == name {
						cfg.MCPServers[i] = serverConf
						found = true
						break
					}
				}
				if !found {
					cfg.MCPServers = append(cfg.MCPServers, serverConf)
				}
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %v", err)
				}
				return fmt.Sprintf("✓ MCP server %q 已添加/更新", name), nil

			case "remove":
				if len(fields) < 2 {
					return "", fmt.Errorf("用法: /mcp remove <name>")
				}
				name := fields[1]
				var remain []engine.MCPServerConf
				removed := false
				for _, s := range cfg.MCPServers {
					if s.Name == name {
						removed = true
					} else {
						remain = append(remain, s)
					}
				}
				if !removed {
					return "", fmt.Errorf("未找到 server %q（可用: %s）", name, mcpNames(cfg.MCPServers))
				}
				cfg.MCPServers = remain
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("保存失败: %v", err)
				}
				return fmt.Sprintf("✓ 已移除 MCP server %q", name), nil

			case "test":
				if len(fields) < 2 {
					return "", fmt.Errorf("用法: /mcp test <name>")
				}
				name := fields[1]
				var found *engine.MCPServerConf
				for i := range cfg.MCPServers {
					if cfg.MCPServers[i].Name == name {
						found = &cfg.MCPServers[i]
						break
					}
				}
				if found == nil {
					return "", fmt.Errorf("未找到 server %q（可用: %s）", name, mcpNames(cfg.MCPServers))
				}
				timeout := 30 * time.Second
				if found.Timeout > 0 {
					timeout = time.Duration(found.Timeout) * time.Second
				}
				_, cancel := ctxpkg.WithTimeout(ctxpkg.Background(), timeout)
				defer cancel()
				reg := engine.NewMCPRegistry([]engine.MCPServerConf{*found})
				defer reg.Close()
				fn := reg.CallFn()
				// 尝试列出工具（通过调用一个特殊工具名触发）
				// mcp_call 本身不暴露 list，我们直接用 registry 测试初始化
				result, err := fn(name, "__ping__", "{}")
				if err != nil {
					// __ping__ 不存在是正常的，只要连接成功就行
					if strings.Contains(err.Error(), "tool not found") || strings.Contains(err.Error(), "Unknown tool") || strings.Contains(err.Error(), "not found") {
						return fmt.Sprintf("✓ MCP server %q 连接正常（工具列表需通过 mcp_call 调用）", name), nil
					}
					return fmt.Sprintf("✗ MCP server %q 连接失败: %v", name, err), nil
				}
				return fmt.Sprintf("✓ MCP server %q 连接正常，响应: %s", name, result), nil

			case "reload":
				// 重新从配置文件加载，并真正重建运行时连接：
				// 关闭旧的 stdio 子进程，下次 mcp_call 按新配置重新握手。
				// 这是 Zig 二进制「热更新」的落地方式：覆盖可执行文件后执行本命令即可。
				newCfg, err := config.Load(config.DefaultPath())
				if err != nil {
					return "", fmt.Errorf("读取配置失败: %v", err)
				}
				cfg.MCPServers = newCfg.MCPServers
				mcpReg.Reload(newCfg.MCPServers)
				return fmt.Sprintf("✓ MCP 配置已重新加载并重建连接（%d 个 server: %s）", len(cfg.MCPServers), mcpNames(cfg.MCPServers)), nil

			default:
				return "", fmt.Errorf("未知子命令 %q，支持: list/add/remove/test/reload（输入 /mcp 查看帮助）", cmd)
			}
		},
	})
	if !*noCompact {
		client2 := client // SummarizeMessages 用同一客户端
		// 摘要输出预算：跟随模型 output limit（配置可选手填），未知时回退 1500。
		// 对齐 pi 1.4.0：固定小上限会让推理模型报 "generation hit the token cap"。
		modelMaxOutput := 0
		if cfg, err := config.Load(config.DefaultPath()); err == nil {
			modelMaxOutput = cfg.MaxOutput
		}
		summaryTokens := agent.EffectiveSummaryTokens(modelMaxOutput, 0)
		a.Compactor = agent.DefaultCompactor(func(msgs []agent.Message) (string, error) {
			return client2.SummarizeMessages(msgs, summaryTokens)
		})
		a.Compactor.MaxSummaryTokens = summaryTokens
		a.Compactor.ContextWindow = *ctxWindow
		// 小窗口兜底（参照 auto_offload 教训）：reserve/keep 不能超过窗口的合理比例，
		// 否则压缩恒触发或永不触发。
		if a.Compactor.ReserveTokens > *ctxWindow/5 {
			a.Compactor.ReserveTokens = *ctxWindow / 5
		}
		if a.Compactor.KeepRecentTokens > *ctxWindow/3 {
			a.Compactor.KeepRecentTokens = *ctxWindow / 3
		}
		if a.Compactor.KeepRecentTokens < 500 {
			a.Compactor.KeepRecentTokens = 500
		}
		log.Printf("会话压缩开启 (window=%d, reserve=%d, keep=%d)", a.Compactor.ContextWindow, a.Compactor.ReserveTokens, a.Compactor.KeepRecentTokens)
	}

	// 启动时打印缓存统计（首次调用 SystemPrompt() 后会在运行时更新）
	if a.CacheStats() != nil {
		log.Printf("%s", a.CacheStats().Summary())
	}

	if *sessionID != "" {
		msgs, err := st.Load(*sessionID)
		if err == nil && len(msgs) > 0 {
			// 恢复子任务句柄（GAP-S4）：从全量历史回放终态快照（LoadRaw 穿透压缩
			// 裁剪，快照不丢），再把标记消息从上下文剔除——只服务于状态重建，不进模型视野。
			if raw, rerr := st.LoadRaw(*sessionID); rerr == nil {
				a.SubAgents.RestoreFromMessages(raw)
			}
			msgs = agent.StripSubagentStateMessages(msgs)
			if len(msgs) > *history {
				msgs = msgs[len(msgs)-*history:]
			}
			a.Initial = msgs
			// 恢复 pi 式 todo 状态：回放会话历史中的 todo 工具结果（参照 pi reconstructState）
			builtins.SetTodosFromHistory(msgs)
			log.Printf("恢复会话 %s: %d 条历史", *sessionID, len(msgs))
		}
	}

	if *task != "" {
		// ask_user_question：stdin 是终端时提供文本选项作答；
		// 非终端（管道/CI）保持 nil → 工具走「无交互界面，请自行决策」降级提示。
		if isTerminal(os.Stdin) {
			stdinReader := bufio.NewReader(os.Stdin)
			askUser := func(questionsJSON string) (string, error) {
				return askUserText(questionsJSON, os.Stdout, func(p string) (string, error) {
					return readLineFromStdin(stdinReader, p)
				})
			}
			builtins.SetAskUser(askUser)
			agent.SetPlanAskUser(askUser)
		}
		// 实时输出：模型每步调工具前先说一句说明，再执行。让用户看到“为何执行这些命令”，便于判断是否打断。
		if a.Hooks != nil {
			a.Hooks.OnToolCall(func(ctx *agent.HookContext) error {
				if ctx.Reason != "" {
					fmt.Printf("🧠 %s\n", ctx.Reason)
				}
				fmt.Printf("🔧 %s(%s)\n", ctx.Tool, truncateArgs(ctx.Args))
				return nil
			})
			a.Hooks.OnToolResult(func(ctx *agent.HookContext) error {
				if ctx.Err != nil {
					fmt.Printf("❌ %s 失败: %v\n", ctx.Tool, ctx.Err)
				} else if ctx.Result != "" {
					res := builtins.StripTodoMarker(ctx.Result)
					res = strings.TrimSpace(res)
					if len(res) > 300 {
						res = res[:297] + "..."
					}
					fmt.Printf("✅ %s → %s\n", ctx.Tool, res)
				}
				return nil
			})
		}
		// 工具交换收集：任务结束落盘（与 TUI/interactive 一致）
		var exchanges []agent.Message
		a.OnToolExchange = func(tool, args, reason, out string, err error) {
			exchanges = append(exchanges, agent.ToolExchangeMessages(tool, args, reason, out, err)...)
		}
		// 压缩记录：追加式落盘（不改写历史），下次续接时重建为 summary + 保留范围。
		// 对齐 pi 的 CompactionEntry —— 否则压缩成果只活在内存，跨进程续接会重复压缩。
		if *sessionID != "" {
			a.OnCompact = func(summary string, dropped, tokensBefore int) {
				if err := st.AppendCompaction(*sessionID, summary, dropped, tokensBefore); err != nil {
					log.Printf("压缩记录落盘失败: %v", err)
					return
				}
				log.Printf("压缩记录已落盘: 摘要掉 %d 条消息（压缩前约 %d token）", dropped, tokensBefore)
			}
			// 自愈降级落盘：上下文溢出被应急裁剪后，把「保留起点」写进会话，
			// 否则用户退出再进来会重新加载全量历史、再撞一次 400。
			a.OnEmergencyTrim = func(pct, keptMsgs int) {
				// 保留起点 = 已落盘的消息数 - 本轮保留数（裁剪只发生在历史段内）。
				raw, err := st.LoadRaw(*sessionID)
				if err != nil {
					log.Printf("自愈记录落盘失败（读取会话）: %v", err)
					return
				}
				firstKept := len(raw) - keptMsgs
				if firstKept < 0 {
					firstKept = 0
				}
				if err := st.AppendEmergencyTrim(*sessionID, firstKept, 0, pct); err != nil {
					log.Printf("自愈记录落盘失败: %v", err)
					return
				}
				log.Printf("⚠ 自愈降级已落盘: 裁剪 %d%%，保留起点 %d（共 %d 条历史）", pct, firstKept, len(raw))
			}
		}
		reply, err := a.Run(*task)
		a.OnToolExchange = nil
		if err != nil {
			log.Fatalf("执行失败: %v", err)
		}
		fmt.Println("\n=== 最终回答 ===")
		fmt.Println(reply)
		if *sessionID != "" {
			st.Append(*sessionID, agent.Message{Role: agent.RoleUser, Content: *task})
			for _, msg := range exchanges {
				_ = st.Append(*sessionID, msg)
			}
			st.Append(*sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
			// 首次会话：用首条 user 消息推导标题
			if titleCache.Get(*sessionID) == "" {
				if t := session.DeriveTitle([]agent.Message{{Role: agent.RoleUser, Content: *task}}); t != "" {
					_ = titleCache.Set(*sessionID, t)
				}
			}
			log.Printf("会话 %s 已保存", *sessionID)
			_ = st.SetLast(*sessionID)
			short := *sessionID
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Printf("\n会话 %s: 用 mizar -session %s 续接（或 mizar -c 续接最后一次退出的会话）\n", short, *sessionID)
		}
		return
	}

	// Server 模式（持久运行 daemon）
	if *serve {
		cfg := server.NewConfig(*addr, *token)
		cfg.SetAll(*httpOn, *rpcOn, *wsOn)
		// Web UI 状态栏也显示构建标识（win10-native / win7-compat），便于现场分辨。
		cfg.Version = versionString()
		// Web UI 状态栏：MCP 连接统计（对齐 TUI 项目行）
		cfg.MCPStats = func() (int, int) {
			if runtimeMCPRegistry == nil {
				return 0, 0
			}
			return runtimeMCPRegistry.Stats()
		}
		// Web UI：启用 + 登录鉴权（密码优先取配置，空则随机生成并写回）
		webCfg := &config.WebConfig{}
		diskCfg, _ := config.Load(config.DefaultPath())
		if diskCfg != nil && diskCfg.Web != nil {
			webCfg = diskCfg.Web
		}
		genPwd := false
		if webCfg.Password == "" {
			webCfg.Password = randPassword(12)
			genPwd = true
		}
		if webCfg.Username == "" {
			webCfg.Username = "admin"
		}
		if genPwd {
			if diskCfg == nil {
				diskCfg = &config.Config{}
			}
			diskCfg.Web = webCfg
			if err := config.Save(config.DefaultPath(), diskCfg); err != nil {
				log.Printf("Web UI 密码写回配置失败: %v", err)
			} else {
				log.Printf("Web UI 首次启动：已生成登录密码并写入 %s", config.DefaultPath())
			}
		}
		cfg.Web = server.WebAuth{Enabled: true, Username: webCfg.Username, Password: webCfg.Password, FrontendDir: webCfg.FrontendDir}
		srv := server.New(cfg, a, st)
		// Web 问答通道：把 ask_user_question / exit_plan_mode 的阻塞回调
		// 接到 WebUI 弹窗（之前 Server 模式这两个工具只能降级为「请自行决策」）。
		// 不设超时——用户可能很久才回来选，题目过期比多等更糟。
		builtins.SetAskUser(srv.AskUser)
		// 权限门禁确认（GAP-S2）：走同一 WebUI 弹窗通路（askBroker 多槽支持并发）
		a.Permission.SetAskUser(srv.AskUser)
		agent.SetPlanAskUser(srv.AskUser)
		// 应用持久化的 Web UI 设置（模型/参数/思考等级）
		srv.ApplyStoredSettings()
		// 插件 ws_emit 能力：注入 Server 广播
		host.WSEmit = func(event, dataJSON string) { srv.EmitToWS(event, dataJSON) }
		// 子任务进度推送（GAP-S3 收尾）：delegate_task 派发的运行中子任务
		// 每步进度实时广播到 /ws（event=subagent_progress, data={id,line}）。
		// 不进回放日志（无 conversation_id，子代理管理器进程级共享、跨会话）；
		// 断线重连后由 get_subagent / /subagents 兜底补查终态。
		if a.SubAgents != nil {
			a.SubAgents.SetOnProgress(func(id, line string) {
				srv.EmitToWS("subagent_progress", fmt.Sprintf(`{"id":%q,"line":%q}`, id, line))
			})
		}
		// 强制重载使 ws_emit 对已加载插件生效
		pm.ReloadAll()
		log.Printf("Server 模式启动: %s (http=%v rpc=%v ws=%v)", *addr, *httpOn, *rpcOn, *wsOn)
		log.Printf("  OpenAI 兼容:   POST %s/v1/chat/completions", *addr)
		log.Printf("  JSON-RPC 2.0:   POST %s/rpc (agent.run/steer/abort)", *addr)
		log.Printf("  WebSocket:      %s/ws (实时事件 + 快速纠正)", *addr)
		log.Printf("  admin:          GET %s/admin/status  POST %s/admin/switch", *addr, *addr)
		log.Printf("  Web UI:         http://%s/web/  (登录用户: %s)", hostPort(*addr), webCfg.Username)
		// 先探测端口可用性，避免"正在运行"横幅后跟启动失败的尴尬
		probe, err := net.Listen("tcp", *addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n❌ Server 启动失败: %v\n", err)
			fmt.Fprintf(os.Stderr, "   端口被占用? 用 -addr 指定其他端口，例如: -addr :3005\n")
			fmt.Fprintf(os.Stderr, "   查看占用: ss -tlnp | grep %s\n", strings.TrimPrefix(*addr, ":"))
			os.Exit(1)
		}
		probe.Close()
		// 正常运行中横幅（Start 内部阻塞，必须打印在调用前）
		fmt.Printf("\n✅ Mizar Server 正在运行: %s\n", *addr)
		fmt.Printf("   OpenAI 兼容: POST %s/v1/chat/completions\n", *addr)
		fmt.Printf("   JSON-RPC:    POST %s/rpc   WebSocket: %s/ws\n", *addr, *addr)
		fmt.Printf("   Web UI:      http://%s/web/  (用户 %s)\n", hostPort(*addr), webCfg.Username)
		fmt.Printf("   Ctrl+C 停止服务\n\n")
		// 优雅退出（2026-11-15 审计修复）：Server.Shutdown 此前全仓零调用，
		// 也没有任何信号处理 → Ctrl+C 直接杀进程，会话/DB/插件服务器来不及收尾。
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			<-sigCh
			fmt.Printf("\n⏹ 收到退出信号，正在关闭服务…\n")
			ctx, cancel := ctxpkg.WithTimeout(ctxpkg.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "关闭异常: %v\n", err)
			}
			// 关掉会话搜索索引等资源（若已打开）
			if ssIdx != nil {
				_ = ssIdx.Close()
			}
			fmt.Printf("✓ 已退出\n")
			os.Exit(0)
		}()
		if err := srv.Start(); err != nil {
			// 主动 Shutdown（上面信号处理）会让 ListenAndServe 返回
			// http.ErrServerClosed —— 这是正常退出，不是异常。
			// 之前的实现把它当成错误打印「❌ Server 运行异常退出」并以 1 退出，
			// 在 systemd/supervisor 下会被误判为崩溃并触发无意义重启。
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			fmt.Fprintf(os.Stderr, "\n❌ Server 运行异常退出: %v\n", err)
			os.Exit(1)
		}
		// Start 正常情况不会走到这里（ListenAndServe 阻塞）；走到说明服务已退出
		return
	}

	// TUI 模式关闭 verbose 日志输出到 stderr
	if *tui {
		a.VerboseLog = nil
	}
	// 内置 /sessions 命令：列出当前路径下的所有会话（含标题）
	a.Commands.Register(agent.Command{
		Name:        "sessions",
		Description: "列出当前路径下的所有会话 ID（短 ID 前 8 位，含自动/用户标题）",
		Run: func(args string) (string, error) {
			ids := st.List()
			if len(ids) == 0 {
				return "（无会话）", nil
			}
			titles := titleCache.ListAll()
			var sb strings.Builder
			for _, id := range ids {
				suffix := ""
				if id == *sessionID {
					suffix = " ← 当前"
				}
				short := id
				if len(id) > 8 {
					short = id[:8]
				}
				title := titles[id]
				if title == "" {
					title = "(无标题)"
				}
				fmt.Fprintf(&sb, "  %-12s  %-40s%s\n", short, title, suffix)
			}
			fmt.Fprintf(&sb, "\n当前会话: %s\n", *sessionID)
			fmt.Fprintf(&sb, "续接会话: /session <id> 或 mizar -session <id>（mizar -c 续接最后一次退出的会话）\n")
			return sb.String(), nil
		},
	})
	// 内置 /session 命令：切换当前会话
	a.Commands.Register(agent.Command{
		Name:        "session",
		Description: "切换会话（/session 当前 ｜ /session <id> 续接 ｜ /session new 新建）",
		Run: func(args string) (string, error) {
			aargs := strings.TrimSpace(args)
			if aargs == "" || aargs == "current" || aargs == "当前" {
				short := *sessionID
				if len(short) > 8 {
					short = short[:8]
				}
				return fmt.Sprintf("当前会话: %s", short), nil
			}
			if aargs == "new" || aargs == "新建" {
				*sessionID = uuid.New().String()
				a.Initial = nil
				short := *sessionID
				if len(short) > 8 {
					short = short[:8]
				}
				log.Printf("切换到新会话: %s", short)
				return fmt.Sprintf("✓ 新会话: %s", short), nil
			}
			// 尝试加载指定会话（支持完整 ID 或前缀匹配）
			msgs, err := st.Load(aargs)
			if err != nil {
				// 尝试短 ID 前缀匹配
				found := false
				for _, id := range st.List() {
					if strings.HasPrefix(id, aargs) {
						msgs, err = st.Load(id)
						if err == nil {
							*sessionID = id
							found = true
							break
						}
					}
				}
				if !found {
					return "", fmt.Errorf("会话 %q 不存在: %v", aargs, err)
				}
			}
			if err == nil && len(msgs) > 0 {
				// 恢复子任务句柄（GAP-S4）+ 剔除标记消息（与启动恢复同款）
				if raw, rerr := st.LoadRaw(*sessionID); rerr == nil {
					a.SubAgents.RestoreFromMessages(raw)
				}
				msgs = agent.StripSubagentStateMessages(msgs)
				if len(msgs) > *history {
					msgs = msgs[len(msgs)-*history:]
				}
				a.Initial = msgs
				builtins.SetTodosFromHistory(msgs)
				log.Printf("切换到会话 %s: %d 条历史", *sessionID, len(msgs))
			}
			short := *sessionID
			if len(short) > 8 {
				short = short[:8]
			}
			return fmt.Sprintf("✓ 切换到会话: %s", short), nil
		},
	})

	// 内置 /fork 命令：从当前历史派生新会话分支（P-2，核心逻辑在 forkcmd.go）
	a.Commands.Register(agent.Command{
		Name:        "fork",
		Description: "从当前会话历史分叉出新会话（/fork 全量 ｜ /fork <n> 只保留前 n 条消息），源会话保持不变",
		Run: func(args string) (string, error) {
			return forkSession(a, st, titleCache, sessionID, args)
		},
	})

	// 交互模式
	// 未配置供应商时给出引导（banner 末尾追加提示）
	if cfg, err := config.Load(config.DefaultPath()); err != nil || cfg.BaseURL == "" {
		explicit := map[string]bool{}
		flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		if !explicit["base-url"] {
			startupHint = fmt.Sprintf(
				"  ⚠ 尚未配置 LLM 供应商（baseURL + API Key），当前使用默认地址 %s\n"+
					"  在对话中配置（修改后自动保存到 %s）：\n"+
					"    /provider <baseURL> <apiKey>   一次设置 base URL 与 API Key\n"+
					"    /model <name>                   选择模型\n"+
					"  或命令行启动: mizar -base-url <baseURL> -api-key <key> -model <model>\n"+
					"  查看当前配置: /provider  ｜ 全部命令: /help",
				*baseURL, config.DefaultPath())
		}
	}
	if *tui {
		diag.Logf("tui", "runTUI 会话=%s logs=%s", *sessionID, diag.Path())
		// 传指针：/session 切换、/fork 分叉在 TUI 内执行后，落盘目标立即跟随
		// （此前传值拷贝，TUI 内切换会话后仍写旧文件——既有错位缺陷）
		runTUI(a, st, sessionID, titleCache, mcpReg.Stats)
		diag.Logf("tui", "runTUI 返回，进程结束")
	} else {
		// 与 runTUI 同理：传指针，交互模式内 /session //fork 切换后落盘跟随
		interactive(a, st, sessionID)
	}

	// 退出时记录当前会话 ID，供下次 `mizar -c` 直接续接最后一次（已退出的）会话。
	// 仅记录已落盘（有消息）的会话，避免记录空会话后 -c 续接不到内容。
	if *sessionID != "" && st != nil && st.Exists(*sessionID) {
		if err := st.SetLast(*sessionID); err != nil {
			log.Printf("记录最后会话失败: %v", err)
		}
	}
}

// mcpNames 返回 MCP server 名列表（日志用）。
func mcpNames(servers []engine.MCPServerConf) string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// renderSkillsPrompt 按当前注入模式（index/full）重新渲染技能提示段。
func renderSkillsPrompt(skm *skills.Manager, mode string) string {
	if mode == "full" {
		return skm.RenderAll()
	}
	return skm.RenderIndex()
}

// cfgSkillMode 返回技能注入模式（index 默认 / full）。
func cfgSkillMode() string {
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.SkillInjectIsIndex() {
		return "index"
	}
	return "full"
}

// truncateStr 按字符数截断（多字节安全）。
func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// shortSID 返回会话 id 前 8 位（展示用）。
func shortSID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// findMizarDir 从 workDir 向上递归查找包含 .mizar/prompts/ 的项目目录。
// 空字符串表示未找到。
func findMizarDir(dir string) string {
	for {
		candidate := filepath.Join(dir, ".mizar", "prompts")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
