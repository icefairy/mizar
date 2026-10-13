package builtins

// bash 工具增量输出（对齐 pi 0.73.0 "Bash tool output now appears while commands run"）。
//
// 背景：长命令（构建、测试、npm install）此前要等结束才一次性返回，
// 用户界面长时间无输出，容易误判卡死。这里把 stdout/stderr 边产生边回调给 UI。
//
// 设计：
//   - 累计：完整内容仍写入内部 buffer，工具返回值与截断语义不变
//   - 节流：回调按时间窗口合并，避免每个字节都触发一次 UI 重绘
//   - 可选：回调为 nil（Server/CI）时退化为纯累计，行为与改动前一致

import (
	"strings"
	"sync"
	"time"
)

// bashStreamMinInterval 回调节流窗口。
const bashStreamMinInterval = 80 * time.Millisecond

// bashStreamFn 全局回调：由 TUI 设置（与 ask_user_question 同样的注入方式）。
// 工具读的是包级全局，而非 Tool 字段——保持与现有 agentAskUser 一致的模式。
var (
	bashStreamMu sync.RWMutex
	bashStream   func(chunk string)
)

// SetBashStream 设置 bash 增量输出回调（由 TUI/调用方注入；nil = 关闭）。
func SetBashStream(fn func(chunk string)) {
	bashStreamMu.Lock()
	bashStream = fn
	bashStreamMu.Unlock()
}

// bashStreamFn 读取当前回调（可能为 nil）。
func bashStreamFn() func(string) {
	bashStreamMu.RLock()
	defer bashStreamMu.RUnlock()
	return bashStream
}

// streamWriter 同时承担 io.Writer（喂给 cmd.Stdout/Stderr）与节流回调。
// 并发安全：exec 的 stdout/stderr 可能由不同 goroutine 写入。
type streamWriter struct {
	mu       sync.Mutex
	buf      strings.Builder
	pending  strings.Builder // 尚未回调出去的内容
	onChunk  func(string)
	interval time.Duration
	last     time.Time
}

func newStreamWriter(onChunk func(string), interval time.Duration) *streamWriter {
	return &streamWriter{onChunk: onChunk, interval: interval}
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if w.onChunk != nil {
		w.pending.Write(p)
		now := time.Now()
		// interval <= 0 表示不节流（测试用）
		if w.interval <= 0 || now.Sub(w.last) >= w.interval {
			w.flushLocked()
		}
	}
	return len(p), nil
}

// Flush 把尚未回调的内容送出去（命令结束时调用，保证尾部输出不丢）。
func (w *streamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *streamWriter) flushLocked() {
	if w.onChunk == nil || w.pending.Len() == 0 {
		return
	}
	chunk := w.pending.String()
	w.pending.Reset()
	w.last = time.Now()
	// 回调可能在 UI 线程外执行，自行负责线程安全
	w.onChunk(chunk)
}

// String 返回累计的完整输出（与改动前 strings.Builder 行为一致）。
func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
