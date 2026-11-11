// Package shell 统一「用字符串命令调用系统 shell」的方式。
//
// 背景：mizar 的 bash 工具、goal 门禁、`!` 前缀命令此前都硬编码 `bash -c` / `sh -c`，
// 在 Windows（用户以 PowerShell 启动、机器又没装 WSL/Git-Bash 时）会直接
// exec: "bash": executable file not found，AI 侧表现为「命令全挂」。
//
// 本包按平台选择 shell：
//   - Windows：pwsh.exe（PowerShell 7）→ powershell.exe（Windows PowerShell 5.1）→ cmd.exe
//   - 其它：bash → sh
//
// 选择优先级（从高到低）：
//  1. 运行期手动切换（/shell 命令，或启动时从 config 的 shell 字段恢复）
//  2. 环境变量 MIZAR_SHELL（一次性覆盖，适合 CI / 临时试）
//  3. 平台自动探测（上面的默认顺序）
//
// 参数形态按可执行文件名推断：
//
//	powershell/pwsh → -NoProfile -NonInteractive -EncodedCommand
//	cmd             → /D /C
//	其它(bash/sh/zsh/…) → -c
package shell

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf16"
)

// Shell 描述一个用于执行命令字符串的 shell。
type Shell struct {
	// Path 可执行文件路径（LookPath 结果或用户显式指定的名字）。
	Path string
	// Args 固定参数，命令字符串追加在最后。
	Args []string
	// Name 展示名（bash / PowerShell / pwsh / cmd）。
	Name string
	// Prelude 在真正命令前拼接的前导语句（用于统一输出编码等）。
	Prelude string
	// Postfix 在命令之后追加的语句（用于把原生程序退出码传回 PowerShell）。
	Postfix string
	// Encoded 为 true 时把命令编码成 PowerShell -EncodedCommand 需要的
	// base64(UTF-16LE)，避免命令中的引号/换行/中文在 Windows 参数转义层被破坏。
	Encoded bool
}

// String 返回便于展示与日志的形式，如 "bash -c"、"PowerShell -Command"。
func (s Shell) String() string {
	flag := "-c"
	switch s.Name {
	case "pwsh", "PowerShell":
		flag = "-Command"
	case "cmd":
		flag = "/C"
	}
	return s.Name + " " + flag
}

// IsPowerShell 当前 shell 是否 PowerShell（模型据此写 PowerShell 语法）。
func (s Shell) IsPowerShell() bool {
	return s.Name == "PowerShell" || s.Name == "pwsh"
}

// Hint 返回给模型看的命令行环境说明（写系统提示用）。
func (s Shell) Hint() string {
	switch s.Name {
	case "pwsh", "PowerShell":
		return "当前命令执行器是 " + s.String() + "（PowerShell 语法：用 ; 分隔语句、$env:VAR 读环境变量、" +
			"管道传对象、无 `ls -la` 这类 POSIX 参数；不要用 bash 专有语法）"
	case "cmd":
		return "当前命令执行器是 cmd.exe（批处理语法：用 & 或 && 串联、%VAR% 读环境变量、dir 列目录；" +
			"不支持 PowerShell 的 cmdlet 与 bash 语法）"
	}
	return "当前命令执行器是 " + s.String() + "（可用 POSIX 语法）"
}

// ---------------------------------------------------------------------------
// 选择状态：自动探测结果缓存 + 运行期手动覆盖
// ---------------------------------------------------------------------------

var (
	mu       sync.Mutex
	cached   *Shell
	override string // /shell 或 config 指定的 shell（空=自动）
)

// Meta 全局触发点：main 启动读 config 后调用 Init 注入持久化选择。
// Init 失败（配置里的 shell 已卸载）时静默回退自动探测，不阻断启动。
func Init(spec string) {
	spec = strings.TrimSpace(spec)
	if spec == "" || isAuto(spec) {
		SetOverrideQuiet("")
		return
	}
	if _, err := SetOverride(spec); err != nil {
		SetOverrideQuiet("")
	}
}

// isAuto 判断是否为「恢复自动」关键字。
func isAuto(spec string) bool {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "auto", "default", "reset", "detect":
		return true
	}
	return false
}

// Current 返回当前生效的 shell（只探测一次并缓存，手动切换时失效重算）。
func Current() Shell {
	mu.Lock()
	if cached != nil {
		s := *cached
		mu.Unlock()
		return s
	}
	spec := override
	mu.Unlock()

	resolved := resolve(spec)

	mu.Lock()
	if cached == nil { // 并发下只保留第一次结果，但 override 变化会清缓存
		c := resolved
		cached = &c
	}
	s := *cached
	mu.Unlock()
	return s
}

