package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"mizar/internal/agent"
	"mizar/internal/session"
)

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// interactive 运行交互式对话。
func interactive(a *agent.Agent, st *session.Store, sessionID string) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("输入任务，空行退出。Ctrl-D 也可退出。")
	for {
		fmt.Print("\n> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		// 斜杠命令分发：插件注册的 command_* 与内置命令
		if handled, out, err := a.Commands.Dispatch(line); handled {
			if err != nil {
				fmt.Printf("命令错误: %v\n", err)
			}
			if out != "" {
				fmt.Println(out)
			}
			continue
		}
		reply, err := a.Run(line)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			continue
		}
		fmt.Println(reply)
		if sessionID != "" {
			st.Append(sessionID, agent.Message{Role: agent.RoleUser, Content: line})
			st.Append(sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
		}
	}
}
