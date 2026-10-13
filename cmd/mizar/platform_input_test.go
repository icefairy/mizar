package main

import (
	"testing"
	"time"
)

// TestShouldEnableMouseFor Windows 默认禁用鼠标上报（避免 ConPTY 把鼠标事件
// 双重编码成字符泄漏进输入框），MIZAR_MOUSE=1 可强制开启。
func TestShouldEnableMouseFor(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{"windows 默认关闭", "windows", nil, false},
		{"windows WT 下关闭", "windows", map[string]string{"WT_SESSION": "xxx"}, false},
		{"windows MIZAR_MOUSE=1 强制开启", "windows", map[string]string{"MIZAR_MOUSE": "1"}, true},
		{"windows MIZAR_MOUSE=0 强制关闭", "windows", map[string]string{"MIZAR_MOUSE": "0"}, false},
		{"linux 默认开启", "linux", nil, true},
		{"darwin 默认开启", "darwin", nil, true},
		{"linux MIZAR_MOUSE=0 强制关闭", "linux", map[string]string{"MIZAR_MOUSE": "0"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := func(k string) string { return c.env[k] }
			if got := shouldEnableMouseFor(c.goos, getenv); got != c.want {
				t.Fatalf("shouldEnableMouseFor(%s, %v) = %v, want %v", c.goos, c.env, got, c.want)
			}
		})
	}
}

// TestIsPasteNewline 粘贴突发识别：毫秒级连发的 Enter 视为粘贴换行（不提交），
// 人手打字间隔（≥30ms）视为用户提交。
func TestIsPasteNewline(t *testing.T) {
	base := time.Now()
	cases := []struct {
		name string
		last time.Time
		now  time.Time
		want bool
	}{
		{"零值（无前序按键）不算粘贴", time.Time{}, base, false},
		{"1ms 间隔 → 粘贴", base, base.Add(1 * time.Millisecond), true},
		{"7ms 间隔 → 粘贴", base, base.Add(7 * time.Millisecond), true},
		{"10ms 间隔 → 用户输入", base, base.Add(10 * time.Millisecond), false},
		{"150ms 间隔（人手打字）→ 用户输入", base, base.Add(150 * time.Millisecond), false},
		{"时钟回拨不算粘贴", base, base.Add(-5 * time.Millisecond), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPasteNewline(c.now, c.last); got != c.want {
				t.Fatalf("isPasteNewline = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPasteGuardEnabled 环境变量开关：默认开启，MIZAR_PASTE_GUARD=0 关闭。
func TestPasteGuardEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true},
		{"1", true},
		{"0", false},
		{"off", false},
		{"false", false},
	}
	for _, c := range cases {
		getenv := func(string) string { return c.val }
		if got := pasteGuardEnabledFor(getenv); got != c.want {
			t.Fatalf("pasteGuardEnabledFor(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}
