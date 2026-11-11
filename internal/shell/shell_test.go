package shell

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodePS 把 -EncodedCommand 的 base64 还原为脚本文本。
func decodePS(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("EncodedCommand 不是合法 base64: %v", err)
	}
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u = append(u, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// TestCurrentNonEmpty 当前平台必须能选出 shell（含兜底）。
func TestCurrentNonEmpty(t *testing.T) {
	s := Current()
	if s.Path == "" {
		t.Fatalf("Current() 返回空 Path")
	}
	if len(s.Args) == 0 {
		t.Fatalf("Current() 无固定参数: %+v", s)
	}
	if s.Hint() == "" {
		t.Fatalf("Hint() 为空")
	}
}

// TestCommandCarriesScript 命令字符串确实进入最终参数（PowerShell 走 base64）。
func TestCommandCarriesScript(t *testing.T) {
	c := Command("echo hi")
	if len(c.Args) == 0 {
		t.Fatal("no args")
	}
	last := c.Args[len(c.Args)-1]
	if Current().Encoded {
		if !strings.Contains(decodePS(t, last), "echo hi") {
			t.Fatalf("解码后不含命令: %q", last)
		}
		return
	}
	if !strings.Contains(last, "echo hi") {
		t.Fatalf("命令字符串不在最后: %v", c.Args)
	}
}

// TestWithArgsWindowsPowerShell PowerShell 参数与编码标记正确（不依赖真实安装）。
func TestWithArgsWindowsPowerShell(t *testing.T) {
	s := withArgs(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "PowerShell")
	if !s.IsPowerShell() {
		t.Fatal("IsPowerShell 应为 true")
	}
	if s.Args[len(s.Args)-1] != "-EncodedCommand" || !s.Encoded {
		t.Fatalf("PowerShell 应使用 -EncodedCommand: %+v", s)
	}
	if s.String() != "PowerShell -Command" {
		t.Fatalf("String=%q", s.String())
	}
}

// TestWithArgsPwsh pwsh 同样走 EncodedCommand。
func TestWithArgsPwsh(t *testing.T) {
	s := withArgs(`C:\Program Files\PowerShell\7\pwsh.exe`, "pwsh")
	if s.Args[len(s.Args)-1] != "-EncodedCommand" || !s.Encoded {
		t.Fatalf("pwsh 参数异常: %+v", s)
	}
}

// TestWithArgsCmd cmd 用 /D /C。
func TestWithArgsCmd(t *testing.T) {
	s := withArgs(`C:\Windows\System32\cmd.exe`, "cmd")
	if len(s.Args) != 2 || s.Args[0] != "/D" || s.Args[1] != "/C" {
		t.Fatalf("cmd 参数异常: %v", s.Args)
	}
	if s.Encoded {
		t.Fatal("cmd 不应使用 Encoded")
	}
}

// TestWithArgsBash bash 用 -c。
func TestWithArgsBash(t *testing.T) {
	s := withArgs("/bin/bash", "bash")
	if len(s.Args) != 1 || s.Args[0] != "-c" {
		t.Fatalf("bash 参数异常: %v", s.Args)
	}
}

// TestEncodedScriptIncludesPrelude 编码模式下前导语句与命令一起进入脚本，且顺序正确。
func TestEncodedScriptIncludesPrelude(t *testing.T) {
	// postProcess 在 Unix 上是 no-op，这里手动注入一个前导验证拼接顺序。
	ps := withArgs("pwsh.exe", "pwsh")
	ps.Prelude = "Set-StrictMode -Version Latest;"
	ps.Postfix = "if ($true) { exit 0 }"
	args := ps.argsWithCommand("Write-Output hi")
	script := decodePS(t, args[len(args)-1])
	if !strings.Contains(script, "Write-Output hi") {
		t.Fatalf("命令丢失: %q", script)
	}
	if !strings.Contains(script, "Set-StrictMode") {
		t.Fatalf("编码前导丢失: %q", script)
	}
	if strings.Index(script, "Write-Output hi") < strings.Index(script, "Set-StrictMode") {
		t.Fatalf("命令应在编码前导之后: %q", script)
	}
	if strings.Index(script, "Write-Output hi") > strings.Index(script, "exit 0") {
		t.Fatalf("后置语句应在命令之后: %q", script)
	}
}

// TestEncodePowerSplashQuotes 含引号/中文/换行的脚本能无损往返。
func TestEncodePowerSplashQuotes(t *testing.T) {
	src := "Write-Output \"a b\";\nWrite-Output '中文'\nif ($true) { \"x&y\" }"
	if got := decodePS(t, encodePowerShell(src)); got != src {
		t.Fatalf("往返不一致:\n in=%q\nout=%q", src, got)
	}
}

// TestHitActualShell 真实跑一次命令，验证 echo 正常返回。
func TestHitActualShell(t *testing.T) {
	var cmdStr string
	switch Current().Name {
	case "pwsh", "PowerShell":
		cmdStr = "Write-Output mizar-shell-ok"
	case "cmd":
		cmdStr = "echo mizar-shell-ok"
	default:
		cmdStr = "echo mizar-shell-ok"
	}
	out, err := Command(cmdStr).CombinedOutput()
	if err != nil {
		t.Fatalf("%s 执行失败: %v\n%s", runtime.GOOS, err, out)
	}
	if !strings.Contains(string(out), "mizar-shell-ok") {
		t.Fatalf("输出不含预期内容: %q", out)
	}
}

// TestCleanOutputCLIXML PowerShell 错误流 CLIXML 应还原成可读文本。
func TestCleanOutputCLIXML(t *testing.T) {
	in := "正常输出\n#< CLIXML\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\">" +
		"<S S=\"Error\">git : 不是内部或外部命令_x000D__x000A_</S>" +
		"<S S=\"Error\">At line:1 char:1_x000D__x000A_</S></Objs>"
	got := CleanOutput(in)
	if strings.Contains(got, "CLIXML") || strings.Contains(got, "<S S=") {
		t.Fatalf("CLIXML 未被清理: %q", got)
	}
	if !strings.Contains(got, "正常输出") || !strings.Contains(got, "不是内部或外部命令") {
		t.Fatalf("内容丢失: %q", got)
	}
	if !strings.Contains(got, "ERROR:") {
		t.Fatalf("缺 ERROR 前缀: %q", got)
	}
}

// TestCleanOutputPassthrough 无 CLIXML 时原样返回。
func TestCleanOutputPassthrough(t *testing.T) {
	in := "hello\nworld"
	if got := CleanOutput(in); got != in {
		t.Fatalf("应原样返回: %q", got)
	}
	if got := CleanOutput(""); got != "" {
		t.Fatalf("空串应返回空: %q", got)
	}
}

// TestSetOverrideAndAuto 手动指定与恢复自动。
func TestSetOverrideAndAuto(t *testing.T) {
	cands := Candidates()
	if len(cands) == 0 {
		t.Skip("本机无可用 shell")
	}
	t.Cleanup(func() { SetOverride("") })

	target := cands[len(cands)-1]
	got, err := SetOverride(target.Name)
	if err != nil {
		t.Fatalf("SetOverride(%q) 失败: %v", target.Name, err)
	}
	if Current().Path != got.Path && Current().Name != got.Name {
		t.Fatalf("Current 未跟随切换: %+v vs %+v", Current(), got)
	}
	if Override() == "" {
		t.Fatal("Override 应为非空")
	}

	// auto 恢复
	if _, err := SetOverride("auto"); err != nil {
		t.Fatalf("auto 失败: %v", err)
	}
	if Override() != "" {
		t.Fatalf("auto 后 Override 应为空: %q", Override())
	}
}

// TestSetOverrideUnknownKeepsPrevious 未知 shell 报错且保持原选择。
func TestSetOverrideUnknownKeepsPrevious(t *testing.T) {
	t.Cleanup(func() { SetOverride("") })
	before := Current()
	if _, err := SetOverride("no-such-shell-abc"); err == nil {
		t.Fatal("期望报错")
	}
	if Current().Path != before.Path {
		t.Fatal("失败时不应改变当前 shell")
	}
}

// TestSetOverrideByPath 含路径分隔符的规格直接构造（不要求文件存在）。
func TestSetOverrideByPath(t *testing.T) {
	t.Cleanup(func() { SetOverride("") })
	got, err := SetOverride(`C:\Tools\Git\bin\bash.exe`)
	if err != nil {
		t.Fatalf("路径规格应被接受: %v", err)
	}
	if got.Name != "bash" || len(got.Args) != 1 || got.Args[0] != "-c" {
		t.Fatalf("按文件名推断参数失败: %+v", got)
	}
}

// TestAliasWindows 别名与 .exe 后缀推断。
func TestAliasWindows(t *testing.T) {
	cases := map[string]string{
		`pwsh.exe`:                               "-EncodedCommand",
		`C:\Program Files\PowerShell\7\pwsh.exe`: "-EncodedCommand",
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: "-EncodedCommand",
		`C:\Windows\System32\cmd.exe`:                               "/C",
		`/usr/bin/bash`:                                             "-c",
	}
	for path, want := range cases {
		s := withArgs(path, nameOf(path))
		if s.Args[len(s.Args)-1] != want {
			t.Fatalf("%s: 期望末位参数 %q，实际 %v", path, want, s.Args)
		}
	}
}

// TestCommandArgsMatchesCommand CommandArgs 与 Command 的参数一致。
func TestCommandArgsMatchesCommand(t *testing.T) {
	s := Current()
	cmd := Command("echo hi")
	want := s.CommandArgs("echo hi")
	// exec.Command 会把程序路径放在 Args[0]
	got := cmd.Args[1:]
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("参数不一致: %v vs %v", got, want)
	}
}

