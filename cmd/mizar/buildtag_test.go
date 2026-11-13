package main

// buildTag / versionString 的回归测试。
//
// 背景：Windows 出两份产物（win10-native / win7-compat），构建标识通过
// -ldflags -X main.buildTag=... 注入。现场排查时用户只会报「我是 Win7 那个包」，
// 若 -version 不带标识，就得靠包名猜 —— 这个测试锁住「标识必须出现在版本串里」。

import (
	"strings"
	"testing"
)

func TestVersionStringWithoutBuildTag(t *testing.T) {
	oldTag := buildTag
	defer func() { buildTag = oldTag }()
	buildTag = ""

	// 非 Windows 产物（Linux 两条链产物一致，不需要区分）。
	if got := versionString(); got != version {
		t.Fatalf("无 buildTag 时应只返回版本号，得到 %q", got)
	}
}

func TestVersionStringWithBuildTag(t *testing.T) {
	oldTag := buildTag
	defer func() { buildTag = oldTag }()

	for _, tag := range []string{"win10-native", "win7-compat"} {
		buildTag = tag
		got := versionString()
		if !strings.HasPrefix(got, version) {
			t.Fatalf("buildTag=%s: 版本串应以 %q 开头，得到 %q", tag, version, got)
		}
		if !strings.Contains(got, tag) {
			t.Fatalf("buildTag=%s: 版本串应包含构建标识，得到 %q", tag, got)
		}
		// 括号形式固定，便于用户/脚本 grep（如 `mizar -version | grep win7-compat`）。
		if want := version + " (" + tag + ")"; got != want {
			t.Fatalf("buildTag=%s: 期望 %q，得到 %q", tag, want, got)
		}
	}
}
