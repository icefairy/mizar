package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// =============================================================================
// 消息
// =============================================================================

type chatLine struct {
	role    string // "system" / "user" / "bot" / "err" / "tool"
	content string
	ts      time.Time
}

type toolCallInfo struct {
	Tool string
	Args string
}

// =============================================================================
// TUI 模型
// =============================================================================

type tuiStats struct {
	CumulativePromptTokens     int
	CumulativeCompletionTokens int
	CumulativeTotalTokens      int
	CumulativeCachedTokens     int
	LastPromptTokens           int
	LastCompletionTokens       int
	LastResponseDuration       float64
	LastSpeedTokensPerSec      float64
	RequestStartTime           time.Time
	ThinkingLevel              string
	ModelName                  string
}

func (s *tuiStats) AddUsage(u *agent.Usage, dur time.Duration) {
	if u == nil {
		return
	}
	s.CumulativePromptTokens += u.PromptTokens
	s.CumulativeCompletionTokens += u.CompletionTokens
	s.CumulativeTotalTokens += u.TotalTokens
	s.CumulativeCachedTokens += u.CachedTokens
	s.LastPromptTokens = u.PromptTokens
	s.LastCompletionTokens = u.CompletionTokens
	s.LastResponseDuration = dur.Seconds()
	if u.CompletionTokens > 0 && dur.Seconds() > 0 {
		s.LastSpeedTokensPerSec = float64(u.CompletionTokens) / dur.Seconds()
	}
}

type tuiModel struct {
	agent      *agent.Agent
	store      *session.Store
	sessionID  string
	lines      []chatLine
	stats      tuiStats
	loading    bool
	app        *tview.Application
	textView   *tview.TextView
	inputField *tview.InputField
	statusBar  *tview.TextView
	flex       *tview.Flex

	// 钩子通信：每次任务用新 channel
	liveMu sync.Mutex
	evtCh  chan toolCallInfo
}

// sgrColor 生成 tview 动态颜色标记：[color:name]text[::]
func sgrColor(name, text string) string {
	return "[#" + name + "]" + text + "[::]"
}

func (m *tuiModel) addChatLine(line chatLine) {
	m.lines = append(m.lines, line)
	m.renderAll()
}

func (m *tuiModel) renderAll() {
	m.app.QueueUpdateDraw(func() {
		var sb strings.Builder
		start := 0
		if len(m.lines) > 30 {
			start = len(m.lines) - 30
		}
		for _, l := range m.lines[start:] {
			t := l.ts.Format("15:04")
			switch l.role {
			case "system":
				sb.WriteString(l.content + "\n")
			case "user":
				sb.WriteString(sgrColor("bold", fmt.Sprintf("▶ [%s] %s", t, l.content)) + "\n\n")
			case "bot":
				sb.WriteString(sgrColor("green", fmt.Sprintf("▲ [%s]", t)) + "\n")
				sb.WriteString(l.content + "\n\n")
			case "err":
				sb.WriteString(sgrColor("red", fmt.Sprintf("✗ [%s] %s", t, l.content)) + "\n")
			case "tool":
				sb.WriteString(sgrColor("cyan", fmt.Sprintf("🔧 [%s] %s", t, l.content)) + "\n")
			}
		}
		m.textView.SetText(sb.String()).SetDynamicColors(true).ScrollToEnd()
		m.updateStatusBar()
	})
}

