package main

import (
	"fmt"
	"sync/atomic"
	"time"
)

// spinnerFrames 等待指示动画帧（Braille spinner）。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerLines 等待期间轮换展示的「开阳」特性宣传语（每 4 秒切换一条）。
var spinnerLines = []string{
	"零依赖 · 单文件二进制，拷进去就能跑",
	"自举 Agent · 让模型自己写插件，扩展自己",
	"插件热重载 · .ts 改完立即生效，无需重启",
	"会话压缩 · 超窗自动摘要，长对话不丢上下文",
	"思考等级可调 · auto / off / low / medium / high",
	"全链路审计 · 工具操作留痕，token 用量透明",
	"内置 TUI · 排队输入、实时统计、快速纠正",
	"MCP 工具 · 一个协议接入任意外部服务",
	"SKILL.md 技能 · 兼容第三方技能生态",
	"goja 沙箱 · 插件安全执行，运行时隔离",
	"离线部署 · 数据不出内网，适合私有化",
	"内置 bash/read/write · 开箱即用的编码能力",
}

// classicSpinner 经典（liner/readline）模式的等待提示：单行覆盖刷新
// （\r 回行首 + ANSI 清行），每 200ms 换一帧、每 4s 轮换一条「开阳」宣传语，
// 并累计显示已等待秒数。模型回复流开始后（streamed=true）自动退出，避免交错。
func classicSpinner(stop chan struct{}, streamed *atomic.Bool) {
	start := time.Now()
	lineStart := time.Now()
	lineIdx := 0
	i := 0
	for {
		select {
		case <-stop:
			return
		default:
		}
		if streamed.Load() {
			return
		}
		if time.Since(lineStart) >= 4*time.Second {
			lineStart = time.Now()
			lineIdx = (lineIdx + 1) % len(spinnerLines)
		}
		frame := spinnerFrames[i%len(spinnerFrames)]
		secs := int(time.Since(start).Seconds())
		fmt.Printf("\r\x1b[K%s %s · 已等待 %d 秒…", frame, spinnerLines[lineIdx], secs)
		i++
		time.Sleep(200 * time.Millisecond)
	}
}
