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
	"path/filepath"
	"regexp"
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
			"/zig path 查路径 · /zig clean 清缓存 · " +
			"/zig new-mcp <dir> 生成 MCP server 骨架（形态B）· /zig new-wasm <dir> 生成 wasm 内核骨架（形态A）",
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

			case "new-mcp":
				dir, force, err := newProjectArg(fields[1:], "zig-mcp")
				if err != nil {
					return "", err
				}
				p, exists, err := zigboot.NewMCPServer(dir)
				if err != nil {
					return "", err
				}
				if exists && !force {
					return fmt.Sprintf("已存在 %s（加 --force 覆盖）\n如需覆盖: /zig new-mcp %s --force", p, dir), nil
				}
				if exists && force {
					if _, _, err := zigboot.NewMCPServer(dir); err != nil {
						return "", err
					}
				}
				bin := filepath.Join(dir, "server")
				zig := zigboot.ZigBinary()
				if st, e := os.Stat(zig); e != nil || st.IsDir() {
					zig = "zig" // 未释放内嵌工具链，退回 PATH 上的 zig
				}
				return fmt.Sprintf(`✓ 已生成 Zig MCP server 骨架（形态 B）：%s

下一步：
  1) 编辑 %s，在 callTool() 里加你的工具（改完记得同步 tools_json 清单）
  2) 编译（zig 已自动加进 bash 的 PATH，可直接执行）：
       zig build-exe %s -OReleaseFast -femit-bin=%s
     （跨架构：加 -target aarch64-linux）
  3) 注册进 Mizar：
       /mcp add %s %s
  4) 验证：/mcp test %s

热加载真相：MCP server 是独立子进程，Zig 二进制本身无法在进程内被替换；
但改完源码重新编译覆盖同一个可执行文件后，执行 /mcp reload 就会杀掉旧子进程、
按新路径重新拉起——所以改 Zig 代码后是「重编译 + /mcp reload」，不需要重启 Mizar。
而插件（.ts）才是真正热加载的：写入插件目录即自动重载。`,
					p, p, p, bin, filepath.Base(dir), bin, filepath.Base(dir)), nil

			case "new-wasm":
				dir, force, err := newProjectArg(fields[1:], "zig-wasm")
				if err != nil {
					return "", err
				}
				p, exists, err := zigboot.NewWasmKernel(dir)
				if err != nil {
					return "", err
				}
				if exists && !force {
					return fmt.Sprintf("已存在 %s（加 --force 覆盖）\n如需覆盖: /zig new-wasm %s --force", p, dir), nil
				}
				if exists && force {
					if _, _, err := zigboot.NewWasmKernel(dir); err != nil {
						return "", err
					}
				}
				name := wasmKernelName(filepath.Base(dir))
				return fmt.Sprintf(`✓ 已生成 Zig wasm 内核骨架（形态 A）：%s

下一步：
  1) 编辑 %s：把 export fn %s(...) 里的算法换成你的逻辑
     （契约：export fn <名>(in_ptr: [*]const u8, in_len: usize) u32，
       返回响应头地址，头内容是 [out_ptr, out_len] 两个 u32）
  2) 编译成 wasm（一条命令）：
       zig build-exe %s -target wasm32-freestanding -OReleaseFast \
         --name %s -fno-entry -rdynamic -femit-bin=%s
  3) 内嵌进 Mizar 二进制需要重新构建（把 .wasm 放 internal/wasmkern/ 后 make zig-kernels）
  4) 插件里调用：
       wasm_invoke("%s", "%s", "输入文本")
     宿主会自动探测内核是否可用；插件侧务必用 typeof wasm_kernels 做降级兜底。`,
					p, p, name, p, name, filepath.Join(dir, name+".wasm"), name, name), nil

			default:
				return "", fmt.Errorf("未知子命令: %s\n用法: /zig [init [--force] | path | clean | new-mcp <dir> | new-wasm <dir>]", sub)
			}
		},
	}
}

// newProjectArg 解析 new-mcp/new-wasm 的目录参数与 --force 开关。
func newProjectArg(args []string, fallback string) (dir string, force bool, err error) {
	var rest []string
	for _, f := range args {
		if f == "--force" || f == "-f" {
			force = true
			continue
		}
		rest = append(rest, f)
	}
	if len(rest) == 0 {
		return "", false, fmt.Errorf("缺少目录参数\n用法: /zig new-mcp <目录> [--force]")
	}
	if len(rest) > 1 {
		return "", false, fmt.Errorf("目录参数只能有一个（含空格的路径请加引号）")
	}
	d, absErr := filepath.Abs(rest[0])
	if absErr != nil {
		return rest[0], force, nil
	}
	_ = fallback
	return d, force, nil
}

var identRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// wasmKernelName 由目录名推导合法 Zig 函数名/内核名（非法字符转下划线，数字开头补 k）。
func wasmKernelName(dir string) string {
	n := identRe.ReplaceAllString(dir, "_")
	n = strings.Trim(n, "_")
	if n == "" {
		n = "kernel"
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "k_" + n
	}
	return n
}

// registerZigCommand 在 Agent 初始化时挂上 /zig。
func registerZigCommand(a *agent.Agent) {
	if a == nil || a.Commands == nil {
		return
	}
	a.Commands.Register(newZigCommand())
}
