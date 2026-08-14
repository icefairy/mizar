package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	glamour "github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// =============================================================================
// 消息
// =============================================================================

type chatLine struct {
	role    string // "system" / "user" / "bot" / "err"
	content string
	ts      time.Time
}

// =============================================================================
// 样式
// =============================================================================

var (
	styleUser = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	styleBot  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	styleSys  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleSep  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleHelp = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// =============================================================================
// 模型
// =============================================================================

type tuiModel struct {
	agent     *agent.Agent
	store     *session.Store
	sessionID string
	input     textinput.Model
	lines     []chatLine
	width     int
	height    int
	loading   bool
	status    string
	renderer  *glamour.TermRenderer
}

func newTuiModel(a *agent.Agent, st *session.Store, sid string) *tuiModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务，/help 查看命令，/quit 退出"
	ti.Focus()
	ti.Width = 80

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
	)

	return &tuiModel{
		agent:     a,
		store:     st,
		sessionID: sid,
		input:     ti,
		width:     80,
		height:    24,
		renderer:  renderer,
		lines: []chatLine{
			{
				role: "system",
				content: banner(),
				ts: time.Now(),
			},
		},
		status: fmt.Sprintf("model=%s", a.Model()),
	}
}

func (m *tuiModel) Init() tea.Cmd { return nil }

type doneMsg struct {
	reply string
	err   error
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 2
		if m.input.Width < 10 {
			m.input.Width = 40
		}

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			if m.loading {
				return m, nil
			}
			s := strings.TrimSpace(m.input.Value())
			if s == "" {
				return m, nil
			}
			m.input.SetValue("")

			// 斜杠命令
			if strings.HasPrefix(s, "/") {
				if handled, out, err := m.agent.Commands.Dispatch(s); handled {
					if err != nil {
						m.lines = append(m.lines, chatLine{role: "err", content: err.Error(), ts: time.Now()})
					}
					if out != "" {
						m.lines = append(m.lines, chatLine{role: "bot", content: out, ts: time.Now()})
					}
					m.status = fmt.Sprintf("model=%s", m.agent.Model())
					return m, nil
				}
			}

			if s == "/quit" || s == "/exit" {
				return m, tea.Quit
			}

			// 普通消息
			m.lines = append(m.lines, chatLine{role: "user", content: s, ts: time.Now()})
			m.loading = true

			done := make(chan doneMsg, 1)
			go func() {
				reply, err := m.agent.Run(s)
				done <- doneMsg{reply: reply, err: err}
			}()
			return m, func() tea.Msg { return <-done }
		}

		if msg.Type == tea.KeyTab {
			// Tab 补全：/ 命令 / @ 补全
			val := m.input.Value()
			if strings.HasPrefix(val, "/") {
				for _, c := range m.agent.Commands.List() {
					full := "/" + c.Name
					if strings.HasPrefix(full, val) && full != val {
						m.input.SetValue(full + " ")
						break
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
					m.input.SetValue(newVal)
				}
			}
			return m, nil
		}

		if msg.Type == tea.KeyEsc {
			if m.loading {
				m.loading = false
				m.lines = append(m.lines, chatLine{role: "err", content: "任务已取消", ts: time.Now()})
			}
			return m, nil
		}

		m.input, _ = m.input.Update(msg)

	case doneMsg:
		m.loading = false
		if msg.err != nil {
			m.lines = append(m.lines, chatLine{role: "err", content: msg.err.Error(), ts: time.Now()})
		} else {
			m.lines = append(m.lines, chatLine{role: "bot", content: msg.reply, ts: time.Now()})
		}
		m.input.Focus()
	}

	return m, nil
}

func (m *tuiModel) View() string {
	var sb strings.Builder

	// 保留最后 15 条（避免撑爆）
	start := 0
	if len(m.lines) > 15 {
		start = len(m.lines) - 15
	}

	for _, l := range m.lines[start:] {
		t := l.ts.Format("15:04")
		switch l.role {
		case "system":
			// banner 直接输出（已含格式化）
			sb.WriteString(l.content + "\n")
		case "user":
			sb.WriteString(styleUser.Render("▶ [" + t + "]") + "\n")
			sb.WriteString(l.content + "\n\n")
		case "bot":
			sb.WriteString(styleBot.Render("▲ [" + t + "]") + "\n")
			// Markdown 渲染
			rendered, err := m.renderer.Render(l.content)
			if err != nil {
				rendered = l.content
			}
			sb.WriteString(rendered + "\n\n")
		case "err":
			sb.WriteString(styleErr.Render("✗ [" + t + "]") + " " + l.content + "\n")
		}
	}

	// 分隔线
	sb.WriteString(styleSep.Render(strings.Repeat("─", m.width-2)) + "\n")

	// 输入区
	if m.loading {
		sb.WriteString("  ⏳ [思考中...] " + m.status + "\n")
	} else {
		sb.WriteString(m.input.View() + "\n")
	}

	sb.WriteString(styleHelp.Render("  Enter=发送 | /help=命令 | /quit=退出 | Esc=取消") + "\n")

	return sb.String()
}

func runTUI(a *agent.Agent, st *session.Store, sessionID string) {
	m := newTuiModel(a, st, sessionID)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
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