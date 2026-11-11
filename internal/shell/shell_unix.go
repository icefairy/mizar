//go:build !windows

package shell

import (
	"os"
	"os/exec"
)

// envShellSpec 读取 MIZAR_SHELL 环境变量（未设置返回空）。
func envShellSpec() string { return os.Getenv("MIZAR_SHELL") }

// candidates 非 Windows：按推荐顺序返回已安装的 shell（bash → sh）。
func candidates() []Shell {
	var out []Shell
	for _, name := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, postProcess(withArgs(p, name)))
		}
	}
	return out
}

// postProcess 非 Windows 无需编码前导。
func postProcess(s Shell) Shell { return s }

// fallback 兜底：直接用 sh，让 exec 报出清晰错误。
func fallback() Shell { return Shell{Path: "sh", Args: []string{"-c"}, Name: "sh"} }
