package main

import (
	"strings"

	"mizar/internal/builtins"
)

// bash 增量输出的 TUI 接线（对齐 pi 0.73.0：命令运行中就能看到输出）。
//
// 数据流：bash 工具 stdout/stderr → builtins.streamWriter（80ms 节流）
//   → 本文件注册的回调（agent goroutine）→ bashMu 保护的内存缓冲
//   → QueueUpdateDraw 触发事件循环重绘 → renderAllDirect 渲染尾部若干行。
//
// 只保留尾部：长命令可能产生大量输出，全量保存会占内存，UI 也只关心最新进展。

// bashLiveTailLines 实时块显示的尾部行数。
const bashLiveTailLines = 12

// bashLiveMaxBytes 实时缓冲上限（超出后只保留尾部，防止内存无界增长）。
const bashLiveMaxBytes = 64 * 1024

// setBashLive 注册/注销 bash 增量输出回调。
// on=false 时注销并清空缓冲（任务结束调用，避免残留内容影响下一轮渲染）。
func (m *tuiModel) setBashLive(on bool) {
	if !on {
		builtins.SetBashStream(nil)
		m.bashMu.Lock()
		m.bashLive = ""
		m.bashLiveOn = false
		m.bashMu.Unlock()
		return
	}
	builtins.SetBashStream(func(chunk string) {
		m.bashMu.Lock()
		m.bashLiveOn = true
		m.bashLive += chunk
		if len(m.bashLive) > bashLiveMaxBytes {
			m.bashLive = m.bashLive[len(m.bashLive)-bashLiveMaxBytes:]
		}
		m.bashMu.Unlock()
		// 节流已在 streamWriter 侧完成（80ms），这里直接请求重绘
		m.app.QueueUpdateDraw(func() { m.renderAllDirect() })
	})
}

// clearBashLive 清空实时缓冲（每次新任务开始时调用）。
func (m *tuiModel) clearBashLive() {
	m.bashMu.Lock()
	m.bashLive = ""
	m.bashLiveOn = false
	m.bashMu.Unlock()
}

// bashLiveActive 是否有命令正在运行（供状态栏/渲染判断）。
func (m *tuiModel) bashLiveActive() bool {
	m.bashMu.Lock()
	defer m.bashMu.Unlock()
	return m.bashLiveOn
}

// tailLines 返回文本末尾 n 行；行数不足时返回全部。
// 超长行会被截断，避免单行撑爆聊天区。
func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		lines[i] = truncateRunesPlain(l, 200)
	}
	return strings.Join(lines, "\n")
}

// truncateRunesPlain 按 rune 截断（不添加省略号以外的修饰）。
func truncateRunesPlain(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
