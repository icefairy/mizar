package main

import (
	"strings"
	"testing"

	"mizar/internal/shell"
)

// TestRenderShellList 展示内容包含当前 shell、选择来源、可选清单。
func TestRenderShellList(t *testing.T) {
	cur := shell.Current()
	cands := shell.Candidates()
	out := renderShellList(cur, "", cands, "")
	if !strings.Contains(out, "当前命令执行器") {
		t.Fatalf("缺少当前执行器: %s", out)
	}
	if !strings.Contains(out, cur.Name) {
		t.Fatalf("缺少 shell 名 %q: %s", cur.Name, out)
	}
	if !strings.Contains(out, "平台自动探测") {
		t.Fatalf("缺少来源说明: %s", out)
	}

	// 手动指定来源
	out2 := renderShellList(cur, "cmd", cands, "")
	if !strings.Contains(out2, "/shell 手动指定") {
		t.Fatalf("缺少手动来源: %s", out2)
	}
	// 环境变量来源
	out3 := renderShellList(cur, "", cands, "bash")
	if !strings.Contains(out3, "MIZAR_SHELL=bash") {
		t.Fatalf("缺少环境变量来源: %s", out3)
	}
}

// TestShellCommandList 无参数时只展示，不切换。
func TestShellCommandList(t *testing.T) {
	before := shell.Current()
	cmd := newShellCommand(nil, nil)
	out, err := cmd.Run("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "当前命令执行器") {
		t.Fatalf("输出异常: %s", out)
	}
	if shell.Current().Path != before.Path {
		t.Fatalf("无参数调用不应切换: %s → %s", before.Path, shell.Current().Path)
	}
}

// TestShellCommandAutoRestoresAuto 传 auto 应恢复自动探测（不写入配置时 persist=nil）。
func TestShellCommandAutoRestoresAuto(t *testing.T) {
	// 先切到一个确定存在的 shell，再 auto 回来
	cands := shell.Candidates()
	if len(cands) == 0 {
		t.Skip("本机无可用 shell")
	}
	cmd := newShellCommand(nil, nil)
	if _, err := cmd.Run(cands[len(cands)-1].Name); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if shell.Override() == "" {
		t.Fatal("切换后 override 应为非空")
	}
	out, err := cmd.Run("auto")
	if err != nil {
		t.Fatalf("auto 失败: %v", err)
	}
	if shell.Override() != "" {
		t.Fatalf("auto 后 override 应为空，实际 %q", shell.Override())
	}
	if !strings.Contains(out, "已恢复平台自动探测") {
		t.Fatalf("输出异常: %s", out)
	}
}

// TestShellCommandInvalidKeepsCurrent 指定不存在的 shell 应报错且不改变当前选择。
func TestShellCommandInvalidKeepsCurrent(t *testing.T) {
	before := shell.Current()
	cmd := newShellCommand(nil, nil)
	_, err := cmd.Run("definitely-not-a-shell-xyz")
	if err == nil {
		t.Fatal("不存在的 shell 应报错")
	}
	if shell.Current().Path != before.Path {
		t.Fatalf("失败时不应改变当前 shell: %s → %s", before.Path, shell.Current().Path)
	}
}

// TestShellCommandPersistAndReload 切换成功应回调 persist 与 onSwitch。
func TestShellCommandPersistAndReload(t *testing.T) {
	cands := shell.Candidates()
	if len(cands) == 0 {
		t.Skip("本机无可用 shell")
	}
	var persisted string
	persistCalled, switchCalled := false, false
	cmd := newShellCommand(
		func(spec string) error { persisted = spec; persistCalled = true; return nil },
		func() { switchCalled = true },
	)
	target := cands[0]
	if _, err := cmd.Run(target.Name); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if !persistCalled || persisted == "" {
		t.Fatalf("persist 应被调用且非空: called=%v spec=%q", persistCalled, persisted)
	}
	if !switchCalled {
		t.Fatal("onSwitch 应被调用")
	}
	t.Cleanup(func() { shell.SetOverride("") })
}

// TestParseShellTarget 路径含空格/带引号时也要解析出完整 shell 规格。
func TestParseShellTarget(t *testing.T) {
	cases := map[string]string{
		"cmd":                                 "cmd",
		"  pwsh  ":                            "pwsh",
		"auto":                                "auto",
		`C:\Program Files\Git\bin\bash.exe`:   `C:\Program Files\Git\bin\bash.exe`,
		`"C:\Program Files\Git\bin\bash.exe"`: `C:\Program Files\Git\bin\bash.exe`,
		`"C:\Program Files\Git\bin\bash.exe" extra`: `C:\Program Files\Git\bin\bash.exe`,
		"/usr/local/bin/bash":                       "/usr/local/bin/bash",
		`pwsh -NoProfile`:                           "pwsh",
	}
	for in, want := range cases {
		if got := parseShellTarget(in); got != want {
			t.Fatalf("parseShellTarget(%q)=%q, want %q", in, got, want)
		}
	}
}
