package plugins

import (
	"strings"
	"testing"
	"time"

	"mizar/internal/engine"
)

// 工具 panic 必须被 Manager.Call 兜住 —— 这是"单个工具不能弄崩主程序"的最后一道闸。
func TestManagerCallRecoversPanic(t *testing.T) {
	m := NewManager(t.TempDir(), &engine.HostFuncs{})
	m.SetMaxExec(20 * time.Second)
	m.RegisterBuiltin(Tool{
		Name: "boom",
		Run:  func(args string) (string, error) { panic("第三方库炸了") },
	})
	m.RegisterBuiltin(Tool{
		Name: "nilmap",
		Run:  func(args string) (string, error) { var m map[string]int; m["x"] = 1; return "", nil },
	})
	m.RegisterBuiltin(Tool{
		Name: "ok",
		Run:  func(args string) (string, error) { return "fine", nil },
	})

	for _, name := range []string{"boom", "nilmap"} {
		out, err := m.Call(name, "{}")
		if err == nil {
			t.Fatalf("%s: panic 应转成 error", name)
		}
		if !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("%s: 错误信息应说明 panic: %v", name, err)
		}
		if out != "" {
			t.Errorf("%s: panic 后不应有输出: %q", name, out)
		}
	}

	if out, err := m.Call("ok", "{}"); err != nil || out != "fine" {
		t.Fatalf("正常工具应成功: %q %v", out, err)
	}
	if _, err := m.Call("nope", "{}"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("不存在的工具应报 not found: %v", err)
	}
}

// 超时必须返回 timeout 而不是挂死。
func TestManagerCallTimeout(t *testing.T) {
	m := NewManager(t.TempDir(), &engine.HostFuncs{})
	m.SetMaxExec(300 * time.Millisecond)
	m.RegisterBuiltin(Tool{
		Name: "slow",
		Run: func(args string) (string, error) {
			time.Sleep(3 * time.Second)
			return "done", nil
		},
	})
	if _, err := m.Call("slow", "{}"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("应返回 timeout，得到 %v", err)
	}
}

// 命令 panic 同样要兜住。
func TestManagerCallCommandRecoversPanic(t *testing.T) {
	m := NewManager(t.TempDir(), &engine.HostFuncs{})
	m.SetMaxExec(5 * time.Second)
	m.mu.Lock()
	m.commands["boomcmd"] = Command{
		Name: "boomcmd",
		Run:  func(args string) (string, error) { panic("命令实现炸了") },
	}
	m.mu.Unlock()
	if _, err := m.CallCommand("boomcmd", ""); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("命令 panic 应转成 error，得到 %v", err)
	}
}