// TestWithArgsBusybox busybox 是多合一二进制，必须 `busybox sh -c`。
func TestWithArgsBusybox(t *testing.T) {
	s := withArgs("/data/adb/magisk/busybox", "busybox")
	if len(s.Args) != 2 || s.Args[0] != "sh" || s.Args[1] != "-c" {
		t.Fatalf("busybox 参数应为 [sh -c]，实际 %v", s.Args)
	}
	if s.Name != "busybox" {
		t.Fatalf("Name=%q", s.Name)
	}
	if s.String() != "busybox sh -c" {
		t.Fatalf("String=%q", s.String())
	}
}

// TestHintPOSIXShellWarnsBashism ash/mksh/sh 等非 bash shell 的提示要警告 bash 专有语法。
func TestHintPOSIXShellWarnsBashism(t *testing.T) {
	for _, name := range []string{"sh", "ash", "mksh", "dash"} {
		s := Shell{Path: "/system/bin/" + name, Args: []string{"-c"}, Name: name}
		h := s.Hint()
		if !strings.Contains(h, "不是 bash") {
			t.Fatalf("%s 的 Hint 未警告 bash 专有语法: %q", name, h)
		}
	}
	// bash 本身不该有这个警告
	b := Shell{Path: "/bin/bash", Args: []string{"-c"}, Name: "bash"}
	if strings.Contains(b.Hint(), "不是 bash") {
		t.Fatalf("bash 不应警告 bash 专有语法: %q", b.Hint())
	}
}

// TestCandidatesIncludeSystemSh /system/bin/sh 存在时必须出现在候选里（Android/su 场景）。
func TestCandidatesIncludeSystemSh(t *testing.T) {
	if _, err := os.Stat("/system/bin/sh"); err != nil {
		t.Skip("/system/bin/sh 不存在（非 Android）")
	}
	var found bool
	for _, c := range Candidates() {
		if c.Path == "/system/bin/sh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("候选未包含 /system/bin/sh: %+v", Candidates())
	}
}

// TestFallbackStable fallback 必须给出可执行路径（Android 优先 /system/bin/sh）。
func TestFallbackStable(t *testing.T) {
	f := fallback()
	if f.Path == "" || len(f.Args) == 0 {
		t.Fatalf("fallback 不完整: %+v", f)
	}
}
