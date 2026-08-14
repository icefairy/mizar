package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	glamour "github.com/charmbracelet/glamour"

	"mizar/internal/agent"
	"mizar/internal/session"
)

type messageRole int

const (
	msgUser      messageRole = iota
	msgAssistant
	msgThinking
	msgError
	msgToolCall
	msgSystem
)

func (r messageRole) prefix() string {
	switch r {
	case msgUser:
		return "▶"
	case msgAssistant:
		return "▲"
	case msgThinking:
		return "◌"
	case msgError:
		return "✗"
	case msgToolCall:
		return "⚙"
	case msgSystem:
		return "ℹ"
	default:
		return "·"
	}
}

type chatMessage struct {
	role    messageRole
	content string
	ts      time.Time
}

type tuiModel struct {
	agent     *agent.Agent
	store     *session.Store
	sessionID string
	width     int
	height    int
	input     textinput.Model
	pendingInput string

	candidates       []string
	candidateIdx     int
	showAutocomplete bool

	messages []chatMessage
	renderer *glamour.TermRenderer

	loading    bool
	spinner    spinner.Model
	status     string
	cmdHistory []string
	historyIdx int
	quitting   bool
}

func newTuiModel(a *agent.Agent, st *session.Store, sessionID string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务（@cmd:/ @tool:/ @file:/ 补全，Tab 切换候选，↑↓ 历史，Enter 发送）"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 80

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	renderer, _ := glamour.NewTermRenderer(glamour.WithAutoStyle())

	messages := []chatMessage{
		{
			role:    msgSystem,
			content: "# 开阳 · Mizar v0.1.0\n\n输入任务开始对话。按 `Tab` 使用 `@` 补全，`/help` 查看命令，`/quit` 退出。",
			ts:      time.Now(),
		},
	}

	return tuiModel{
		agent:      a,
		store:      st,
		sessionID:  sessionID,
		input:      ti,
		spinner:    sp,
		renderer:   renderer,
		cmdHistory: []string{},
		loading:    false,
		width:      80,
		height:     24,
		messages:   messages,
		status:     fmt.Sprintf("model=%s", a.Model()),
	}
}

func (m tuiModel) Init() tea.Cmd {
	return spinner.Tick
}

