package main

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// projectInfo 是状态栏第一行（项目行）展示的信息快照。
//
// 为什么缓存：状态栏在 spinner/流式输出/等待时每秒被重绘十几次，
// 若每次都跑 git 命令会把事件循环拖住（用户体感就是输入框发涩）。
// 采集只在启动时与任务结束时各做一次，其余时刻读缓存。
type projectInfo struct {
	Dir    string // 工作目录（只保留末两级，避免挤爆一行）
	Branch string // git 分支名；detached 时是短 SHA；非 git 仓库为空
	Dirty  int    // 未提交改动条数（git status --porcelain 行数），非仓库为 0
	IsGit  bool   // 是否在 git 仓库内
}

// shortDir 只保留路径末两级，过长路径不占满整行。
func shortDir(p string) string {
	trimmed := strings.TrimRight(p, "/\\")
	if trimmed == "" {
		// 根目录（"/" 或 "C:\\"）被 trim 成空：保留原始路径，否则项目行显示空白
		if p != "" {
			return p
		}
		return ""
	}
	p = trimmed
	parent := filepath.Base(filepath.Dir(p))
	base := filepath.Base(p)
	// 根目录（/ 或 C:\）：parent 与 base 相等或 parent 为 "."，只显示 base
	if parent == "." || parent == base || parent == string(filepath.Separator) {
		return base
	}
	return parent + "/" + base
}

// collectProjectInfo 采集一次项目信息（可能跑 git 命令，勿在事件循环高频调用）。
func collectProjectInfo(dir string) projectInfo {
	info := projectInfo{Dir: shortDir(dir)}
	if dir == "" {
		return info
	}
	// 分支：rev-parse --abbrev-ref HEAD（detached 时返回 HEAD，再用短 SHA 兜底）
	if out, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		info.IsGit = true
		branch := strings.TrimSpace(out)
		if branch == "HEAD" {
			if sha, err2 := runGit(dir, "rev-parse", "--short", "HEAD"); err2 == nil {
				branch = strings.TrimSpace(sha)
			}
		}
		info.Branch = branch
	}
	if info.IsGit {
		if out, err := runGit(dir, "status", "--porcelain"); err == nil {
			out = strings.TrimSpace(out)
			if out != "" {
				info.Dirty = len(strings.Split(out, "\n"))
			}
		}
	}
	return info
}

// runGit 在 dir 下执行 git 命令并返回 stdout。
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// projectLine 把项目信息渲染成状态栏第一行的纯文本（不含颜色标记）。
//
//	📁 mizar  ⎇ main ✱3  │ 会话 a1b2c3d4  │ 已运行 12m
//
// 分支/改动段仅在 git 仓库内出现；会话号为空时省略；运行时长至少显示到分。
func projectLine(info projectInfo, sessionID string, started time.Time, now time.Time) string {
	dir := info.Dir
	if dir == "" {
		dir = "?"
	}
	return projectLineTitled(dir, "", info, sessionID, started, now)
}

// projectLineTitled 与 projectLine 相同，但目录段使用已截好的 shown 文本
// （宽度不够时由调用方算出滞动窗口），供超宽路径滞动显示使用。
func projectLineTitled(shown, fallbackDir string, info projectInfo, sessionID string, started time.Time, now time.Time) string {
	var parts []string
	dir := shown
	if dir == "" {
		dir = fallbackDir
	}
	if dir == "" {
		dir = "?"
	}
	head := "📁 " + dir
	if info.IsGit {
		if info.Branch != "" {
			head += "  ⎇ " + info.Branch
		}
		if info.Dirty > 0 {
			head += " ✱" + strconv.Itoa(info.Dirty)
		}
	}
	parts = append(parts, head)
	if sessionID != "" {
		short := sessionID
		if len(short) > 8 {
			short = short[:8]
		}
		parts = append(parts, "会话 "+short)
	}
	if !started.IsZero() {
		parts = append(parts, "已运行 "+humanDuration(now.Sub(started)))
	}
	return strings.Join(parts, "  │ ")
}

// windowPath 取滞动窗口片：给出完整路径、可用宽度与已滞动步数，
// 返回该帧应显示的文本（不足宽度时返回原串，否则带 … 前缀）。
// 到达末端时会停留 hold 帧再回卷，保证项目名（末端）能被完整看清。
func windowPath(path string, width, tick int) string {
	r := []rune(path)
	if width <= 0 || len(r) <= width {
		return path
	}
	const (
		stepRunes = 3 // 每帧滞动 3 字符，慢一点才看得清
		holdTicks = 6 // 到末端/回卷处停留 6 帧（末端是项目名，比开头重要，多停一会）
	)
	maxStart := len(r) - width
	// 周期：去程 maxStart/step 帧 + 末端停留 hold + 回程（同速）+ 起点停留 hold
	steps := maxStart / stepRunes
	if maxStart%stepRunes != 0 {
		steps++
	}
	span := (steps + holdTicks) * 2
	if span <= 0 {
		return string(r[:width])
	}
	phase := tick % span
	start := 0
	switch {
	case phase < steps:
		start = phase * stepRunes
	case phase < steps+holdTicks:
		start = maxStart // 末端停留
	default:
		back := phase - (steps + holdTicks)
		if back < steps {
			start = maxStart - back*stepRunes
		} else {
			start = 0 // 起点停留
		}
	}
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	end := start + width
	if end > len(r) {
		end = len(r)
	}
	seg := string(r[start:end])
	if start > 0 {
		seg = "…" + string(r[start+1:end]) // 用 … 占掉首字符，提示被截断
	}
	return seg
}

// humanDuration 把时长渲染成紧凑形式：45s / 12m / 3h05m。
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return strconv.Itoa(h) + "h"
		}
		return strconv.Itoa(h) + "h" + pad2(m) + "m"
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
