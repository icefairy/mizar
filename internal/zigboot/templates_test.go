package zigboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMCPServerTemplate(t *testing.T) {
	dir := t.TempDir()
	p, exists, err := NewMCPServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("首次生成不应报已存在")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"tools/call", "tools/list", "initialize", "callTool"} {
		if !strings.Contains(s, want) {
			t.Errorf("模板缺少 %q", want)
		}
	}
	// 二次生成不覆盖
	_, exists2, err := NewMCPServer(dir)
	if err != nil || !exists2 {
		t.Fatalf("二次生成应返回 exists=true, err=%v exists=%v", err, exists2)
	}
}

func TestNewWasmKernelTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mykernel")
	p, exists, err := NewWasmKernel(dir)
	if err != nil || exists {
		t.Fatalf("err=%v exists=%v", err, exists)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "kernel_data_end") {
		t.Error("模板缺少通用 ABI 的 kernel_data_end")
	}
}