// Override 返回当前手动指定的 shell（空=自动探测）。
func Override() string {
	mu.Lock()
	defer mu.Unlock()
	return override
}

// SetOverride 手动指定 shell：spec 可为名字（powershell/pwsh/cmd/bash/sh/zsh…）
// 或可执行文件路径；"auto"（也接受 ""/reset/default）表示恢复自动探测。
// 指定的 shell 找不到时返回错误且不改变当前选择。
func SetOverride(spec string) (Shell, error) {
	spec = strings.TrimSpace(spec)
	if isAuto(spec) {
		spec = ""
	}
	var picked Shell
	if spec != "" {
		s, ok := lookupSpec(spec)
		if !ok {
			return Shell{}, fmt.Errorf("找不到 shell %q（可用: %s）", spec, strings.Join(CandidateNames(), " / "))
		}
		picked = s
	}
	mu.Lock()
	override = spec
	cached = nil
	if spec != "" {
		s := picked
		cached = &s
	}
	mu.Unlock()
	if spec == "" {
		return Current(), nil
	}
	return picked, nil
}

// SetOverrideQuiet 同 SetOverride 但忽略错误（用于启动时恢复持久化配置）。
func SetOverrideQuiet(spec string) {
	_, _ = SetOverride(spec)
}

// resolve 计算当前应使用的 shell（spec 为已读出的运行期覆盖值，避免重复加锁）。
// 优先级：运行期覆盖 → MIZAR_SHELL 环境变量 → 平台自动探测 → 兜底。
func resolve(spec string) Shell {
	if spec != "" {
		if s, ok := lookupSpec(spec); ok {
			return s
		}
	}
	if env := EnvOverride(); env != "" {
		if s, ok := lookupSpec(env); ok {
			return s
		}
	}
	if list := candidates(); len(list) > 0 {
		return list[0]
	}
	return fallback()
}

// EnvOverride 返回 MIZAR_SHELL 环境变量值（未设置时为空）。
func EnvOverride() string { return envShellSpec() }

// Candidates 返回本机可用（已安装）的 shell 列表，按平台推荐顺序。
// 用于 /shell 无参数时展示可选清单。
func Candidates() []Shell { return candidates() }

// CandidateNames 返回候选 shell 的短名字（提示用）。
func CandidateNames() []string {
	list := candidates()
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	return names
}

// CommandArgs 返回用本 shell 执行 command 字符串时的完整参数列表
// （固定参数 + 可选前导 + 命令 [+ 可选后置]，PowerShell 走 EncodedCommand）。
// 供需要临时使用「非当前」shell 的调用方（如 /shell 的探针命令）使用。
func (s Shell) CommandArgs(command string) []string {
	return s.argsWithCommand(command)
}

// argsWithCommand 拼接固定参数 + （可选前导 +）命令字符串（+ 可选后置）。
// Prelude 只在写 stdout 编码兼容语句时存在（Windows PowerShell / cmd）。
func (s Shell) argsWithCommand(command string) []string {
	full := command
	if s.Prelude != "" {
		full = s.Prelude + " " + command
	}
	if s.Postfix != "" {
		full = full + "\n" + s.Postfix
	}
	args := make([]string, 0, len(s.Args)+1)
	args = append(args, s.Args...)
	if s.Encoded {
		args = append(args, encodePowerShell(full))
	} else {
		args = append(args, full)
	}
	return args
}