func (m *tuiModel) updateStatusBar() {
	s := &m.stats
	var sb strings.Builder

	sb.WriteString(sgrColor("magenta", "tokens: "))
	sb.WriteString(fmt.Sprintf("%d", s.CumulativeTotalTokens))
	sb.WriteString(sgrColor("magenta", "(in:"))
	sb.WriteString(fmt.Sprintf("%d", s.CumulativePromptTokens))
	sb.WriteString(sgrColor("magenta", " out:"))
	sb.WriteString(fmt.Sprintf("%d", s.CumulativeCompletionTokens))
	sb.WriteString(sgrColor("magenta", ")"))

	if a := m.agent; a != nil && a.Compactor != nil {
		limit := a.Compactor.ContextWindow - a.Compactor.ReserveTokens
		if limit > 0 && len(m.lines) > 0 {
			est := 0
			for i := 1; i < len(m.lines); i++ {
				est += agent.EstimateTokens(agent.Message{Content: m.lines[i].content})
			}
			pct := float64(est) / float64(limit) * 100
			color := "green"
			if pct > 80 {
				color = "red"
			} else if pct > 60 {
				color = "yellow"
			}
			sb.WriteString(sgrColor("magenta", " | "))
			sb.WriteString(sgrColor(color, fmt.Sprintf("压缩: %.0f%%", pct)))
		}
	}

	if s.CumulativeCachedTokens > 0 && s.CumulativePromptTokens > 0 {
		hit := float64(s.CumulativeCachedTokens) / float64(s.CumulativePromptTokens) * 100
		sb.WriteString(sgrColor("magenta", " | "))
		sb.WriteString(sgrColor("magenta", fmt.Sprintf("缓存: %.0f%%", hit)))
	} else {
		sb.WriteString(sgrColor("magenta", " | "))
		sb.WriteString(sgrColor("magenta", "缓存: -"))
	}

	if s.LastCompletionTokens > 0 && s.LastResponseDuration > 0 {
		sb.WriteString(sgrColor("magenta", " | "))
		sb.WriteString(sgrColor("magenta", fmt.Sprintf("%.0f tok/s", s.LastSpeedTokensPerSec)))
	} else {
		sb.WriteString(sgrColor("magenta", " | "))
		sb.WriteString(sgrColor("magenta", ". tok/s"))
	}

	level := s.ThinkingLevel
	if level == "" {
		level = "auto"
	}
	sb.WriteString(sgrColor("magenta", " | "))
	sb.WriteString(sgrColor("magenta", "思考: "))
	if level == "off" {
		sb.WriteString(sgrColor("grey", level))
	} else {
		sb.WriteString(sgrColor("magenta", level))
	}

	m.app.QueueUpdateDraw(func() {
		m.statusBar.SetText(sb.String()).SetDynamicColors(true)
	})
}

func (m *tuiModel) setLoading(loading bool) {
	m.loading = loading
	m.app.QueueUpdateDraw(func() {
		if loading {
			m.inputField.SetPlaceholder("思考中...")
			m.inputField.SetLabel("> ")
		} else {
			m.inputField.SetPlaceholder("输入任务，/help 查看命令，/quit 退出")
		}
		m.updateStatusBar()
	})
}

func (m *tuiModel) startTask(input string) {
	m.liveMu.Lock()
	evtCh := make(chan toolCallInfo, 20)
	m.evtCh = evtCh
	m.liveMu.Unlock()

	m.setLoading(true)
	m.stats.RequestStartTime = time.Now()
	m.addChatLine(chatLine{role: "user", content: input, ts: time.Now()})

	go func() {
		reply, err := m.agent.Run(input)
		m.liveMu.Lock()
		m.evtCh = nil
		m.liveMu.Unlock()

		elapsed := time.Since(m.stats.RequestStartTime)

		// 用量
		if ut, ok := m.agent.LLM.(interface{ LastUsage() *agent.Usage }); ok {
			if u := ut.LastUsage(); u != nil {
				m.stats.AddUsage(u, elapsed)
			}
		}
		// 思考等级
		if ut, ok := m.agent.LLM.(interface{ ThinkingEnabled() string }); ok {
			m.stats.ThinkingLevel = ut.ThinkingEnabled()
		}
		m.stats.ModelName = m.agent.Model()

		if err != nil {
			m.addChatLine(chatLine{role: "err", content: err.Error(), ts: time.Now()})
		} else {
			m.addChatLine(chatLine{role: "bot", content: reply, ts: time.Now()})
		}

		m.setLoading(false)
	}()
}

// =============================================================================
// 构造
// =============================================================================

