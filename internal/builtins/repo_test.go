package builtins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestToolRepoMapDefault 默认参数（cwd + maxFiles=50）。
func TestToolRepoMapDefault(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Setenv("CWD", cwd)

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)
	os.Mkdir(filepath.Join(tmpDir, "sub"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "sub", "foo.ts"), []byte("const x=1;"), 0644)

	tool := toolRepoMap()
	raw, _ := json.Marshal(map[string]interface{}{"path": tmpDir})
	out, err := tool.Run(string(raw))
	if err != nil {
		t.Fatalf("repo_map: %v", err)
	}
	// 应包含文件名和路径
	if out == "" {
		t.Fatal("empty output")
	}
}

// TestToolRepoMapNoArgs 空 args 使用 cwd。
func TestToolRepoMapNoArgs(t *testing.T) {
	tool := toolRepoMap()
	// 空字符串 → 用 cwd
	out, err := tool.Run(``)
	if err != nil {
		t.Fatalf("repo_map empty: %v", err)
	}
	if out == "" {
		t.Fatal("empty output")
	}
}
