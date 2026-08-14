package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mizar/internal/agent"
	"mizar/internal/session"
)

type chatLine struct {
	role    string
	content string
}

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
	quitting  bool
}

func newTuiModel(a *agent.Agent, st *session.Store, sid string) *tuiModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务，/help 查看命令，/quit 退出"
	ti.Focus()
	ti.Width = 60

	return &tuiModel{
		agent:     a,
		store:     st,
		sessionID: sid,
		input:     ti,
		width:     80,
		height:    24,
		lines: []chatLine{
			{role: "system", content: "开阳 · Mizar v0.1.0 — 输入任务开始对话。/help 查看命令，/quit 退出"},
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

			if strings.HasPrefix(s, "/") {
				if handled, out, err := m.agent.Commands.Dispatch(s); handled {
					if err != nil {
						m.lines = append(m.lines, chatLine{role: "err", content: "错误: " + err.Error()})
					}
					if out != "" {
						m.lines = append(m.lines, chatLine{role: "bot", content: out})
					}
					m.status = fmt.Sprintf("model=%s", m.agent.Model())
					return m, nil
				}
			}

			if s == "/quit" || s == "/exit" {
				m.quitting = true
				return m, tea.Quit
			}

			m.lines = append(m.lines, chatLine{role: "user", content: s})
			m.loading = true

			done := make(chan doneMsg, 1)
			go func() {
				reply, err := m.agent.Run(s)
				done <- doneMsg{reply: reply, err: err}
			}()
			return m, func() tea.Msg { return <-done }
		}

		if msg.Type == tea.KeyEsc {
			if m.loading {
				m.loading = false
				m.lines = append(m.lines, chatLine{role: "err", content: "任务已取消"})
			}
			return m, nil
		}

		m.input, _ = m.input.Update(msg)

	case doneMsg:
		m.loading = false
		if msg.err != nil {
			m.lines = append(m.lines, chatLine{role: "err", content: "错误: " + msg.err.Error()})
		} else {
			m.lines = append(m.lines, chatLine{role: "bot", content: msg.reply})
		}
		m.input.Focus()
	}

	return m, nil
}

func (m *tuiModel) View() string {
	var sb strings.Builder

	// 消息区（保留最后 20 条）
	start := 0
	if len(m.lines) > 20 {
		start = len(m.lines) - 20
	}
	for _, l := range m.lines[start:] {
		switch l.role {
		case "system":
			sb.WriteString(">> " + l.content + "\n")
		case "user":
			sb.WriteString("> " + l.content + "\n")
		case "bot":
			sb.WriteString("<< ")
			sb.WriteString(l.content + "\n")
		case "err":
			sb.WriteString("!! " + l.content + "\n")
		}
	}

	// 分隔线
	sb.WriteString(strings.Repeat("-", m.width) + "\n")

	// 输入区
	if m.loading {
		sb.WriteString("  [思考中...] " + m.status + "\n")
	} else {
		sb.WriteString(m.input.View() + "\n")
	}

	sb.WriteString("  Enter=发送 | /help=命令 | /quit=退出 | Esc=取消\n")

	return sb.String()
}

func runTUI(a *agent.Agent, st *session.Store, sessionID string) {
	m := newTuiModel(a, st, sessionID)
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI 退出: %v\n", err)
	}
}
