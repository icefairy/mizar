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
	"strings"
	"time"

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
		initWiz   = flag.Bool("init", false, "运行初始化向导（配置供应商/模型）")
		lspBinary = flag.String("lsp", "", "LSP 语言服务器路径 (如 gopls/tsserver，空=禁用 LSP)")
		// Server 模式（持久运行 daemon）
		serve  = flag.Bool("serve", false, "启动 Server 模式（持久运行）")
		addr   = flag.String("addr", ":3003", "Server 监听地址")
		token  = flag.String("token", "", "Server Bearer token (空=不认证)")
		httpOn = flag.Bool("http", true, "Server: 启用 OpenAI 兼容 HTTP")
		rpcOn  = flag.Bool("rpc", true, "Server: 启用 JSON-RPC")
		wsOn   = flag.Bool("ws", true, "Server: 启用 WebSocket")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("mizar", version)
		return
	}

	// 初始化向导：交互式配置供应商/模型/思考/上下文
	if *initWiz {
		cfg, err := config.RunWizard(config.DefaultPath())
		if err != nil {
			log.Fatalf("初始化失败: %v", err)
		}
		if cfg.BaseURL == "" {
			fmt.Println("未配置供应商，退出。")
			return
		}
		fmt.Printf("初始化完成，配置已保存到 %s\n", config.DefaultPath())
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
		log.Printf("已加载配置 %s: model=%s thinking=%v window=%d", config.DefaultPath(), *model, cfg.Thinking, *ctxWindow)
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
	}
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
	if cfg, err := config.Load(config.DefaultPath()); err == nil && cfg.Thinking {
		thinking := true
		client.Thinking = &thinking
	}
	host.LLMChat = func(messagesJSON string) (string, error) {
		var msgs []agent.Message
		if err := jsonUnmarshal(messagesJSON, &msgs); err != nil {
			return "", err
		}
		return client.Chat(msgs)
	}

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
	log.Printf("可用工具 %d 个", len(tools))
	for _, t := range tools {
		fmt.Printf("  - %s\n", t.Name)
	}

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
	a.System = `你是开阳(Mizar) Agent，一个极简自举的智能体。你可以调用工具完成任务，工具出错时尝试修复或换一种方式。请用简洁的中文回答。` + skPrompt + agentsPrompt
	a.VerboseLog = func(msg string) { log.Print(msg) }

	// 内置 /provider 命令：查看/切换 LLM 供应商（baseURL）
	a.Commands.Register(agent.Command{
		Name:        "provider",
		Description: "查看当前 LLM 供应商（/provider）或切换（/provider <baseURL>）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				thinking := "off"
				if client.Thinking != nil && *client.Thinking {
					thinking = "on"
				}
				return fmt.Sprintf("当前供应商: %s\n模型: %s\n思考模式: %s", client.BaseURL, client.Model, thinking), nil
			}
			old := client.BaseURL
			client.BaseURL = strings.TrimSuffix(args, "/")
			return fmt.Sprintf("✓ 供应商已切换: %s → %s", old, client.BaseURL), nil
		},
	})
	// 内置 /model 命令：查看/切换模型（对齐 pi 的 /model）
	a.Commands.Register(agent.Command{
		Name:        "model",
		Description: "查看当前模型（/model）或切换（/model <name>）",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				return fmt.Sprintf("当前模型: %s", client.Model), nil
			}
			old := client.Model
			client.Model = args
			return fmt.Sprintf("✓ 模型已切换: %s → %s", old, args), nil
		},
	})
	// 内置 /reload 命令：重载 ~/.mizar/config.json + 插件热重载
	a.Commands.Register(agent.Command{
		Name:        "reload",
		Description: "重载 ~/.mizar/config.json 与插件（/reload）",
		Run: func(args string) (string, error) {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil || cfg.BaseURL == "" {
				return "", fmt.Errorf("重载失败: %v（先运行 --init 或检查 %s）", err, config.DefaultPath())
			}
			// 应用配置到客户端
			client.BaseURL = cfg.BaseURL
			client.APIKey = cfg.APIKey
			client.Model = cfg.Model
			if cfg.Thinking {
				thinking := true
				client.Thinking = &thinking
			} else {
				client.Thinking = nil
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
			fmt.Fprintf(&sb, "✓ 配置已重载: model=%s window=%d thinking=%v\n", cfg.Model, cfg.ContextWindow, cfg.Thinking)
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

	// 交互模式
	interactive(a, st, *sessionID)
}
