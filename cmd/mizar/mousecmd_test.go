package main

import (
	"strings"
	"testing"
)

// TestRenderMouseStatus 状态文案随开关变化，并带切换用法。
func TestRenderMouseStatus(t *testing.T) {
	on := renderMouseStatus(true)
	if !strings.Contains(on, "已开启") || !strings.Contains(on, "滚轮") {
		t.Fatalf("开启态文案异常: %s", on)
	}
	off := renderMouseStatus(false)
	if !strings.Contains(off, "已关闭") {
		t.Fatalf("关闭态文案异常: %s", off)
	}
	for _, want := range []string{"/mouse on", "/mouse off", "MIZAR_MOUSE"} {
		if !strings.Contains(on, want) {
			t.Fatalf("缺少 %q: %s", want, on)
		}
	}
}

// TestMouseCommandFlow 查看 / on / off / toggle / 非法参数。
func TestMouseCommandFlow(t *testing.T) {
	t.Cleanup(func() { SetMouseEnabled(true) })

	// 查询
	cmd := newMouseCommand(nil)
	out, err := cmd.Run("")
	if err != nil || !strings.Contains(out, "鼠标上报") {
		t.Fatalf("查询失败: out=%q err=%v", out, err)
	}

	// 关闭
	if _, err := cmd.Run("off"); err != nil {
		t.Fatalf("off 失败: %v", err)
	}
	if MouseEnabled() {
		t.Fatal("off 后应为关闭")
	}

	// 开启
	if _, err := cmd.Run("on"); err != nil {
		t.Fatalf("on 失败: %v", err)
	}
	if !MouseEnabled() {
		t.Fatal("on 后应为开启")
	}

	// toggle
	if _, err := cmd.Run("toggle"); err != nil {
		t.Fatalf("toggle 失败: %v", err)
	}
	if MouseEnabled() {
		t.Fatal("toggle 后应反转成关闭")
	}

	// 非法参数
	if _, err := cmd.Run("sideways"); err == nil {
		t.Fatal("非法参数应报错")
	}
}

// TestMouseCommandPersist 切换成功应回调 persist。
func TestMouseCommandPersist(t *testing.T) {
	t.Cleanup(func() { SetMouseEnabled(true) })
	var got []bool
	cmd := newMouseCommand(func(on bool) error { got = append(got, on); return nil })
	if _, err := cmd.Run("off"); err != nil {
		t.Fatalf("off 失败: %v", err)
	}
	if _, err := cmd.Run("on"); err != nil {
		t.Fatalf("on 失败: %v", err)
	}
	if len(got) != 2 || got[0] != false || got[1] != true {
		t.Fatalf("persist 回调异常: %v", got)
	}
}
