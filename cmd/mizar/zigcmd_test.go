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

// TestZigCommandNewMCPScaffold /zig new-mcp 生成可编译的 MCP server 骨架。
func TestZigCommandNewMCPScaffold(t *testing.T) {
	isolateZigCache(t)
	dir := t.TempDir()
	proj := dir + "/myserver"
	out, err := newZigCommand().Run("new-mcp " + proj)
	if err != nil {
		t.Fatalf("new-mcp 报错: %v", err)
	}
	if !strings.Contains(out, "热加载") || !strings.Contains(out, "/mcp reload") {
		t.Errorf("脚手架说明应讲清热加载真相:\n%s", out)
	}
	if _, err := os.Stat(proj + "/server.zig"); err != nil {
		t.Fatalf("未生成 server.zig: %v", err)
	}
	// 二次调用不覆盖
	out2, err := newZigCommand().Run("new-mcp " + proj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "--force") {
		t.Errorf("应提示 --force: %s", out2)
	}
}

// TestZigCommandNewWasmScaffold /zig new-wasm 生成 wasm 内核骨架，内核名合法化。
func TestZigCommandNewWasmScaffold(t *testing.T) {
	isolateZigCache(t)
	dir := t.TempDir()
	proj := dir + "/my-kernel"
	out, err := newZigCommand().Run("new-wasm " + proj)
	if err != nil {
		t.Fatalf("new-wasm 报错: %v", err)
	}
	if !strings.Contains(out, "wasm_invoke(\"my_kernel\", \"my_kernel\"") {
		t.Errorf("内核名应合法化为 my_kernel:\n%s", out)
	}
	if _, err := os.Stat(proj + "/kernel.zig"); err != nil {
		t.Fatalf("未生成 kernel.zig: %v", err)
	}
}

// TestZigCommandNewMissingArg 缺少目录参数应报错。
func TestZigCommandNewMissingArg(t *testing.T) {
	isolateZigCache(t)
	if _, err := newZigCommand().Run("new-mcp"); err == nil {
		t.Fatal("缺目录应报错")
	}
}
