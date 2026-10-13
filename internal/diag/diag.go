// Package diag 提供轻量诊断日志：把关键事件落盘到 ~/.mizar/logs/，
// 用于排查「任务莫名中止/卡住/退出」这类 TUI 崩溃后无法复现的问题。
//
// 设计原则（最小改动、零依赖）：
//   - 默认开启（TUI 下 stderr 是终端，诊断信息不能往那里写，只能落盘）
//   - 写入失败绝不影响主流程（所有错误静默吞掉）
//   - 单文件按天轮转，超限自动截断，避免无限增长
//
// 开关：MIZAR_DIAG=0 关闭；MIZAR_LOG_DIR 覆盖目录（测试用）。
package diag

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxFileSize 单日日志上限（超过后停止写入，避免磁盘被撑爆）。
const maxFileSize = 8 << 20 // 8MB

var (
	mu      sync.Mutex
	file    *os.File
	path    string
	enabled bool
	size    int64
)

// Init 初始化诊断日志。enabled=false 时不写任何内容。
// 目录不可写等错误一律忽略（诊断日志不能影响主流程）。
func Init(en bool) {
	mu.Lock()
	defer mu.Unlock()
	// 重置状态：重复 Init（如测试或重新初始化）不应残留旧的路径与文件句柄
	if file != nil {
		_ = file.Close()
		file = nil
	}
	path, size, enabled = "", 0, false
	if !en {
		return
	}
	dir := os.Getenv("MIZAR_LOG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			enabled = false
			return
		}
		dir = filepath.Join(home, ".mizar", "logs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		enabled = false
		return
	}
	name := fmt.Sprintf("mizar-%s.log", time.Now().Format("20060102"))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		enabled = false
		return
	}
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	file = f
	path = f.Name()
	enabled = true
}

// Path 返回当前日志文件路径（未启用时为空）。
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Enabled 返回诊断日志是否启用。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Logf 写入一行诊断日志（带标签，便于 grep）。
// tag 约定：tui / agent / llm / panic。写入失败静默忽略。
func Logf(tag, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled || file == nil || size >= maxFileSize {
		return
	}
	line := fmt.Sprintf("%s [%-5s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), tag, fmt.Sprintf(format, args...))
	n, err := file.WriteString(line)
	if err != nil {
		enabled = false
		return
	}
	size += int64(n)
}

// Close 关闭日志文件（进程退出时调用）。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
	enabled = false
}
