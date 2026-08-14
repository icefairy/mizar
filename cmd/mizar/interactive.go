package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/peterh/liner"

	"mizar/internal/agent"
	"mizar/internal/session"
)

func jsonUnmarshal(s string, v any) error {
	return fmt.Errorf("jsonUnmarshal is unused; use json.Unmarshal directly")
}

// banner 启动标语。
func banner() string {
	return fmt.Sprintf(`
  __  ___      __  ___        __
 /  |/  /_ ___/ /_/ _ \___ __/ /__
/ /|_/ / // / __/ // / -_) \ / (_-<
/_/  /_/\_,_/\__/\___/\__/_//_/___/
              %s — 开阳 · 自举式 AI Agent

  ◆ 单文件二进制，零依赖安装（无依赖地狱）
  ◆ 完全离线可用：私有模型网关，数据不出内网
  ◆ 插件沙箱：goja 隔离执行，TS/JS 双写，热重载
  ◆ 全链路可审计：每步工具调用留痕，技能用量透明
  ◆ 轻量自举：一个可执行文件跑通 规划→执行→验证

  Tab 补全：/ 命令 | @cmd: 命令 | @tool: 工具 | @file: 文件
  输入任务，空行退出。Ctrl-D 或 /quit 退出。↑↓ 历史。
`, version)
}

// interactive 运行交互式对话（readline 支持：退格删除 / 历史上下键 / Tab 补全）。
func interactive(a *agent.Agent, st *session.Store, sessionID string) {
	rl := liner.NewLiner()
	defer rl.Close()
	rl.SetCtrlCAborts(true)

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
		line, err := rl.Prompt("> ")
		if err != nil {
			fmt.Println()
			return // EOF / Ctrl-C
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return
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

		// LLM 调用
		reply, err := a.Run(input)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			continue
		}

		// Markdown 渲染输出
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

		if sessionID != "" {
			st.Append(sessionID, agent.Message{Role: agent.RoleUser, Content: line})
			st.Append(sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
		}
	}
}