// encodePowerShell 把脚本编码为 base64(UTF-16LE)，供 -EncodedCommand 使用。
// 为什么不用 -Command：powershell.exe 走 Windows 命令行参数转义，命令里含
// 双引号、换行、中文或 `&` 时极易被拆坏（Go 的 argv 转义与 PowerShell 的
// 二次解析规则不同）。EncodedCommand 把脚本整体 base64 传递，彻底绕开转义。
func encodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(u)*2)
	for _, r := range u {
		buf = append(buf, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// Command 构造用当前 shell 执行 command 字符串的命令。
func Command(command string) *exec.Cmd {
	s := Current()
	return exec.Command(s.Path, s.argsWithCommand(command)...)
}

// CommandContext 同 Command，但绑定 context（取消即杀进程）。
func CommandContext(ctx context.Context, command string) *exec.Cmd {
	s := Current()
	return exec.CommandContext(ctx, s.Path, s.argsWithCommand(command)...)
}

// ---------------------------------------------------------------------------
// CLIXML 清理
// ---------------------------------------------------------------------------

// clixmlErrRe 匹配 PowerShell 把错误流重定向到管道时的 CLIXML 片段。
var clixmlErrRe = regexp.MustCompile(`(?s)<S S="Error">(.*?)</S>`)

// xmlEscapeRe 匹配 PowerShell CLIXML 里的 _xXXXX_ 转义。
var xmlEscapeRe = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)

// CleanOutput 清理 PowerShell 特有的输出噪声，让模型看到纯文本。
//
// 背景：powershell.exe 的 stderr 被重定向到管道时（Go 里就是 pipe），错误记录
// 会序列化成 CLIXML（`#< CLIXML\n<Objs ...><S S="Error">…</S>`），夹在正常输出里
// 既难读又占 token。这里把 CLIXML 块里的错误文本抽出来、反转义成普通多行文本。
// 非 PowerShell 或没有 CLIXML 时原样返回。
func CleanOutput(s string) string {
	if s == "" {
		return s
	}
	idx := strings.Index(s, "#< CLIXML")
	if idx < 0 {
		return s
	}
	head := s[:idx]
	tail := s[idx:]
	var b strings.Builder
	b.WriteString(head)
	for _, m := range clixmlErrRe.FindAllStringSubmatch(tail, -1) {
		msg := xmlEscapeRe.ReplaceAllStringFunc(m[1], func(esc string) string {
			var code int
			if _, err := fmt.Sscanf(esc[2:6], "%04x", &code); err != nil {
				return esc
			}
			return string(rune(code))
		})
		msg = strings.TrimRight(msg, "\r\n")
		if msg == "" {
			continue
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("ERROR: " + msg + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------------------------------------------------------------------------
// 名字解析（跨平台共用）
// ---------------------------------------------------------------------------

// lookupSpec 解析用户给定的 shell 规格（名字或路径）。
// 先按原样 LookPath；失败再尝试补 .exe；仍失败则查短名别名表。
func lookupSpec(spec string) (Shell, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Shell{}, false
	}
	// 含路径分隔符：视为路径，直接构造（存在性交给 exec 报错，便于用户指向未入 PATH 的 exe）
	if strings.ContainsAny(spec, `/\`) {
		return postProcess(withArgs(spec, nameOf(spec))), true
	}
	cands := []string{spec}
	if !strings.HasSuffix(strings.ToLower(spec), ".exe") {
		cands = append(cands, spec+".exe")
	}
	if alias := aliasOf(spec); alias != "" {
		cands = append(cands, alias)
	}
	for _, c := range cands {
		if p, err := exec.LookPath(c); err == nil {
			return postProcess(withArgs(p, nameOf(p))), true
		}
	}
	return Shell{}, false
}

// aliasOf 短名/别名 → 可执行文件名（Windows 常见别名）。
func aliasOf(spec string) string {
	switch strings.ToLower(spec) {
	case "ps", "powershell":
		return "powershell.exe"
	case "pwsh":
		return "pwsh.exe"
	case "cmd", "command":
		return "cmd.exe"
	}
	return ""
}

// baseName 取文件名，同时兼容 Windows 反斜杠路径（即使在非 Windows 上
// 解析 Windows 风格路径，例如 MIZAR_SHELL=C:\...\pwsh.exe）。
func baseName(path string) string {
	return filepath.Base(strings.ReplaceAll(path, `\`, "/"))
}

// withArgs 按可执行文件名推断合适的参数。
func withArgs(path, name string) Shell {
	lower := strings.ToLower(strings.TrimSuffix(baseName(path), ".exe"))
	switch lower {
	case "pwsh", "powershell":
		return Shell{Path: path, Args: []string{"-NoProfile", "-NonInteractive", "-EncodedCommand"}, Name: name, Encoded: true}
	case "cmd":
		return Shell{Path: path, Args: []string{"/D", "/C"}, Name: name}
	default: // bash / sh / zsh / dash ...
		return Shell{Path: path, Args: []string{"-c"}, Name: name}
	}
}

// nameOf 返回展示名。
func nameOf(path string) string {
	base := strings.ToLower(strings.TrimSuffix(baseName(path), ".exe"))
	switch base {
	case "pwsh":
		return "pwsh"
	case "powershell":
		return "PowerShell"
	case "cmd":
		return "cmd"
	}
	return base
}
