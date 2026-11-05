package main

// zigcmd.go —— 内置 /zig 命令：查看状态、释放内嵌 Zig 工具链、清理缓存。
//
// 背景：Mizar 走「C/Zig 编成独立二进制，再用 exec 运行」的路线时，
// 目标机不一定自带编译器。把官方 Zig 归档内嵌进二进制后，一条 /zig init
// 就能把工具链释放到本地缓存，之后用 bash 调 zig 即可现场编译，
// 无需联网、无需系统包管理器、不污染发行版 glibc 版本。
//
// 子命令：
//   /zig             # 状态摘要（版本/是否内嵌/是否已释放/缓存路径）
//   /zig init        # 释放到缓存（幂等；已释放直接返回路径）
//   /zig init --force# 强制重新释放
//   /zig path        # 打印 zig 可执行文件路径（未释放则提示）
//   /zig clean       # 删除运行时释放目录（不删 ~/.cache/zig 原始归档）

import (
	"fmt"
	"os"
	"strings"
	"time"

	"mizar/internal/agent"
	"mizar/internal/zigboot"
)

// newZigCommand 构造 /zig 命令。独立函数便于单测。
func newZigCommand() agent.Command {
	return agent.Command{
		Name: "zig",
		Description: "Zig 工具链管理：/zig 查状态 · /zig init 释放内嵌工具链到缓存 · " +
			"/zig path 查路径 · /zig clean 清缓存",
		// 解压 50MB+ 归档可能耗时数秒，给足超时。
		Timeout: 120 * time.Second,
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			fields := strings.Fields(args)
			sub := ""
			if len(fields) > 0 {
				sub = fields[0]
			}

			switch sub {
			case "":
				return zigboot.Status(), nil

			case "init":
				force := false
				for _, f := range fields[1:] {
					if f == "--force" || f == "-f" {
						force = true
					}
				}
				path, err := zigboot.Release(force)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("✓ Zig 工具链已就绪\n  路径: %s\n  缓存: %s\n\n用法: %s version", path, zigboot.CacheDir(), path), nil

			case "path":
				p := zigboot.ZigBinary()
				if st, err := os.Stat(p); err != nil || st.IsDir() {
					return fmt.Sprintf("Zig 尚未释放（%s 不存在）\n执行 /zig init 释放", p), nil
				}
				return p, nil

			case "clean":
				if err := zigboot.Clean(); err != nil {
					return "", fmt.Errorf("清理失败: %w", err)
				}
				return fmt.Sprintf("✓ 已清理运行时释放目录: %s\n（原始归档仍在 ~/.cache/zig，如需要可 scripts/fetch-zig.sh --clean）", zigboot.CacheDir()), nil

			default:
				return "", fmt.Errorf("未知子命令: %s\n用法: /zig [init [--force] | path | clean]", sub)
			}
		},
	}
}

// registerZigCommand 在 Agent 初始化时挂上 /zig。
func registerZigCommand(a *agent.Agent) {
	if a == nil || a.Commands == nil {
		return
	}
	a.Commands.Register(newZigCommand())
}
