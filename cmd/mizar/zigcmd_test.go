package main

import (
	"os"
	"strings"
	"testing"
)

// isolateZigCache 把释放目录重定向到临时目录。
// 原因：go test 会并行跑不同包，若本包测试去动真实的
// ~/.cache/mizar/zig，会和 internal/zigboot 的释放测试互相踩
// （实测：Clean 撞上正在解压的 .tmp 目录 → "directory not empty"）。
func isolateZigCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MIZAR_ZIG_CACHE", dir)
}

// TestZigCommandStatus /zig 无参返回状态摘要，不报错。
func TestZigCommandStatus(t *testing.T) {
	isolateZigCache(t)
	c := newZigCommand()
	if c.Name != "zig" {
		t.Fatalf("命令名 = %q, want zig", c.Name)
	}
	if c.Timeout <= 0 {
		t.Fatal("解压大归档需要正超时")
	}
	out, err := c.Run("")
	if err != nil {
		t.Fatalf("/zig 报错: %v", err)
	}
	_ = os.Getenv("MIZAR_ZIG_CACHE")
	if !strings.Contains(out, "Zig") {
		t.Fatalf("状态输出异常: %q", out)
	}
}

// TestZigCommandPath /zig path 未释放时给出提示而非报错。
func TestZigCommandPath(t *testing.T) {
	isolateZigCache(t)
	c := newZigCommand()
	out, err := c.Run("path")
	if err != nil {
		t.Fatalf("/zig path 报错: %v", err)
	}
	if out == "" {
		t.Fatal("/zig path 输出为空")
	}
}

// TestZigCommandUnknown 未知子命令应报错并提示用法。
func TestZigCommandUnknown(t *testing.T) {
	isolateZigCache(t)
	c := newZigCommand()
	_, err := c.Run("nonsense")
	if err == nil {
		t.Fatal("未知子命令应报错")
	}
	if !strings.Contains(err.Error(), "用法") {
		t.Fatalf("错误信息应含用法提示: %v", err)
	}
}

// TestZigCommandClean /zig clean 正常返回。
func TestZigCommandClean(t *testing.T) {
	isolateZigCache(t)
	c := newZigCommand()
	if _, err := c.Run("clean"); err != nil {
		t.Fatalf("/zig clean 报错: %v", err)
	}
}