type doneMsg struct {
	reply string
	err   error
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = msg.Width - 2
		if m.input.Width < 10 {
			m.input.Width = 40
		}
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			if m.loading {
				return m, nil
			}
			input := strings.TrimSpace(m.input.Value())
			if input == "" {
				return m, nil
			}

			m.cmdHistory = append(m.cmdHistory, input)
			m.historyIdx = len(m.cmdHistory)
			m.showAutocomplete = false
			m.input.SetValue("")

			if strings.HasPrefix(input, "/") {
				if handled, out, err := m.agent.Commands.Dispatch(input); handled {
					if err != nil {
						m.addMessage(msgError, fmt.Sprintf("命令错误: %v", err))
					}
					if out != "" {
						m.addMessage(msgAssistant, out)
					}
					m.status = fmt.Sprintf("model=%s", m.agent.Model())
					return m, nil
				}
			}

			if input == "/quit" || input == "/exit" {
				m.quitting = true
				return m, tea.Quit
			}

			cmdNames, toolNames := m.getCandidates()
			resolved := resolveAtRef(input, cmdNames, toolNames)

			m.addMessage(msgUser, input)
			m.loading = true
			m.addMessage(msgThinking, "思考中...")

			done := make(chan doneMsg, 1)
			go func() {
				reply, err := m.agent.Run(resolved)
				done <- doneMsg{reply: reply, err: err}
			}()
			cmds = append(cmds, func() tea.Msg { return <-done })

			if m.sessionID != "" {
				m.store.Append(m.sessionID, agent.Message{Role: agent.RoleUser, Content: input})
			}
			cmds = append(cmds, spinner.Tick)
			return m, tea.Batch(cmds...)
		}

		if msg.Type == tea.KeyTab {
			if len(m.candidates) == 0 {
				m.triggerAutocomplete()
			} else {
				if m.candidateIdx < len(m.candidates)-1 {
					m.candidateIdx++
				} else {
					m.candidateIdx = 0
				}
				applyCandidate(&m, m.candidates[m.candidateIdx])
			}
			return m, nil
		}

		if msg.Type == tea.KeyEsc {
			if m.loading {
				m.loading = false
				m.addMessage(msgError, "任务已取消")
			} else {
				m.showAutocomplete = false
			}
			return m, nil
		}

		if msg.Type == tea.KeyUp {
			if m.showAutocomplete && len(m.candidates) > 0 {
				if m.candidateIdx > 0 {
					m.candidateIdx--
				} else {
					m.candidateIdx = len(m.candidates) - 1
				}
				applyCandidate(&m, m.candidates[m.candidateIdx])
			} else if m.historyIdx > 0 {
				m.historyIdx--
				m.input.SetValue(m.cmdHistory[m.historyIdx])
			}
			return m, nil
		}

		if msg.Type == tea.KeyDown {
			if m.showAutocomplete && len(m.candidates) > 0 {
				if m.candidateIdx < len(m.candidates)-1 {
					m.candidateIdx++
				} else {
					m.candidateIdx = 0
				}
				applyCandidate(&m, m.candidates[m.candidateIdx])
			} else if m.historyIdx < len(m.cmdHistory) {
				m.historyIdx++
				if m.historyIdx < len(m.cmdHistory) {
					m.input.SetValue(m.cmdHistory[m.historyIdx])
				} else {
					m.input.SetValue("")
				}
			}
			return m, nil
		}

		m.input, _ = m.input.Update(msg)
		m.pendingInput = m.input.Value()

		if after := findAtSuffix(m.pendingInput); after != "" {
			cmdNames, toolNames := m.getCandidates()
			m.candidates = completeAtRaw(after, cmdNames, toolNames)
			m.showAutocomplete = len(m.candidates) > 0
			m.candidateIdx = 0
		} else {
			m.showAutocomplete = false
		}

	case spinner.TickMsg:
		if m.loading {
			m.spinner, _ = m.spinner.Update(msg)
			cmds = append(cmds, spinner.Tick)
		}

	case doneMsg:
		m.loading = false
		if msg.err != nil {
			m.addMessage(msgError, fmt.Sprintf("错误: %v", msg.err))
		} else {
			m.addMessage(msgAssistant, msg.reply)
			if m.sessionID != "" {
				m.store.Append(m.sessionID, agent.Message{Role: agent.RoleAssistant, Content: msg.reply})
			}
		}
		m.input.Focus()
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	var sb strings.Builder

	// 消息区（保留最后 20 条，避免屏幕撑爆）
	startIdx := 0
	if len(m.messages) > 20 {
		startIdx = len(m.messages) - 20
	}
	for _, msg := range m.messages[startIdx:] {
		prefix := msg.role.prefix()
		ts := msg.ts.Format("15:04:05")

		switch msg.role {
		case msgUser:
			sb.WriteString(fmt.Sprintf(" %s [%s] %s\n", prefix, ts, msg.content))
		case msgThinking:
			sb.WriteString(fmt.Sprintf(" %s [%s] %s\n", prefix, ts, msg.content))
		case msgError:
			sb.WriteString(fmt.Sprintf(" %s [%s] %s\n", prefix, ts, msg.content))
		case msgAssistant:
			sb.WriteString(fmt.Sprintf(" %s [%s]\n", prefix, ts))
			rendered, err := m.renderer.Render(msg.content)
			if err != nil {
				rendered = msg.content
			}
			// 缩进 Markdown 内容
			for _, line := range strings.Split(rendered, "\n") {
				sb.WriteString("  " + line + "\n")
			}
			sb.WriteString("\n")
		case msgSystem:
			sb.WriteString(fmt.Sprintf(" %s [%s]\n", prefix, ts))
			rendered, err := m.renderer.Render(msg.content)
			if err != nil {
				rendered = msg.content
			}
			for _, line := range strings.Split(rendered, "\n") {
				sb.WriteString("  " + line + "\n")
			}
			sb.WriteString("\n")
		}
	}

	// 分隔线
	sb.WriteString(strings.Repeat("─", 70) + "\n")

	// 补全候选（垂直显示，最多 8 行）
	if m.showAutocomplete && len(m.candidates) > 0 {
		maxShow := 8
		if len(m.candidates) < maxShow {
			maxShow = len(m.candidates)
		}
		start := m.candidateIdx - maxShow/2
		if start < 0 {
			start = 0
		}
		if start+maxShow > len(m.candidates) {
			start = len(m.candidates) - maxShow
		}
		for i := start; i < start+maxShow; i++ {
			if i == m.candidateIdx {
				sb.WriteString(fmt.Sprintf("  ▸ %s\n", m.candidates[i]))
			} else {
				sb.WriteString(fmt.Sprintf("    %s\n", m.candidates[i]))
			}
		}
		if len(m.candidates) > maxShow {
			sb.WriteString(fmt.Sprintf("    ... (%d 个候选, ↑↓ 滚动)\n", len(m.candidates)))
		}
	}

	// 输入区
	if m.loading {
		sb.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), m.status))
	} else {
		sb.WriteString(m.input.View())
		sb.WriteString("\n")
	}

	sb.WriteString("  [Tab=@补全 | Enter=发送 | ↑↓=历史 | /help=命令 | /quit=退出 | Esc=取消]\n")

	return sb.String()
}

func (m tuiModel) addMessage(role messageRole, content string) {
	m.messages = append(m.messages, chatMessage{role: role, content: content, ts: time.Now()})
}

func (m *tuiModel) getCandidates() (cmdNames, toolNames []string) {
	for _, c := range m.agent.Commands.List() {
		cmdNames = append(cmdNames, "/"+c.Name)
	}
	for _, t := range m.agent.Plugins.Tools() {
		toolNames = append(toolNames, t.Name)
	}
	return
}

func (m *tuiModel) triggerAutocomplete() {
	cmdNames, toolNames := m.getCandidates()
	if idx := strings.LastIndex(m.pendingInput, "@"); idx >= 0 {
		after := strings.TrimSpace(m.pendingInput[idx+1:])
		m.candidates = completeAtRaw(after, cmdNames, toolNames)
		m.showAutocomplete = len(m.candidates) > 0
		m.candidateIdx = 0
	} else {
		for _, c := range cmdNames {
			if strings.HasPrefix(c, m.pendingInput) {
				m.candidates = append(m.candidates, c)
			}
		}
	}
}

func applyCandidate(m *tuiModel, candidate string) {
	if idx := strings.LastIndex(m.pendingInput, "@"); idx >= 0 {
		newLine := m.pendingInput[:idx] + candidate + " "
		m.input.SetValue(newLine)
		m.pendingInput = newLine
	}
}

func runTUI(a *agent.Agent, st *session.Store, sessionID string) {
	model := newTuiModel(a, st, sessionID)
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI 退出: %v\n", err)
		os.Exit(1)
	}
}