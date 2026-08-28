package main

import (
	"fmt"
	"strings"
	"sync/atomic"

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
  ╚═╝     ╚═╝╚═╝╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝
              %s — 开阳 · 自举式 AI Agent

  ◆ 自举循环：goja 沙箱 → 工具调用 → 反思 → 决策
  ◆ 离线部署：单文件二进制，数据不出内网
  ◆ 插件系统：TS/JS 双写，热重载，LSP 诊断/补全
  ◆ 全链路审计：操作留痕，技能用量透明
  ◆ 内置 TUI：Tab 补全 / Ctrl+T 思考 / 实时 token 统计
  ◆ 会话压缩：自动超窗压缩，摘要保留上下文
  ◆ 模型兼容：OpenAI 兼容端点，思考等级 auto/off/low/medium/high
  ◆ TUI 模式：Enter 发送 / Alt+Enter 换行 / Esc 取消 / Ctrl+T 思考等级
  ◆ 命令行模式：Enter 发送（多行输入请用 TUI 模式: mizar -tui）
  ◆ 鼠标：Shift+拖拽 选择复制 ｜ 滚轮滚动 ｜ PgUp/PgDn 翻页
`, version)
	if startupHint != "" {
		b += startupHint + "\n"
	}
	return b
}

// interactive 运行交互式对话（readline 支持：退格删除 / 历史上下键 / Tab 补全）。
// 多行输入：Alt+Enter 换行，Ctrl+J 提交（防粘贴时每行直接发送）。
func interactive(a *agent.Agent, st *session.Store, sessionID string) {
	rl := liner.NewLiner()
	defer rl.Close()
	rl.SetCtrlCAborts(true)
	// 多行模式：Enter 插入换行而非提交（需 Ctrl+J 提交）
	rl.SetMultiLineMode(true)

	// 渲染已加载的历史消息
	for _, msg := range a.Initial {
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
		// 找到最后一个 @
		// 但 @cmd: / @tool: / @file: 要特殊处理
		if idx := strings.LastIndex(line, "@"); idx >= 0 {
			after := strings.TrimSpace(line[idx+1:])
			res = completeAtRaw(after, cmdNames, toolNames)
			return
		}
		// 普通命令补全
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
	for {
		line, err := rl.Prompt(">")
		if err != nil {
			fmt.Println()
			return // EOF / Ctrl-C
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rl.AppendHistory(line)

		// 斜杠命令分发
		if handled, out, err := a.Commands.Dispatch(line); handled {
			if err != nil {
				fmt.Printf("命令错误: %v\n", err)
			}
			if out != "" {
				fmt.Println(out)
			}
			continue
		}

		// 处理 @file: / @cmd: / @tool: 引用，解析成实际输入
		input := resolveAtRef(line, cmdNames, toolNames)

		// LLM 调用（流式）：先显示等待提示（spinner + 宣传语），模型回复时逐 token 输出内容；
		// 纯工具任务（无回复文本流）完成后统一 Markdown 渲染最终回复。
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
				// 首个内容分片：清除等待提示行
				fmt.Print("\r\x1b[K")
			}
			// 逐 token 增量追加显示（只打印新增部分，避免重复）
			fmt.Print(inc)
		}
		reply, err := a.Run(input)
		a.OnLLMStream = nil
		close(spinnerStop)
		if err != nil {
			fmt.Printf("\r\x1b[K错误: %v\n", err)
			continue
		}
		if !streamed.Load() {
			// 纯工具任务：清除等待提示行后渲染最终回复
			fmt.Print("\r\x1b[K")
			rendered, renderErr := renderer.Render(reply)
			if renderErr != nil {
				// 渲染失败回退到纯文本
				fmt.Println(reply)
			} else {
				fmt.Print(rendered)
				if !strings.HasSuffix(rendered, "\n") {
					fmt.Println()
				}
			}
		} else {
			// 流式内容已逐 token 输出，补一个换行收尾
			fmt.Println()
		}

		if sessionID != "" {
			st.Append(sessionID, agent.Message{Role: agent.RoleUser, Content: line})
			st.Append(sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
		}
	}
}
