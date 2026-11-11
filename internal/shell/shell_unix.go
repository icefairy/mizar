//go:build !windows

package shell

import (
	"os"
	"os/exec"

	"mizar/internal/caps"
)

// envShellSpec 读取 MIZAR_SHELL 环境变量（未设置返回空）。
func envShellSpec() string { return os.Getenv("MIZAR_SHELL") }

// candidates 非 Windows：按推荐顺序返回已安装的 shell。
//
// 两级探测：
//  1. PATH 内（bash → sh）：常规桌面/服务器环境。
//  2. PATH 外固定目录：su -c / Magisk / KernelSU / Termux / 精简容器常把 PATH
//     重置成极简集甚至空，此时 /system/bin/sh 明明存在却 LookPath 不到，
//     bash 工具会报「executable file not found」。这一层按绝对路径兜底，
//     覆盖 Android 与 root 场景。
func candidates() []Shell {
	var out []Shell
	seen := map[string]bool{}
	add := func(p, name string) {
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return
		}
		seen[p] = true
		out = append(out, postProcess(withArgs(p, name)))
	}

	// 1. PATH 内
	for _, name := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			add(p, name)
		}
	}

	// 2. PATH 外：Android / root / Termux 固定路径兜底。
	//    - /system/bin/sh         Android mksh（4.0+ 恒存在）
	//    - /data/adb/magisk/busybox、ksu、apatch 的 busybox
	//    - Termux 的 bash（$PREFIX/bin）
	for _, dir := range caps.Get().ExtraBins {
		// busybox 类多合一二进制的名字是 busybox / 也可能是被软链成 sh
		for _, cand := range []string{"ash", "sh", "mksh", "bash", "busybox"} {
			p := dir + string(os.PathSeparator) + cand
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				add(p, cand)
			}
		}
	}
	return out
}

// postProcess 非 Windows 无需编码前导。
func postProcess(s Shell) Shell { return s }

// fallback 兜底：优先用绝对路径 /system/bin/sh（Android），否则 sh 让 exec 报清晰错误。
func fallback() Shell {
	for _, p := range []string{"/system/bin/sh", "/bin/sh", "/usr/bin/sh"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return Shell{Path: p, Args: []string{"-c"}, Name: "sh"}
		}
	}
	return Shell{Path: "sh", Args: []string{"-c"}, Name: "sh"}
}
