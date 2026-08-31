package builtins

import (
	"fmt"
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
	out, err := toolEdit(nil).Run(args)
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
	if _, err := toolEdit(nil).Run(args); err != nil {
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
	_, err := toolEdit(nil).Run(args)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got: %v", err)
	}
}

// P1-1: read 双限截断——超 2000 行截断 + 续读提示
func TestReadTruncatesLines(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "big.txt")
	var sb strings.Builder
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&sb, "line-%d\n", i)
	}
	if err := os.WriteFile(f, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolRead().Run(`{"path": "` + f + `"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "Use offset=") {
		t.Fatalf("want truncate hint, got: %s", out[:200])
	}
	if !strings.Contains(out, "line-0") || !strings.Contains(out, "line-1999") {
		t.Fatalf("first 2000 lines missing")
	}
	if strings.Contains(out, "line-2000") {
		t.Fatalf("should not include line 2000")
	}
}

// P1-1: read 字节截断——50KB 大文件只保留前 50KB
func TestReadTruncatesBytes(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "big.bin.txt")
	// 120KB 单行（无换行，行数不会超限，走字节限）
	content := strings.Repeat("x", 120_000)
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolRead().Run(`{"path": "` + f + `"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("want truncate hint, got: %s", out[:200])
	}
}

// P1-1: read offset 续读能拿到后续行
func TestReadOffsetContinue(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	var sb strings.Builder
	for i := 0; i < 2100; i++ {
		fmt.Fprintf(&sb, "line-%d\n", i)
	}
	if err := os.WriteFile(f, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := toolRead().Run(`{"path": "` + f + `", "offset": 2001, "limit": 100}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "line-2001") {
		t.Fatalf("offset continue missing: %s", out[:300])
	}
}

// P1-1: 超过 10MB 的大文件拒绝全量读（防内存不可预估消耗）
func TestReadRejectsHugeFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "huge.txt")
	// 11MB 稀疏文件（不实际写 11MB，用 Truncate 创建空洞文件）
	fh, err := os.Create(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(11 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	_, err = toolRead().Run(`{"path": "` + f + `"}`)
	if err == nil {
		t.Fatalf("want size-limit error")
	}
	if !strings.Contains(err.Error(), "10MB") {
		t.Fatalf("err: %v", err)
	}
}

// P1-2: bash 输出截断——超过 50KB 落盘 temp 并回传路径
func TestBashOutputTruncates(t *testing.T) {
	out, err := toolBash().Run(`{"command": "head -c 120000 /dev/zero | tr '\\0' 'x'"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "Full output:") {
		t.Fatalf("want truncate + temp path, got: %s", out[:300])
	}
	// 提取 temp 路径验证存在
	idx := strings.Index(out, "Full output: ")
	if idx < 0 {
		t.Fatalf("no temp path")
	}
	path := strings.TrimSpace(out[idx+len("Full output: "):])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file missing: %v", err)
	}
	os.Remove(path)
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
