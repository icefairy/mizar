// Mizar (开阳) —— 极简自举 Agent CLI。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ctxpkg "context"
	"github.com/google/uuid"

	"mizar/internal/agent"
	"mizar/internal/builtins"
	"mizar/internal/config"
	"mizar/internal/context"
	"mizar/internal/engine"
	"mizar/internal/jobs"
	"mizar/internal/llm"
	"mizar/internal/lsp"
	"mizar/internal/plugins"
	"mizar/internal/prompts"
	"mizar/internal/schedule"
	"mizar/internal/server"
	"mizar/internal/session"
	"mizar/internal/skills"
)

var version = "v0.4.1"

// startupHint 首次运行未配置供应商时，banner 末尾追加的引导提示。
var startupHint string

func main() {
	var (
		baseURL   = flag.String("base-url", "http://127.0.0.1:3002/v1", "OpenAI 兼容端点 (默认指向璇玑网关)")
		apiKey    = flag.String("api-key", "", "API Key (可选)")
		model     = flag.String("model", "deepseek-v4-flash", "模型名")
		extDir    = flag.String("ext", "", "插件目录 (默认 ~/.mizar/extensions)")
		skillDir  = flag.String("skills", "skills", "技能目录")
		workDir   = flag.String("workdir", "", "工作目录 (AGENTS.md 查找起点, 默认当前目录)")
		sessDir   = flag.String("sessions", "~/.mizar/sessions", "会话目录 (默认 ~/.mizar/sessions，按工作路径自动分目录)")
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
	// 兼容别名：-server → -serve（Go flag 无别名机制，解析前替换）
	// 避免用户拼写 -server 时直接 flag 报错
	for i, a := range os.Args[1:] {
		if a == "-server" || a == "--server" {
			os.Args[i+1] = "-serve"
		}
	}
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
	// 技能使用统计（默认开启，全局配置 skill_stats_enabled=false 关闭）
	stats := skills.LoadStats(filepath.Join(config.ConfigDir(), "skill_stats.json"))

	pm := plugins.NewManager(extAbs, host)
	// 禁用工具（来自技能统计周报，用户确认后记录）——只影响工具注册表，不动 System prompt
	pm.SetDisabledTools(stats.DisabledNames())
	// agent 实例先声明后赋值（builtins.All 的插件热重载回调需要引用它）
	a := agent.New(client, pm)
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
	a.SkillsPrompt = skPrompt
	// AGENTS.md 自动读取（全局 ~/.mizar/AGENTS.md + 局部向上查找，相加注入）
	wd := *workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	contextPrompt := context.LoadContextFiles(wd)
	if contextPrompt != "" {
		log.Printf("上下文文件注入: %s", strings.Join(context.ContextFiles(wd), ", "))
	}
	// SOUL.md 身份声明（跨会话稳定，注入 stable+context 层）
	a.Soul = context.LoadSoul()
	if a.Soul != "" {
		log.Printf("SOUL.md 注入: %s", context.SoulPath())
	}

	// 会话存储（模仿 pi Agent：按工作路径自动分目录，路径记忆）
	sessAbs := *sessDir
	if strings.HasPrefix(sessAbs, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			sessAbs = filepath.Join(home, strings.TrimPrefix(sessAbs, "~/"))
		}
	}
	st := session.New(sessAbs)
	pathKey := session.EncodePathKey(wd)
	if pathKey != "" {
		st = st.Scoped(pathKey)
		log.Printf("路径记忆: 会话目录 %s/%s", sessAbs, pathKey)
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
	// 辅助 judge 函数：用主 LLM
	auxLLM := a.LLM
	a.AuxLLM = auxLLM
	goalSvc.SetJudgeFn(agent.BuildDefaultJudgeFn(auxLLM))
	// Subagent 管理器
	var subLLM agent.ToolCallLLM
	if tllm, ok := a.LLM.(agent.ToolCallLLM); ok {
		subLLM = tllm
	}
	a.SubAgents = agent.NewSubagentManager(subLLM, a.System)
	// BackgroundReview：后台自学习
	homeDir, _ := os.UserHomeDir()
	skillsDir := filepath.Join(homeDir, ".mizar", "skills")
	a.BgReview = agent.NewBackgroundReview(agent.BackgroundReviewConfig{
		MinTurnsBetweenReviews: 3,
		AuxLLM:                 auxLLM,
		SkillsDir:              skillsDir,
	})
	// StatsData
	a.StatsData = &agent.StatsDataRef{}
	// 注册后台任务工具（与 a.Jobs 同一 registry 实例，保证跨工具一致性）
	for _, t := range builtins.JobTools(a.Jobs) {
		pm.RegisterBuiltin(t)
	}
	// ask_user_question：TUI 模式下由 tui.go 设置回调；非 TUI 保持 nil（降级提示）
	// 全局可访问的计划模式实例（供 builtins 工具引用）
	agent.CurrentPlanMode = a.PlanMode
	// 定时提醒注册表（投递回调：到期时打印到终端）
	schedReg := schedule.NewRegistry(func(id, prompt string) {
		log.Printf("[schedule] 提醒到期: %s — %s", id, prompt)
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
	// 子代理委派工具
	pm.RegisterBuiltin(agent.DelegateTool(a.SubAgents))
	pm.RegisterBuiltin(agent.GetSubagentTool(a.SubAgents))
	pm.RegisterBuiltin(agent.ListSubagentsTool(a.SubAgents))
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
	if projDir := findMizarDir(wd); projDir != "" {
		promptReg.AddDir(filepath.Join(projDir, ".mizar", "prompts"))
	}
	promptReg.Reload()
	log.Printf("Prompt 模板加载 %d 个", len(promptReg.List()))
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

	// 内置 /provider 命令：查看/切换/配置 LLM 供应商
	a.Commands.Register(agent.Command{
		Name:        "provider",
		Description: "查看当前 LLM 供应商（/provider）、切换 URL（/provider <baseURL>）或配置完整信息（/provider <baseURL> <apiKey>）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				thinking := client.ThinkingEnabled()
				keyMask := "<未设置>"
				if client.APIKey != "" {
					keyMask = fmt.Sprintf("%s...%s", client.APIKey[:2], client.APIKey[len(client.APIKey)-2:])
				}
				return fmt.Sprintf(`当前 LLM 配置:
  供应商 baseURL: %s
  模型 model:     %s
  思考模式 thinking: %s
  API Key:       %s

设置方法（修改后自动保存到 %s）：
  /provider <baseURL> <apiKey>   一次设置 base URL 与 API Key
  /provider <baseURL>            仅切换 base URL
  /apikey <apiKey>               单独设置 API Key
  /model <name>                  切换模型
  示例: /provider https://api.deepseek.com/v1 sk-xxxx 然后 /model deepseek-chat`, client.BaseURL, client.Model, thinking, keyMask, config.DefaultPath()), nil
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
	// 内置 /model 命令：查看/切换模型；无参数时从供应商拉取可用模型清单（/v1/models）
	a.Commands.Register(agent.Command{
		Name:        "model",
		Description: "查看/列出模型（/model 列出供应商可用模型）或切换（/model <name>）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			// 拉取供应商模型清单（网络失败不阻断，仅提示）
			models, listErr := config.ListModels(client.BaseURL, client.APIKey)
			if args == "" {
				var sb strings.Builder
				fmt.Fprintf(&sb, "当前模型: %s\n", client.Model)
				if listErr == nil && len(models) > 0 {
					fmt.Fprintf(&sb, "供应商可用模型（%s，共 %d 个，* 为当前）：\n", client.BaseURL, len(models))
					for _, m := range models {
						mark := " "
						if m == client.Model {
							mark = "*"
						}
						fmt.Fprintf(&sb, "  %s %s\n", mark, m)
					}
					sb.WriteString("切换: /model <名称>")
				} else if listErr != nil {
					fmt.Fprintf(&sb, "获取模型清单失败: %v\n可 /model <名字> 直接指定模型名（不依赖清单）", listErr)
				} else {
					sb.WriteString("供应商未返回可用模型，可 /model <名字> 直接指定模型名")
				}
				return sb.String(), nil
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
			out := fmt.Sprintf("✓ 模型已切换: %s → %s", old, args)
			// 清单可获取但目标不在内：警告不阻断（网关代理场景可能接受任意名）
			if listErr == nil && len(models) > 0 {
				found := false
				for _, m := range models {
					if m == args {
						found = true
						break
					}
				}
				if !found {
					out += fmt.Sprintf("\n⚠ 模型 %q 不在供应商清单中（可执行 /model 查看可用清单）", args)
				}
			}
			return out, nil
		},
	})
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
		Description: "进入/退出计划模式（/plan 进入 ｜ /plan <指令> 进入并附带规划指引 ｜ /plan off 直接退出）",
		Run: func(args string) (string, error) {
			arg := strings.TrimSpace(args)
			if arg == "off" || arg == "关闭" || arg == "退出" {
				a.PlanMode.Exit()
				return "✓ 已退出计划模式。模型可直接执行任务。", nil
			}
			a.PlanMode.Enter(arg)
			msg := "✓ 已进入计划模式。模型将先探索设计方案并通过 exit_plan_mode 提交计划供审批。"
			if arg != "" && arg != "进入" && arg != "on" {
				msg += fmt.Sprintf("\n规划指引：%s", arg)
			}
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
		Name:        "config",
		Description: "查看/修改运行配置（/config 查看 ｜ /config max_steps 30 设置）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			cfg, _ := config.Load(config.DefaultPath())
			if args == "" {
				// 查看当前生效配置（内存中的 a.MaxSteps 优先，因为可能被 Run 兜底改写）
				ms := a.MaxSteps
				if ms < 0 {
					return fmt.Sprintf(`当前运行配置:
  最大步数 max_steps: -1 (无限)
  上下文窗口 context_window: %d (token, 配置:%d)
  思考等级 thinking_level: %s
  模型 model: %s
  供应商 base_url: %s
可修改项: max_steps ｜ 修改方法: /config max_steps 30`, *ctxWindow, cfg.ContextWindow, cfg.ThinkingStr(), client.Model, client.BaseURL), nil
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
			return "", fmt.Errorf("用法: /config 查看 ｜ /config max_steps 30 或 /config max_steps -1(无限)")
		},
	})
	// 内置 /compact 命令：手动触发上下文压缩
	a.Commands.Register(agent.Command{
		Name:        "compact",
		Description: "手动触发上下文压缩（/compact 或 /compact <摘要指引>）",
		Run: func(args string) (string, error) {
			if a.Compactor == nil {
				return "✗ 会话压缩未启用（启动时使用了 --no-compact）", nil
			}
			msgs := a.Initial
			if len(msgs) <= 1 {
				return "✗ 消息不足，无需压缩", nil
			}
			est := agent.EstimateMessages(msgs)
			limit := a.Compactor.ContextWindow - a.Compactor.ReserveTokens
			var sb strings.Builder
			fmt.Fprintf(&sb, "当前估算: %d/%d tokens（%d 条消息）\n", est, limit, len(msgs))
			if est <= limit {
				sb.WriteString("  未达到压缩阈值，仍可强制压缩：/compact force\n")
			}
			cut := a.Compactor.FindCutPoint(msgs)
			if cut <= 0 {
				sb.WriteString("  无可切点（保留消息量在预算内）\n")
				return sb.String(), nil
			}
			action := strings.TrimSpace(args)
			force := strings.HasPrefix(strings.ToLower(action), "force")
			customPrompt := ""
			if !force && action != "" {
				customPrompt = action
			}
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
			newMsgs, err := a.Compactor.Compact(msgs)
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
			if err := config.Save(config.DefaultPath(), cfg); err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			return fmt.Sprintf("✓ 配置已保存: %s", config.DefaultPath()), nil
		},
	})
	// 内置 /mcp 命令：查看/管理 MCP server
	a.Commands.Register(agent.Command{
		Name:        "mcp",
		Description: "MCP server 管理（/mcp 列表 ｜ /mcp add <name> <url|command> [args...] ｜ /mcp remove <name> ｜ /mcp test <name> ｜ /mcp reload）",
		Run: func(args string) (string, error) {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return "", fmt.Errorf("读取配置失败: %v", err)
			}
			args = strings.TrimSpace(args)
			if args == "" {
				// 列表模式
				var sb strings.Builder
				sb.WriteString("MCP servers（配置来源: ~/.mizar/config.json）:\n")
				if len(cfg.MCPServers) == 0 {
					sb.WriteString("  （未配置，使用 /mcp add 添加）\n")
				} else {
					for _, s := range cfg.MCPServers {
						transport := "stdio"
						addr := s.Command
						if s.URL != "" {
							transport = "http"
							addr = s.URL
						}
						if s.Timeout > 0 {
							sb.WriteString(fmt.Sprintf("  %-15s [%s] %s  timeout=%ds\n", s.Name, transport, addr, s.Timeout))
						} else {
							sb.WriteString(fmt.Sprintf("  %-15s [%s] %s\n", s.Name, transport, addr))
						}
						if len(s.Args) > 0 {
							sb.WriteString(fmt.Sprintf("    args: %s\n", strings.Join(s.Args, " ")))
						}
					}
				}
				sb.WriteString("\n用法:\n")
				sb.WriteString("  /mcp add <name> <url>           添加 HTTP server\n")
				sb.WriteString("  /mcp add <name> <command> [args] 添加 stdio server\n")
				sb.WriteString("  /mcp remove <name>              删除 server\n")
				sb.WriteString("  /mcp test <name>                测试连接并列出工具\n")
				sb.WriteString("  /mcp reload                     重新加载配置\n")
				return sb.String(), nil
			}
			fields := strings.Fields(args)
			cmd := fields[0]
			switch cmd {
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
				// 重新从配置文件加载
				newCfg, err := config.Load(config.DefaultPath())
				if err != nil {
					return "", fmt.Errorf("读取配置失败: %v", err)
				}
				cfg.MCPServers = newCfg.MCPServers
				return fmt.Sprintf("✓ MCP 配置已重新加载（%d 个 server: %s）", len(cfg.MCPServers), mcpNames(cfg.MCPServers)), nil

			default:
				return "", fmt.Errorf("未知子命令 %q，支持: add/remove/test/reload（输入 /mcp 查看帮助）", cmd)
			}
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

	// 启动时打印缓存统计（首次调用 SystemPrompt() 后会在运行时更新）
	if a.CacheStats() != nil {
		log.Printf("%s", a.CacheStats().Summary())
	}

	if *sessionID != "" {
		msgs, err := st.Load(*sessionID)
		if err == nil && len(msgs) > 0 {
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
			short := *sessionID
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Printf("\n会话 %s: 用 mizar -session %s 续接\n", short, *sessionID)
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
		fmt.Printf("   Ctrl+C 停止服务\n\n")
		if err := srv.Start(); err != nil {
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
			fmt.Fprintf(&sb, "续接会话: /session <id> 或 mizar -session <id>\n")
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
				if len(msgs) > *history {
					msgs = msgs[len(msgs)-*history:]
				}
				a.Initial = msgs
				log.Printf("切换到会话 %s: %d 条历史", *sessionID, len(msgs))
			}
			short := *sessionID
			if len(short) > 8 {
				short = short[:8]
			}
			return fmt.Sprintf("✓ 切换到会话: %s", short), nil
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
		runTUI(a, st, *sessionID, titleCache)
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
