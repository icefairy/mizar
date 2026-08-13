package builtins

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processAlive 判断 pid 进程是否存活
func processAlive(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil
}

// P0-1: 多编辑倒序应用——前面 edit 改变长度不应导致后面 edit 错配
func TestEditReverseOrder(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	content := "foo bar baz\n"
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// 两次 edit：bar→X（变短），baz→Y（变短），倒序应用后都不应错位
	args := `{"path": "` + f + `", "edits": [{"oldText": "bar", "newText": "X"}, {"oldText": "baz", "newText": "Y"}]}`
	out, err := toolEdit().Run(args)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "applied 2 edit(s)") {
		t.Fatalf("out: %s", out)
	}
	got, _ := os.ReadFile(f)
	want := "foo X Y\n"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// P0-1: 多编辑变长场景——前面 edit 变长也不应影响后面
func TestEditReverseOrderGrow(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	content := "a bb ccc\n"
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// a→AAAAA 变长、ccc→C 变短：倒序应用应正确
	args := `{"path": "` + f + `", "edits": [{"oldText": "a", "newText": "AAAAA"}, {"oldText": "ccc", "newText": "C"}]}`
	if _, err := toolEdit().Run(args); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, _ := os.ReadFile(f)
	want := "AAAAA bb C\n"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// P0-1: oldText 未找到时明确报错
func TestEditNotFound(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(f, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := `{"path": "` + f + `", "oldText": "nope", "newText": "x"}`
	_, err := toolEdit().Run(args)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got: %v", err)
	}
}

// P0-2: bash 超时 kill 整个进程树（含子进程）
func TestBashKillProcessTree(t *testing.T) {
	// 父进程 sleep 10 且 spawn 一个后台子进程写 pid 文件；超时 1s 后
	// 子进程也应被杀（不残留）
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	cmd := `sleep 10 & echo $! > ` + pidfile + `; wait`
	start := time.Now()
	_, err := toolBash().Run(`{"command": "` + cmd + `", "timeout": 1}`)
	if err == nil || !strings.Contains(err.Error(), "timeout after 1s") {
		t.Fatalf("want timeout error, got: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
	// 等 200ms 让进程组 kill 生效
	time.Sleep(200 * time.Millisecond)
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("child pid file missing: %v", err)
	}
	pid := strings.TrimSpace(string(b))
	alive := processAlive(pid)
	if alive {
		t.Fatalf("child process %s still alive after timeout (process group kill failed)", pid)
	}
}
