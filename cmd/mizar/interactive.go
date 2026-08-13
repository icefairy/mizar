package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterh/liner"

	"mizar/internal/agent"
	"mizar/internal/session"
)

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// banner 启动标语（开阳 = Mizar 的项目代号）。
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
`, version)
}

// interactive 运行交互式对话（readline 支持：退格删除 / 历史上下键 / Tab 补全）。
func interactive(a *agent.Agent, st *session.Store, sessionID string) {
	rl := liner.NewLiner()
	defer rl.Close()
	rl.SetCtrlCAborts(true)
	// Tab 补全：/ 开头的命令 + 历史
	cmds := a.Commands.List()
	names := make([]string, 0, len(cmds))
	for _, c := range cmds {
		names = append(names, "/"+c.Name)
	}
	names = append(names, "/help")
	rl.SetCompleter(func(line string) (res []string) {
		// @ 路径补全：@路径 → 自动提示文件/文件夹
		if idx := strings.LastIndex(line, "@"); idx >= 0 {
			prefix := strings.TrimSpace(line[idx+1:])
			for _, c := range completeAtPath(prefix) {
				if strings.HasPrefix(c, prefix) {
					res = append(res, "@"+c)
				}
			}
			return
		}
		// 普通命令补全
		for _, n := range names {
			if strings.HasPrefix(n, line) {
				res = append(res, n)
			}
		}
		return
	})

	fmt.Print(banner())
	fmt.Println("输入任务，空行退出。Ctrl-D 或 /quit 退出。Tab 补全命令，↑↓ 历史。")
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

// completeAtPath 根据输入的前缀提示文件/文件夹路径。
// 支持相对路径和绝对路径：
//   - prefix = ""       → 无补全（空前缀）
//   - prefix = "src"    → 从当前工作目录匹配 src* 的条目
//   - prefix = "src/"   → 列出当前工作目录/src/ 下的所有条目
//   - prefix = "./src"  → 同上（相对路径）
//   - prefix = "/etc/"  → 列出 /etc/ 下的所有条目
//
// 文件夹返回时带 / 后缀。返回最多 100 个候选。
func completeAtPath(prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}

	// 拆分路径：取已存在的父目录 + 当前输入的文件名片段
	parent := filepath.Dir(prefix)
	name := filepath.Base(prefix)

	// 解析当前工作目录
	cdir, err := os.Getwd()
	if err != nil {
		return nil
	}

	// 将 parent 解析为绝对路径
	if parent == "" || parent == "." {
		parent = cdir
	} else if !filepath.IsAbs(parent) {
		parent = filepath.Join(cdir, parent)
	}

	// 打开父目录
	es, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}

	var result []string
	for _, e := range es {
		entryName := e.Name()
		if name != "" && !strings.HasPrefix(entryName, name) {
			continue
		}
		entryPath := filepath.Join(parent, entryName)
		if e.IsDir() {
			entryPath = entryPath + "/"
		}
		result = append(result, entryPath)
	}
	if len(result) > 100 {
		result = result[:100]
	}
	return result
}
