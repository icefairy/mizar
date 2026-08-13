package builtins

import (
	"strings"
	"testing"
	"time"
)

// 无 timeout：普通命令立即返回
func TestBashNoTimeoutNormal(t *testing.T) {
	out, err := toolBash().Run(`{"command": "echo hello"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("out: %s", out)
	}
}

// 无 timeout：长命令不被截断（对齐 pi：timeout 可选，无默认超时）
func TestBashNoTimeoutLongRuns(t *testing.T) {
	start := time.Now()
	out, err := toolBash().Run(`{"command": "sleep 1 && echo done-long"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("returned too fast")
	}
	if !strings.Contains(out, "done-long") {
		t.Fatalf("out: %s", out)
	}
}

// 显式 timeout：命令超过限制触发超时报错
func TestBashExplicitTimeout(t *testing.T) {
	start := time.Now()
	_, err := toolBash().Run(`{"command": "sleep 10", "timeout": 1}`)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1s") {
		t.Fatalf("err: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
}
