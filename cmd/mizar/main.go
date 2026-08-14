// Mizar (开阳) —— 极简自举 Agent CLI。
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ctxpkg "context"

	"mizar/internal/agent"
	"mizar/internal/builtins"
	"mizar/internal/config"
	"mizar/internal/context"
	"mizar/internal/engine"
	"mizar/internal/llm"
	"mizar/internal/lsp"
	"mizar/internal/plugins"
	"mizar/internal/server"
	"mizar/internal/session"
	"mizar/internal/skills"
)

var version = "v0.1.0"

func main() {
	var (
		baseURL   = flag.String("base-url", "http://127.0.0.1:3002/v1", "OpenAI 兼容端点 (默认指向璇玑网关)")
		apiKey    = flag.String("api-key", "", "API Key (可选)")
		model     = flag.String("model", "deepseek-v4-flash", "模型名")
		extDir    = flag.String("ext", "extensions", "插件目录")
		skillDir  = flag.String("skills", "skills", "技能目录")
		workDir   = flag.String("workdir", "", "工作目录 (AGENTS.md 查找起点, 默认当前目录)")
		sessDir   = flag.String("sessions", "sessions", "会话目录")
		sessionID = flag.String("session", "", "会话 ID (续接对话)")
		history   = flag.Int("history", 50, "会话恢复的最大历史消息数")
		ctxWindow = flag.Int("ctx-window", 128000, "模型上下文窗口 (token，压缩触发线)")
		noCompact = flag.Bool("no-compact", false, "禁用会话压缩")
		task      = flag.String("task", "", "任务内容 (非空则单次执行)")
		showVer   = flag.Bool("version", false, "显示版本")
		lspBinary = flag.String("lsp", "", "LSP 语言服务器路径 (如 gopls/tsserver，空=禁用 LSP)")
		lspServer = flag.Bool("lsp-server", false, "以 LSP server 模式运行 (stdio)")
		tui       = flag.Bool("tui", true, "使用 Bubble Tea TUI 界面（默认开启，--no-tui 用经典 readline）")
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
	)
	flag.Parse()

	if *showVer {
		fmt.Println("mizar", version)
		return
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

	// 宿主函数集
	host := &engine.HostFuncs{
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
		Log: func(msg string) { log.Print(msg) },
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
	if cfg, err := config.Load(config.DefaultPath()); err == nil && len(cfg.MCPServers) > 0 {
		mcpReg = engine.NewMCPRegistry(cfg.MCPServers)
		log.Printf("MCP server 配置 %d 个: %s", len(cfg.MCPServers), mcpNames(cfg.MCPServers))
	} else if err != nil {
		log.Printf("MCP 配置读取失败（mcp_call 不可用）: %v", err)
	} else {
		log.Printf("未配置 MCP server（mcp_call 不可用，config.json 加 mcp_servers 启用）")
	}
	defer mcpReg.Close()
	host.MCPCall = mcpReg.CallFn()
	// 插件 HTTP 能力：统一 http_request(method,url,body,headers) + 薄封装 http_get/http_post
	httpDo := func(method, url, body, headersJSON string) (string, error) {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if headersJSON != "" {
			var hdr map[string]string
			if err := jsonUnmarshal(headersJSON, &hdr); err != nil {
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
	}
	host.LLMChat = func(messagesJSON string) (string, error) {
		var msgs []agent.Message
		if err := jsonUnmarshal(messagesJSON, &msgs); err != nil {
			return "", err
		}
		return client.Chat(msgs)
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

	extAbs, err := filepath.Abs(*extDir)
	if err != nil {
		log.Fatal(err)
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
	// 技能使用统计（默认开启，全局配置 skill_stats_enabled=false 关闭）
	stats := skills.LoadStats(filepath.Join(config.ConfigDir(), "skill_stats.json"))

	pm := plugins.NewManager(extAbs, host)
	// 禁用工具（来自技能统计周报，用户确认后记录）——只影响工具注册表，不动 System prompt
	pm.SetDisabledTools(stats.DisabledNames())
	// 注册 pi 式内置工具（bash/grep/find/read/write/edit/ls/skill_manage）
	for _, t := range builtins.All(skAbs) {
		pm.RegisterBuiltin(t)
	}
	for _, t := range lsp.All() {
		pm.RegisterBuiltin(t)
	}
	lsp.Init(*lspBinary)
	loaded, failed := pm.LoadAll()
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
		pm.SetOnToolCall(func(name string) { stats.Incr(name) })
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
		skPrompt = skm.RenderIndex()
	} else {
		skMode = "full"
		skPrompt = skm.RenderAll()
	}
	if skPrompt != "" {
		log.Printf("技能注入 %d 个 (%s)", len(skLoaded), skMode)
	}
	// AGENTS.md 自动读取（全局 ~/.mizar/AGENTS.md + 局部向上查找，相加注入）
	wd := *workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	agentsPrompt := context.Load(wd)
	if agentsPrompt != "" {
		log.Printf("AGENTS.md 注入: %s", strings.Join(context.AGENTSFiles(wd), ", "))
	}

	// 会话存储
	st := session.New(*sessDir)

	a := agent.New(client, pm)
	// 从配置读取最大步数（0=默认30）
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.MaxSteps > 0 {
		a.MaxSteps = cfg.MaxSteps
	}
	a.System = `你是开阳(Mizar) Agent，一个极简自举的智能体。你可以调用工具完成任务，工具出错时尝试修复或换一种方式。请用简洁的中文回答。` + skPrompt + agentsPrompt
	a.VerboseLog = func(msg string) { log.Print(msg) }

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

	// 内置 /provider 命令：查看/切换/配置 LLM 供应商
	a.Commands.Register(agent.Command{
		Name:        "provider",
		Description: "查看当前 LLM 供应商（/provider）、切换 URL（/provider  ＜baseURL＞）或配置完整信息（/provider ＜baseURL＞ ＜apiKey＞）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				thinking := client.ThinkingEnabled()
				keyMask := "<set>"
				if client.APIKey != "" {
					keyMask = fmt.Sprintf("%s...%s", client.APIKey[:2], client.APIKey[len(client.APIKey)-2:])
				}
				return fmt.Sprintf("当前供应商: %s\n模型: %s\n思考模式: %s\nAPI Key: %s", client.BaseURL, client.Model, thinking, keyMask), nil
			}
			fields := strings.Fields(args)
			baseURL := strings.TrimSuffix(fields[0], "/")
			oldURL := client.BaseURL
			client.BaseURL = baseURL
			if len(fields) >= 2 {
				client.APIKey = fields[1]
			}
			// 持久化
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				log.Printf("/provider 持久化失败: %v", err)
			}
			if len(fields) >= 2 {
				return fmt.Sprintf("✓ 供应商已切换: %s → %s\n✓ API Key 已更新", oldURL, baseURL), nil
			}
			if oldURL == baseURL {
				return fmt.Sprintf("URL 未变更: %s（设置 API Key: /provider <url> <key> 或 /apikey <key>）", baseURL), nil
			}
			return fmt.Sprintf("✓ 供应商已切换: %s → %s", oldURL, baseURL), nil
		},
	})
	// 内置 /model 命令：查看/切换模型（对齐 pi 的 /model）
	a.Commands.Register(agent.Command{
		Name:        "model",
		Description: "查看当前模型（/model）或切换（/model ＜name＞）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				return fmt.Sprintf("当前模型: %s", client.Model), nil
			}
			old := client.Model
			client.Model = args
			// 持久化到 config.json
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				log.Printf("/model 持久化失败: %v", err)
			}
			return fmt.Sprintf("✓ 模型已切换: %s → %s", old, args), nil
		},
	})
	// 内置 /apikey 命令：查看/设置 API Key（与 /provider 配合使用）
	a.Commands.Register(agent.Command{
		Name:        "apikey",
		Description: "查看当前 API Key（/apikey）或设置（/apikey ＜key＞）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				keyMask := "<未设置>"
				if client.APIKey != "" {
					keyMask = fmt.Sprintf("%s...%s", client.APIKey[:2], client.APIKey[len(client.APIKey)-2:])
				}
				return fmt.Sprintf("当前 API Key: %s\n当前供应商: %s", keyMask, client.BaseURL), nil
			}
			old := client.APIKey
			client.APIKey = args
			// 持久化
			cfg, _ := config.Load(config.DefaultPath())
			cfg.BaseURL = client.BaseURL
			cfg.Model = client.Model
			cfg.APIKey = client.APIKey
			cfg.ContextWindow = *ctxWindow
			cfg.ThinkingLevel = client.ThinkingEnabled()
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
			// 插件热重载
			loaded, failed := pm.ReloadAll()
			// 同步插件命令到注册表
			cmds := make([]agent.Command, 0)
			for _, c := range pm.Commands() {
				cc := c
				cmds = append(cmds, agent.Command{Name: cc.Name, Description: cc.Description, PluginFile: cc.PluginFile, Run: cc.Run})
			}
			a.Commands.SyncFromPlugins(cmds)
			// 汇总
			var sb strings.Builder
			fmt.Fprintf(&sb, "✓ 配置已重载: model=%s window=%d thinking=%s\n", cfg.Model, cfg.ContextWindow, cfg.ThinkingStr())
			if len(loaded) > 0 {
				fmt.Fprintf(&sb, "✓ 插件重载: %s\n", strings.Join(loaded, ", "))
			}
			for f, e := range failed {
				fmt.Fprintf(&sb, "✗ 插件失败: %s: %v\n", f, e)
			}
			return sb.String(), nil
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
	// 内置 /quit 命令：退出交互模式
	a.Commands.Register(agent.Command{
		Name:        "quit",
		Description: "退出交互模式",
		Run: func(args string) (string, error) {
			fmt.Println("再见")
			os.Exit(0)
			return "", nil
		},
	})
	// 内置 /think 命令：查看/切换思考等级（auto/off/low/medium/high）
	a.Commands.Register(agent.Command{
		Name:        "think",
		Description: "查看/切换思考等级（/think 查看 ｜ /think auto/off/low/medium/high 设置 ｜ /think cycle 循环切换）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				return fmt.Sprintf("当前思考等级: %s\n等级说明:\n  auto   — 不传 thinking 参数，由模型自行决定\n  off    — 关闭思考\n  low    — 低强度思考（最快，推理 effort=low）\n  medium — 中等强度思考（默认推荐）\n  high   — 高强度思考（最彻底，推理 effort=high）", client.ThinkingEnabled()), nil
			}
			supported := []string{"auto", "off", "low", "medium", "high", "cycle"}
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
			return "", fmt.Errorf("未知参数 %q，支持: auto/off/low/medium/high/cycle", args)
		},
	})
	// 内置 /help 命令：列出所有可用命令
	a.Commands.Register(agent.Command{
		Name:        "help",
		Description: "列出所有可用命令",
		Run: func(args string) (string, error) {
			cmds := a.Commands.List()
			var sb strings.Builder
			sb.WriteString("**可用命令：**\n")
			for _, c := range cmds {
				sb.WriteString(fmt.Sprintf("- `/%s` %s", c.Name, c.Description))
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
		Name:        "config",
		Description: "查看/修改运行配置（/config 查看 ｜ /config max_steps 30 设置）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			cfg, _ := config.Load(config.DefaultPath())
			if args == "" {
				// 查看当前生效配置（内存中的 a.MaxSteps 优先，因为可能被 Run 兜底改写）
				ms := a.MaxSteps
				if ms <= 0 {
					ms = 30
				}
				return fmt.Sprintf(`当前运行配置:
  最大步数 max_steps: %d
  上下文窗口 context_window: %d (token, 配置:%d)
  思考等级 thinking_level: %s
  模型 model: %s
  供应商 base_url: %s
可修改项: max_steps ｜ 修改方法: /config max_steps 30`, ms, *ctxWindow, cfg.ContextWindow, cfg.ThinkingStr(), client.Model, client.BaseURL), nil
			}
			fields := strings.Fields(args)
			if len(fields) == 2 && fields[0] == "max_steps" {
				n, err := strconv.Atoi(fields[1])
				if err != nil || n < 1 || n > 1000 {
					return "", fmt.Errorf("max_steps 必须是 1-1000 的整数")
				}
				a.MaxSteps = n
				cfg.MaxSteps = n
				if err := config.Save(config.DefaultPath(), cfg); err != nil {
					return "", fmt.Errorf("持久化失败: %v", err)
				}
				return fmt.Sprintf("✓ 最大步数已设置为 %d，已持久化到 %s", n, config.DefaultPath()), nil
			}
			return "", fmt.Errorf("用法: /config 查看 ｜ /config max_steps ＜数字1-1000＞")
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
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			return fmt.Sprintf("✓ 配置已保存: %s", config.DefaultPath()), nil
		},
	})
	if !*noCompact {
		client2 := client // SummarizeMessages 用同一客户端
		a.Compactor = agent.DefaultCompactor(func(msgs []agent.Message) (string, error) {
			return client2.SummarizeMessages(msgs, 1500)
		})
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

	if *sessionID != "" {
		msgs, err := st.Load(*sessionID)
		if err == nil && len(msgs) > 0 {
			if len(msgs) > *history {
				msgs = msgs[len(msgs)-*history:]
			}
			a.Initial = msgs
			log.Printf("恢复会话 %s: %d 条历史", *sessionID, len(msgs))
		}
	}

	if *task != "" {
		reply, err := a.Run(*task)
		if err != nil {
			log.Fatalf("执行失败: %v", err)
		}
		fmt.Println("\n=== 最终回答 ===")
		fmt.Println(reply)
		if *sessionID != "" {
			st.Append(*sessionID, agent.Message{Role: agent.RoleUser, Content: *task})
			st.Append(*sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
			log.Printf("会话 %s 已保存", *sessionID)
		}
		return
	}

	// Server 模式（持久运行 daemon）
	if *serve {
		cfg := server.NewConfig(*addr, *token)
		cfg.SetAll(*httpOn, *rpcOn, *wsOn)
		srv := server.New(cfg, a, st)
		// 插件 ws_emit 能力：注入 Server 广播
		host.WSEmit = func(event, dataJSON string) { srv.EmitToWS(event, dataJSON) }
		// 强制重载使 ws_emit 对已加载插件生效
		pm.ReloadAll()
		log.Printf("Server 模式启动: %s (http=%v rpc=%v ws=%v)", *addr, *httpOn, *rpcOn, *wsOn)
		log.Printf("  OpenAI 兼容:   POST %s/v1/chat/completions", *addr)
		log.Printf("  JSON-RPC 2.0:   POST %s/rpc (agent.run/steer/abort)", *addr)
		log.Printf("  WebSocket:      %s/ws (实时事件 + 快速纠正)", *addr)
		log.Printf("  admin:          GET %s/admin/status  POST %s/admin/switch", *addr, *addr)
		if err := srv.Start(); err != nil {
			log.Fatalf("Server 启动失败: %v", err)
		}
		return
	}

	// TUI 模式关闭 verbose 日志输出到 stderr
	if *tui {
		a.VerboseLog = nil
	}

	// 交互模式
	if *tui {
		runTUI(a, st, *sessionID)
	} else {
		interactive(a, st, *sessionID)
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
