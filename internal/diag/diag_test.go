package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Init 后写日志应能在文件里读到带标签的行。
func TestInitAndLogf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MIZAR_LOG_DIR", dir)
	Init(true)
	defer Close()

	if !Enabled() {
		t.Fatal("Init(true) 后应处于启用状态")
	}
	Logf("tui", "启动测试 %d", 42)

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("日志目录应有一个文件, err=%v entries=%d", err, len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "[tui  ]") {
		t.Fatalf("日志应含标签 [tui  ], 实际: %q", got)
	}
	if !strings.Contains(got, "启动测试 42") {
		t.Fatalf("日志应含格式化内容, 实际: %q", got)
	}
	if !strings.Contains(Path(), dir) {
		t.Fatalf("Path() 应指向临时目录, 实际: %q", Path())
	}
}

// 显式关闭后不应再写入任何内容（也不应 panic）。
func TestDisabledNoWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MIZAR_LOG_DIR", dir)
	Init(false)
	defer Close()

	if Enabled() {
		t.Fatal("Init(false) 后不应处于启用状态")
	}
	Logf("agent", "不应出现")
	if Path() != "" {
		t.Fatalf("未启用时 Path() 应为空, 实际: %q", Path())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("未启用时不应创建文件, 实际: %d 个", len(entries))
	}
}
