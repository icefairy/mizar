package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"

	"mizar/internal/agent"
)

// 状态栏第一行的渲染规则：git 仓库带分支与改动数，非仓库只显示目录。
func TestProjectLineGitRepo(t *testing.T) {
	info := projectInfo{Dir: "codes/mizar", Branch: "main", Dirty: 3, IsGit: true}
	got := projectLine(info, "a1b2c3d4e5f6", time.Time{}, time.Time{})
	for _, want := range []string{"📁 codes/mizar", "⎇ main", "✱3", "会话 a1b2c3d4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q，实际: %s", want, got)
		}
	}
	// 会话号只取前 8 位，不应出现完整 id
	if strings.Contains(got, "a1b2c3d4e5f6") {
		t.Fatalf("会话号应截断为 8 位: %s", got)
	}
}

// 干净工作区不显示 ✱0；非 git 目录不显示分支段。
func TestProjectLineCleanAndNonGit(t *testing.T) {
	clean := projectLine(projectInfo{Dir: "codes/mizar", Branch: "dev", IsGit: true}, "", time.Time{}, time.Time{})
	if strings.Contains(clean, "✱") {
		t.Fatalf("干净工作区不应显示改动标记: %s", clean)
	}
	if strings.Contains(clean, "会话") {
		t.Fatalf("无会话号时不应出现会话段: %s", clean)
	}

	nonGit := projectLine(projectInfo{Dir: "tmp/x"}, "", time.Time{}, time.Time{})
	if strings.Contains(nonGit, "⎇") || strings.Contains(nonGit, "✱") {
		t.Fatalf("非 git 目录不应有分支/改动段: %s", nonGit)
	}
	if !strings.Contains(nonGit, "tmp/x") {
		t.Fatalf("应显示目录: %s", nonGit)
	}
}

// 运行时长格式紧凑：秒/分/小时。
func TestProjectLineUptime(t *testing.T) {
	start := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		now  time.Time
		want string
	}{
		{start.Add(45 * time.Second), "已运行 45s"},
		{start.Add(12 * time.Minute), "已运行 12m"},
		{start.Add(3*time.Hour + 5*time.Minute), "已运行 3h05m"},
		{start.Add(2 * time.Hour), "已运行 2h"},
	}
	for _, c := range cases {
		got := projectLine(projectInfo{Dir: "x"}, "", start, c.now)
		if !strings.Contains(got, c.want) {
			t.Fatalf("时长错误，期望含 %q，实际: %s", c.want, got)
		}
	}
}

