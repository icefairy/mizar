package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/peterh/liner"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// banner 启动标语。若设置了 startupHint（首次运行未配置供应商），追加热示。
func banner() string {
	b := fmt.Sprintf(`
  ███╗   ███╗██╗███████╗ █████╗ ██████╗
  ████╗ ████║██║╚══███╔╝██╔══██╗██╔══██╗
  ██╔████╔██║██║  ███╔╝ ███████║██████╔╝
  ██║╚██╔╝██║██║ ███╔╝  ██╔══██║██╔══██╗
  ██║ ╚═╝ ██║██║███████╗██║  ██║██║  ██║
  ╚═╝     ╚═╝╚═╝╚══════╝╚═╝  ╚═╝
              %s — 开阳 · 自举式 AI Agent

  ◆ 自举循环：goja 沙箱 → 工具调用 → 反思 → 决策
  ◆ 离线部署：单文件二进制，数据不出内网
  ◆ 插件系统：TS/JS 双写，热重载，LSP 诊断/补全
  ◆ 全链路审计：操作留痕，技能用量透明
  ◆ 内置 TUI：Tab 补全 / Ctrl+T 思考 / 实时 token 统计
  ◆ 会话压缩：自动超窗压缩，摘要保留上下文
  ◆ 模型兼容：OpenAI 兼容端点，思考等级 auto/off/low/medium/high
  ◆ TUI 模式：Enter 发送 / Alt+Enter 换行 / Esc 取消 / Ctrl+T 思考等级
  ◆ 命令行模式：Enter 发送 ｜ 多行粘贴自动合并 ｜ /send 手动提交
  ◆ 鼠标：Shift+拖拽 选择复制 ｜ 滚轮滚动 ｜ PgUp/PgDn 翻页
`, version)
	if startupHint != "" {
		b += startupHint + "\n"
	}
	return b
}

// interactive 运行交互式对话（readline 支持：退格删除 / 历史上下键 / Tab 补全）。
// 退出时会打印会话 ID，告知用户如何续接。
// 多行粘贴：快速连续输入（<300ms）自动合并为多行文本；输入 /send 手动提交当前 buffer。
func interactive(a *agent.Agent, st *session.Store, sessionID string) {
	rl := liner.NewLiner()
	defer rl.Close()
	rl.SetCtrlCAborts(true)

	// 退出提示
	if sessionID != "" {
		short := sessionID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("\n会话 %s: 用 mizar -session %s 续接\n", short, sessionID)
	}

	// 渲染已加载的历史消息
	for _, msg := range a.Initial {
		switch msg.Kind {
		case agent.KindToolCall:
			fmt.Printf("\033[36m🔧 %s(%s)\033[0m\n\n", msg.ToolName, truncateArgs(msg.ToolArgs))
			continue
		case agent.KindToolResult:
			result := strings.TrimSpace(strings.TrimPrefix(msg.Content, "工具结果: "))
			if len(result) > 300 {
				result = result[:297] + "..."
			}
			fmt.Printf("\033[36m🔧 → %s\033[0m\n\n", result)
			continue
		}
		if msg.Role == agent.RoleUser {
			fmt.Printf("\033[37m▶ %s\033[0m\n\n", msg.Content)
		} else {
			fmt.Printf("\033[32m▲ \033[0m%s\n\n", msg.Content)
		}
	}

	// 预取命令和工具列表（补全用）
	cmds := a.Commands.List()
	cmdNames := make([]string, 0, len(cmds))
	for _, c := range cmds {
		cmdNames = append(cmdNames, "/"+c.Name)
	}

	// 工具列表（从插件管理器取）
	tools := a.Plugins.Tools()
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}

	// 补全器：支持 @cmd: / @tool: / @file: / @路径 四种模式
	rl.SetCompleter(func(line string) (res []string) {
		if idx := strings.LastIndex(line, "@"); idx >= 0 {
			after := strings.TrimSpace(line[idx+1:])
			res = completeAtRaw(after, cmdNames, toolNames)
			return
		}
		for _, n := range cmdNames {
			if strings.HasPrefix(n, line) {
				res = append(res, n)
			}
		}
		return
	})

	// 初始化 glamour 渲染器
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
	)

	fmt.Print(banner())

	// 多行 buffer：快速连续输入时累积多行文本
	var mlBuf strings.Builder
	var lastPromptTime time.Time

	for {
		line, err := rl.Prompt(">")
		if err != nil {
			fmt.Println()
			return // EOF / Ctrl-C
		}
		now := time.Now()
		trimmed := strings.TrimSpace(line)

		// 多行合并：300ms 内的连续非空输入合并为多行
		if mlBuf.Len() > 0 && now.Sub(lastPromptTime) < 300*time.Millisecond && trimmed != "" && trimmed != "/send" {
			mlBuf.WriteString("\n")
			mlBuf.WriteString(trimmed)
			lastPromptTime = now
			continue
		}

		// 提交多行 buffer
		if mlBuf.Len() > 0 {
			trimmed = mlBuf.String()
			mlBuf.Reset()
		}
		lastPromptTime = now

		if trimmed == "" {
			continue
		}
		if trimmed == "/send" {
			continue // /send 只是多行模式的终止符
		}
		rl.AppendHistory(trimmed)

		// 斜杠命令分发
		if handled, out, err := a.Commands.Dispatch(trimmed); handled {
			if err != nil {
				fmt.Printf("命令错误: %v\n", err)
			}
			if out != "" {
				fmt.Println(out)
			}
			continue
		}

		// 处理 @file: / @cmd: / @tool: 引用
		input := resolveAtRef(trimmed, cmdNames, toolNames)

		// 工具交换收集：任务结束时持久化到会话文件（经典模式无需 Initial，历史逐轮重放）
		var exchanges []agent.Message
		a.OnToolExchange = func(tool, args, out string, err error) {
			exchanges = append(exchanges, agent.ToolExchangeMessages(tool, args, out, err)...)
		}

		// LLM 调用（流式）
		var extr agent.StreamTextExtractor
		var streamed atomic.Bool
		spinnerStop := make(chan struct{})
		go classicSpinner(spinnerStop, &streamed)
		a.OnLLMStream = func(step int, d agent.StreamDelta) {
			if d.Content == "" {
				return
			}
			inc := extr.Feed(d.Content)
			if inc == "" {
				return
			}
			if !streamed.Swap(true) {
				fmt.Print("\r\x1b[K")
			}
			fmt.Print(inc)
		}
		reply, err := a.Run(input)
		a.OnLLMStream = nil
		a.OnToolExchange = nil
		close(spinnerStop)
		if err != nil {
			fmt.Printf("\r\x1b[K错误: %v\n", err)
			continue
		}
		if !streamed.Load() {
			fmt.Print("\r\x1b[K")
			rendered, renderErr := renderer.Render(reply)
			if renderErr != nil {
				fmt.Println(reply)
			} else {
				fmt.Print(rendered)
				if !strings.HasSuffix(rendered, "\n") {
					fmt.Println()
				}
			}
		} else {
			fmt.Println()
		}

		if sessionID != "" {
			// 落盘顺序与循环内消息顺序一致：user → (tool_call, tool_result)* → assistant reply
			st.Append(sessionID, agent.Message{Role: agent.RoleUser, Content: trimmed})
			for _, msg := range exchanges {
				st.Append(sessionID, msg)
			}
			st.Append(sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
		}
	}
}
