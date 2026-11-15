package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// askProjectTrust 询问用户是否信任当前项目的项目级资源（P-5 简化版）。
//
// 只在交互式终端（stdin 是终端）里提问；非交互环境（管道/CI/headless）
// 返回 false —— 按「不信任」处理，跳过项目级 AGENTS.md/.mizar/prompts，
// 不阻塞启动。问题打到 stderr，避免污染 stdout 的数据输出。
func askProjectTrust(resources []string) bool {
	if !isTerminal(os.Stdin) {
		return false
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "🔐 该项目包含项目级资源，首次使用需确认是否信任：")
	for _, r := range resources {
		fmt.Fprintf(os.Stderr, "   - %s\n", r)
	}
	fmt.Fprint(os.Stderr, "信任后这些文件会注入系统提示/注册命令。信任本项目？[y/N] ")
	return readYesNo(os.Stdin)
}

// readYesNo 读取一行，y/yes 返回 true（默认否）。
func readYesNo(r io.Reader) bool {
	br := bufio.NewReader(r)
	line, _ := br.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true
	default:
		return false
	}
}
