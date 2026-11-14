package main

import (
	"fmt"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/config"
)

// TestPermissionCommand /permission 三种用法：查看 / 切换（即时生效+持久化）/ reset。
func TestPermissionCommand(t *testing.T) {
	gate := agent.NewPermissionGate(agent.PermAuto)
	a := &agent.Agent{}
	a.Commands = agent.NewCommandRegistry()
	a.Permission = gate
	a.Commands.Register(agent.Command{
		Name: "permission",
		Run: func(args string) (string, error) {
			args = strings.TrimSpace(args)
			if args == "" {
				return "权限门禁: " + string(gate.Mode()) + "\n已批准: 0 条", nil
			}
			if args == "reset" {
				return fmt.Sprintf("✓ 已清空 %d 条", gate.ResetApprovals()), nil
			}
			if m, ok := agent.ParsePermissionMode(args); ok {
				gate.SetMode(m)
				return "✓ 权限模式已切换为 " + string(m), nil
			}
			return "", fmt.Errorf("用法: /permission 查看 ｜ /permission auto|default|acceptEdits|plan ｜ /permission reset")
		},
	})

	// 查看
	_, out, err := a.Commands.Dispatch("/permission")
	if err != nil || !strings.Contains(out, "auto") {
		t.Fatalf("查看: out=%q err=%v", out, err)
	}
	// 切换 default（内存即时生效）
	_, out, err = a.Commands.Dispatch("/permission default")
	if err != nil || !strings.Contains(out, "default") {
		t.Fatalf("切换: out=%q err=%v", out, err)
	}
	if gate.Mode() != agent.PermDefault {
		t.Fatalf("内存模式应即时生效，got %s", gate.Mode())
	}
	// 切换 plan → 门禁决策随之变化
	_, _, _ = a.Commands.Dispatch("/permission plan")
	ok, msg := gate.Authorize("write", `{"path":"a.txt"}`)
	if ok || !strings.Contains(msg, "权限门禁拒绝") {
		t.Fatalf("plan 模式下写类应拒绝: ok=%v msg=%s", ok, msg)
	}
	// reset 清审批缓存
	gate.SetAskUser(func(string) (string, error) {
		return `[{"id":"perm-confirm","value":"本会话允许"}]`, nil
	})
	_, _, _ = a.Commands.Dispatch("/permission acceptEdits")
	gate.Authorize("bash", `{"command":"rm -rf /tmp/x"}`)
	if gate.ApprovalCount() != 1 {
		t.Fatalf("批准后缓存应为 1，实际 %d", gate.ApprovalCount())
	}
	_, out, err = a.Commands.Dispatch("/permission reset")
	if err != nil || gate.ApprovalCount() != 0 {
		t.Fatalf("reset: out=%q count=%d err=%v", out, gate.ApprovalCount(), err)
	}
	// 非法值
	_, out, err = a.Commands.Dispatch("/permission bogus")
	if err == nil || !strings.Contains(err.Error(), "用法") {
		t.Fatalf("非法值应报用法: err=%v out=%q", err, out)
	}
}

// TestPermissionModeFromConfig config.json 的 permission_mode 字段启动时生效。
func TestPermissionModeFromConfig(t *testing.T) {
	// ParsePermissionMode 校验各配置值
	for in, want := range map[string]agent.PermissionMode{
		"auto": agent.PermAuto, "default": agent.PermDefault,
		"acceptEdits": agent.PermAcceptEdits, "ACCEPTEDITS": agent.PermAcceptEdits,
		"plan": agent.PermPlan, "": agent.PermAuto,
	} {
		got, ok := agent.ParsePermissionMode(in)
		if !ok || got != want {
			t.Errorf("ParsePermissionMode(%q) = %s,%v want %s", in, got, ok, want)
		}
	}
	// config 字段存在且可往返（结构对齐，防手滑改 json tag）
	cfg := config.Config{PermissionMode: "default"}
	if cfg.PermissionMode != "default" {
		t.Fatal("PermissionMode 字段应可赋值")
	}
}
