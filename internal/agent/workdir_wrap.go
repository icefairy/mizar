// workdir 工具包装（C-8 配套）：让子代理的 working_dir 真正生效。
//
// 背景：delegate_task 的 working_dir 此前只写入子 Agent 的 WorkDir（系统提示
// 锚点），但 bash / read / write / grep / find / ls / repo_map 这些工具始终按
// 进程 cwd 解析相对路径——所以「在独立 worktree 里干活」实际仍落在主工作区。
//
// 做法：子代理注册工具时按工作目录包一层适配器（不改全局工具实现，避免影响
// 主代理与其他并发会话）：
//   - bash：命令前加 `cd <dir> && ...`（用 shell 语义，跨平台由 bash 工具自身
//     的 shell 决定；路径用双引号包裹，Windows PowerShell 也接受 cd "..."）
//   - 路径类工具（read/write/edit/grep/find/ls/repo_map）：path 为空或相对
//     路径时拼到 <dir> 之下；绝对路径原样保留。
package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
)

// workDirPathTools 需要把相对路径锚到工作目录的工具名。
var workDirPathTools = map[string]bool{
	"read":     true,
	"write":    true,
	"edit":     true,
	"grep":     true,
	"find":     true,
	"ls":       true,
	"repo_map": true,
}

// wrapToolsForWorkDir 返回按 dir 适配的工具列表副本（dir 为空则原样返回）。
func wrapToolsForWorkDir(tools []plugins.Tool, dir string) []plugins.Tool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return tools
	}
	out := make([]plugins.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, wrapOneToolForWorkDir(t, dir))
	}
	return out
}

func wrapOneToolForWorkDir(t plugins.Tool, dir string) plugins.Tool {
	if t.Run == nil {
		return t
	}
	inner := t.Run
	switch {
	case t.Name == "bash":
		t.Run = func(args string) (string, error) {
			return inner(prefixBashDir(args, dir))
		}
	case workDirPathTools[t.Name]:
		t.Run = func(args string) (string, error) {
			return inner(joinPathArg(args, dir))
		}
	}
	return t
}

// prefixBashDir 给 bash 参数里的 command 前置 `cd "<dir>" && `。
// 解析失败（非法 JSON 或空命令）原样返回，交给工具自身报错。
func prefixBashDir(args, dir string) string {
	var p struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Command) == "" {
		return args
	}
	// 已经显式导览过目录（以 cd 开头）就尊重模型意图，不重复前缀。
	trimmed := strings.TrimSpace(p.Command)
	if strings.HasPrefix(trimmed, "cd ") || strings.HasPrefix(trimmed, "cd\t") {
		return args
	}
	p.Command = `cd "` + escapeForDoubleQuote(dir) + `" && ` + p.Command
	b, err := json.Marshal(p)
	if err != nil {
		return args
	}
	return string(b)
}

// joinPathArg 把路径类工具的 path 参数锚到 dir（空=dir，相对=join，绝对=原样）。
func joinPathArg(args, dir string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return args
	}
	var cur string
	if v, ok := raw["path"]; ok {
		_ = json.Unmarshal(v, &cur)
	}
	abs := cur
	if abs == "" {
		abs = dir
	} else if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, abs)
	}
	b, err := json.Marshal(abs)
	if err != nil {
		return args
	}
	raw["path"] = b
	out, err := json.Marshal(raw)
	if err != nil {
		return args
	}
	return string(out)
}

// escapeForDoubleQuote 转义会破坏双引号 shell 字符串的字符（保守：仅处理 " 与 \）。
func escapeForDoubleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