// shortDir 只保留末两级（根目录只留一级）。
func TestShortDir(t *testing.T) {
	cases := map[string]string{
		"/srv/data/codes/mizar": "codes/mizar",
		"/data/mizar":           "data/mizar",
		"/mizar":                "mizar",
		"mizar":                 "mizar",
		"/":                     "/",
	}
	for in, want := range cases {
		if got := shortDir(in); got != want {
			t.Fatalf("shortDir(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 采集真实 git 仓库：分支名非空、IsGit 为真。
func TestCollectProjectInfoInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("环境无 git，跳过")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Skip("取当前目录失败")
	}
	if _, err := runGit(wd, "rev-parse", "--git-dir"); err != nil {
		t.Skip("当前目录不是 git 仓库，跳过")
	}
	info := collectProjectInfo(wd)
	if !info.IsGit {
		t.Fatal("在 git 仓库内应识别为 IsGit")
	}
	if info.Branch == "" {
		t.Fatal("git 仓库内分支名不应为空")
	}
	if info.Dir == "" {
		t.Fatal("目录不应为空")
	}
}

// 非仓库目录：IsGit 为假、分支为空，且不 panic。
func TestCollectProjectInfoNonGit(t *testing.T) {
	dir := t.TempDir()
	info := collectProjectInfo(dir)
	if info.IsGit {
		t.Fatalf("临时目录不应识别为 git 仓库: %+v", info)
	}
	if info.Branch != "" || info.Dirty != 0 {
		t.Fatalf("非仓库不应有分支/改动: %+v", info)
	}
	// 临时目录是 /tmp/TestXxx.../001，末两级为 "TestXxx.../001"
	if !strings.Contains(info.Dir, filepath.Base(dir)) {
		t.Fatalf("目录名不符: %q", info.Dir)
	}
}

// 放得下时原样返回，不滚动、不加 … 前缀。
func TestWindowPathFits(t *testing.T) {
	if got := windowPath("codes/mizar", 30, 5); got != "codes/mizar" {
		t.Fatalf("放得下应原样返回: %q", got)
	}
}

// 超宽路径的滞动：每一帧长度都不超过可用宽度，且【末端项目名一定被完整看到】。
// 这是本次需求的核心：路径太长时可以滚动，但项目名比开头更重要。
func TestWindowPathScrollShowsTail(t *testing.T) {
	path := "very/long/path/to/some/deep/project/codes/mizar"
	width := 24
	tail := "codes/mizar"
	sawTail := false
	for tick := 0; tick < 200; tick++ {
		got := windowPath(path, width, tick)
		if n := len([]rune(got)); n > width {
			t.Fatalf("tick=%d 超出宽度 %d: %q (%d)", tick, width, got, n)
		}
		if strings.Contains(got, tail) && strings.HasSuffix(got, "mizar") {
			sawTail = true
		}
	}
	if !sawTail {
		t.Fatalf("%d 帧内从未完整显示路径末端 %q", 200, tail)
	}
}

// 有滚动时用 … 提示前面被截断；回到起点时不带 …。
func TestWindowPathEllipsis(t *testing.T) {
	path := "aaaaaaaaaabbbbbbbbbbccccccccccdddddddddd"
	width := 12
	first := windowPath(path, width, 0)
	if strings.HasPrefix(first, "…") {
		t.Fatalf("起点不应带 …: %q", first)
	}
	// 走到中段应出现 …
	found := false
	for tick := 1; tick < 40; tick++ {
		if strings.HasPrefix(windowPath(path, width, tick), "…") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("中段滚动应带 … 前缀")
	}
}

// 状态栏第一行要展示 MCP 已连/配置数；无配置时整段省略（不占宽度）。
func TestProjectLineMCPSegment(t *testing.T) {
	withMCP := projectLine(projectInfo{Dir: "codes/mizar", MCPConf: 3, MCPConn: 2}, "", time.Time{}, time.Time{})
	if !strings.Contains(withMCP, "MCP 2/3") {
		t.Fatalf("应显示 MCP 已连/配置: %s", withMCP)
	}
	// MCP 段在项目段之后、会话段之前
	if i, j := strings.Index(withMCP, "📁"), strings.Index(withMCP, "MCP"); i >= j {
		t.Fatalf("MCP 段应位于项目段之后: %s", withMCP)
	}
	none := projectLine(projectInfo{Dir: "codes/mizar"}, "", time.Time{}, time.Time{})
	if strings.Contains(none, "MCP") {
		t.Fatalf("未配置 MCP 时不应出现该段: %s", none)
	}
}

// 状态栏 MCP 统计应实时反映连接建立（惰性连接：连上后从 0/1 变 1/1）。
func TestProjBarMCPSegmentLive(t *testing.T) {
	m := newTUIForQueueTest()
	connected := false
	m.mcpStats = func() (int, int) {
		if connected {
			return 1, 1
		}
		return 1, 0
	}
	m.project = projectInfo{Dir: "codes/mizar"}
	m.projBar = tview.NewTextView().SetDynamicColors(true)
	m.projBarDirect()
	if got := m.projBar.GetText(true); !strings.Contains(got, "MCP 0/1") {
		t.Fatalf("初始应为 MCP 0/1: %q", got)
	}
	connected = true
	m.projBarDirect()
	if got := m.projBar.GetText(true); !strings.Contains(got, "MCP 1/1") {
		t.Fatalf("连接后应为 MCP 1/1: %q", got)
	}
}

// 活跃的拷问/计划模式要在状态栏第一行可见，否则用户会困惑模型为何一直提问。
func TestProjectLineModeBadge(t *testing.T) {
	got := projectLine(projectInfo{Dir: "codes/mizar", Mode: "拷问模式（/grill off · /plan off 退出）"}, "", time.Time{}, time.Time{})
	if !strings.Contains(got, "拷问模式") {
		t.Fatalf("应显示模式标记: %s", got)
	}
	none := projectLine(projectInfo{Dir: "codes/mizar"}, "", time.Time{}, time.Time{})
	if strings.Contains(none, "模式") {
		t.Fatalf("无模式时不应出现该段: %s", none)
	}
}

// modeStatus 反映实际模式状态：三种状态（正常编码 / 计划 / 拷问）都必须有提示，
// 叠加时两个都显示，退出后回到「正常编码模式」。
func TestModeStatus(t *testing.T) {
	m := newTUIForQueueTest()
	// newTUIForQueueTest 直接构造 &agent.Agent{}（无模式控制器），补上
	m.agent.GrillMode = agent.NewGrillMode()
	m.agent.PlanMode = agent.NewPlanMode()

	txt, color := m.modeStatus()
	if !strings.Contains(txt, "正常编码模式") || color == "" {
		t.Fatalf("默认应显示正常编码模式: %q color=%q", txt, color)
	}
	normal := color

	m.agent.GrillMode.Enter("")
	txt, color = m.modeStatus()
	if !strings.Contains(txt, "拷问模式") || !strings.Contains(txt, "/grill off") {
		t.Fatalf("进入拷问模式后应显示且给出退出指令: %q", txt)
	}
	if color == normal {
		t.Fatal("拷问模式应有独立配色")
	}

	m.agent.PlanMode.Enter("")
	txt, color = m.modeStatus()
	if !strings.Contains(txt, "拷问") || !strings.Contains(txt, "计划模式") {
		t.Fatalf("两种模式叠加应都显示: %q", txt)
	}
	if color == normal {
		t.Fatal("叠加状态应有独立配色")
	}

	m.agent.GrillMode.Exit()
	txt, _ = m.modeStatus()
	if !strings.Contains(txt, "计划模式") || strings.Contains(txt, "拷问") {
		t.Fatalf("退出拷问后应只剩计划模式: %q", txt)
	}

	m.agent.PlanMode.Exit()
	txt, color = m.modeStatus()
	if txt != "正常编码模式" || color != normal {
		t.Fatalf("全部退出后应回到正常编码模式: %q", txt)
	}
}

// 模式段在状态栏第一行始终可见（含正常编码模式），且各自配色不同。
func TestProjBarAlwaysShowsMode(t *testing.T) {
	m := newTUIForQueueTest()
	m.agent.GrillMode = agent.NewGrillMode()
	m.agent.PlanMode = agent.NewPlanMode()
	m.project = projectInfo{Dir: "codes/mizar"}
	m.projBar = tview.NewTextView().SetDynamicColors(true)

	m.projBarDirect()
	if got := m.projBar.GetText(true); !strings.Contains(got, "正常编码模式") {
		t.Fatalf("正常模式也应有状态提示: %q", got)
	}
	m.agent.PlanMode.Enter("")
	m.projBarDirect()
	if got := m.projBar.GetText(true); !strings.Contains(got, "计划模式") {
		t.Fatalf("计划模式应显示: %q", got)
	}
	m.agent.PlanMode.Exit()
	m.agent.GrillMode.Enter("")
	m.projBarDirect()
	if got := m.projBar.GetText(true); !strings.Contains(got, "拷问模式") {
		t.Fatalf("拷问模式应显示: %q", got)
	}
}

// colorizeSegment 只给目标段上色，其余保持基础色；seg 不在行内时整行基础色。
func TestColorizeSegment(t *testing.T) {
	out := colorizeSegment("a 计划模式 b", "计划模式", "aqua", "grey")
	for _, want := range []string{"[grey]a ", "[aqua]计划模式", "[grey] b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q: %q", want, out)
		}
	}
	if got := colorizeSegment("abc", "", "aqua", "grey"); got != "[grey]abc[::-]" {
		t.Fatalf("空 seg 应整行基础色: %q", got)
	}
	if got := colorizeSegment("abc", "zzz", "aqua", "grey"); got != "[grey]abc[::-]" {
		t.Fatalf("seg 不存在应整行基础色: %q", got)
	}
}