func newTuiModel(a *agent.Agent, st *session.Store, sid string) *tuiModel {
	m := &tuiModel{
		agent:     a,
		store:     st,
		sessionID: sid,
		lines: []chatLine{
			{role: "system", content: banner(), ts: time.Now()},
		},
		stats: tuiStats{ModelName: a.Model()},
	}

	// 文本视图（聊天区，可滚动）
	m.textView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true)
	m.textView.SetBorder(true).
		SetTitle(" 开阳 Mizar ").
		SetTitleAlign(tview.AlignLeft)

	// 输入框
	m.inputField = tview.NewInputField().
		SetLabel("> ").
		SetFieldWidth(0).
		SetPlaceholder("输入任务，/help 查看命令，/quit 退出")

	// 状态栏
	m.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(false)
	m.statusBar.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	// 布局：垂直排列
	m.flex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.inputField, 1, 0, false).
		AddItem(m.statusBar, 1, 0, false)

	// 应用
	m.app = tview.NewApplication()

	// 全局按键捕获（Ctrl+T 切换思考等级）
	m.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlT {
			if toggle, ok := m.agent.LLM.(interface{ ToggleThinking() string }); ok {
				newLevel := toggle.ToggleThinking()
				m.stats.ThinkingLevel = newLevel
				m.addChatLine(chatLine{role: "err", content: "思考等级已切换: " + newLevel, ts: time.Now()})
			}
			return nil
		}
		if event.Key() == tcell.KeyEsc && m.loading {
			m.setLoading(false)
			m.addChatLine(chatLine{role: "err", content: "任务已取消", ts: time.Now()})
			return nil
		}
		return event // 转发
	})

	// 输入框按键捕获（Enter 发送，Tab 补全）
	m.inputField.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			// 回车发送
			if m.loading {
				return nil
			}
			s := strings.TrimSpace(m.inputField.GetText())
			if s == "" {
				return nil
			}
			m.inputField.SetText("")

			// 斜杠命令
			if strings.HasPrefix(s, "/") {
				if handled, out, err := m.agent.Commands.Dispatch(s); handled {
					if err != nil {
						m.addChatLine(chatLine{role: "err", content: err.Error(), ts: time.Now()})
					}
					if out != "" {
						m.addChatLine(chatLine{role: "bot", content: out, ts: time.Now()})
					}
					m.stats.ModelName = m.agent.Model()
					if ut, ok := m.agent.LLM.(interface{ ThinkingEnabled() string }); ok {
						m.stats.ThinkingLevel = ut.ThinkingEnabled()
					}
					m.updateStatusBar()
					return nil
				}
			}

			if s == "/quit" || s == "/exit" {
				m.app.Stop()
				return nil
			}

			m.startTask(s)
			return nil

		case tcell.KeyTab:
			// Tab 补全
			val := m.inputField.GetText()
			if strings.HasPrefix(val, "/") {
				for _, c := range m.agent.Commands.List() {
					full := "/" + c.Name
					if strings.HasPrefix(full, val) && full != val {
						m.inputField.SetText(full + " ")
						return nil
					}
				}
			} else if idx := strings.LastIndex(val, "@"); idx >= 0 {
				after := strings.TrimSpace(val[idx+1:])
				cmds := make([]string, 0)
				for _, c := range m.agent.Commands.List() {
					cmds = append(cmds, "/"+c.Name)
				}
				tools := make([]string, 0)
				for _, t := range m.agent.Plugins.Tools() {
					tools = append(tools, t.Name)
				}
				cands := completeAtRaw(after, cmds, tools)
				if len(cands) > 0 {
					newVal := val[:idx] + cands[0] + " "
					m.inputField.SetText(newVal)
				}
			}
			return nil
		}
		return event
	})

	// 钩子只注册一次：从 m.evtCh 发送工具调用
	if a.Hooks != nil {
		a.Hooks.OnToolCall(func(ctx *agent.HookContext) error {
			m.liveMu.Lock()
			ch := m.evtCh
			m.liveMu.Unlock()
			if ch == nil {
				return nil
			}
			call := toolCallInfo{Tool: ctx.Tool, Args: truncateArgs(ctx.Args)}
			select {
			case ch <- call:
				m.addChatLine(chatLine{role: "tool", content: call.Tool + "(" + call.Args + ")", ts: time.Now()})
			default:
			}
			return nil
		})
	}

	return m
}

func (m *tuiModel) Run() error {
	// 初始渲染 banner（直接设 text 而非 QueueUpdateDraw，因事件循环尚未启动）
	var sb strings.Builder
	sb.WriteString(banner() + "\n")
	m.textView.SetText(sb.String()).SetDynamicColors(true)

	m.app.SetRoot(m.flex, true)
	m.app.SetFocus(m.inputField)
	return m.app.Run()
}

func truncateArgs(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 80 {
		return s
	}
	return s[:77] + "..."
}

func runTUI(a *agent.Agent, st *session.Store, sessionID string) {
	m := newTuiModel(a, st, sessionID)
	if err := m.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI 退出: %v，回退经典模式\n", err)
		fmt.Println(banner())
		classicFallback(a, st, sessionID)
		return
	}
}

func classicFallback(a *agent.Agent, st *session.Store, sessionID string) {
	for {
		var line string
		fmt.Print("> ")
		_, err := fmt.Scanln(&line)
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "/quit" || line == "/exit" {
			break
		}
		if strings.HasPrefix(line, "/") {
			if handled, out, err := a.Commands.Dispatch(line); handled {
				if err != nil {
					fmt.Printf("命令错误: %v\n", err)
				}
				if out != "" {
					fmt.Println(out)
				}
				continue
			}
		}
		reply, err := a.Run(line)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			continue
		}
		fmt.Println(reply)
	}
}
