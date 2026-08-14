package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	glamour "github.com/charmbracelet/glamour"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// messageRole 消息类型。
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

// chatMessage 一条聊天消息。
type chatMessage struct {
	role    messageRole
	content string
	ts      time.Time
}

// tuiModel Bubble Tea 主模型。
type tuiModel struct {
	agent       *agent.Agent
	store       *session.Store
	sessionID   string
	width       int
	height      int
	input       textinput.Model
	pendingInput string

	candidates       []string
	candidateIdx     int
	showAutocomplete bool

	messages []chatMessage
	viewport viewport.Model
	renderer *glamour.TermRenderer

	loading  bool
	spinner  spinner.Model
	status   string
	err      error
	cmdHistory []string
	historyIdx int
	quitting bool
}

func newTuiModel(a *agent.Agent, st *session.Store, sessionID string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务（@cmd:/ @tool:/ @file:/ 补全，Tab 切换候选，↑↓ 历史，Enter 发送）"
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 120

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	vm := viewport.New(60, 20)

	renderer, _ := glamour.NewTermRenderer(glamour.WithAutoStyle())

	return tuiModel{
		agent:      a,
		store:      st,
		sessionID:  sessionID,
		input:      ti,
		spinner:    sp,
		viewport:   vm,
		renderer:   renderer,
		cmdHistory: []string{},
		loading:    false,
		status:     fmt.Sprintf("model=%s", a.Model()),
	}
}

func fetchCmdToolNames(a *agent.Agent) (cmdNames, toolNames []string) {
	for _, c := range a.Commands.List() {
		cmdNames = append(cmdNames, "/"+c.Name)
	}
	for _, t := range a.Plugins.Tools() {
		toolNames = append(toolNames, t.Name)
	}
	return
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
		inputAreaH := 4
		if m.showAutocomplete {
			inputAreaH += len(m.candidates) + 1
		}
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - inputAreaH
		m.input.Width = msg.Width - 4
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "enter" {
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

			// 斜杠命令
			if strings.HasPrefix(input, "/") {
				if handled, out, err := m.agent.Commands.Dispatch(input); handled {
					if err != nil {
						m.addMessage(msgError, fmt.Sprintf("命令错误: %v", err))
					}
					if out != "" {
						m.addMessage(msgAssistant, out)
					}
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

			// 异步 LLM 调用
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

		if msg.String() == "tab" || msg.String() == "shift+tab" {
			if len(m.candidates) == 0 {
				m.triggerAutocomplete()
			} else {
				if msg.String() == "tab" {
					m.candidateIdx = (m.candidateIdx + 1) % len(m.candidates)
				} else {
					m.candidateIdx = (m.candidateIdx - 1 + len(m.candidates)) % len(m.candidates)
				}
				applyCandidate(&m, m.candidates[m.candidateIdx])
			}
			return m, nil
		}

		if msg.String() == "esc" {
			m.showAutocomplete = false
			return m, nil
		}

		if msg.String() == "up" {
			if m.historyIdx > 0 {
				m.historyIdx--
				m.input.SetValue(m.cmdHistory[m.historyIdx])
			}
			return m, nil
		}

		if msg.String() == "down" {
			if m.historyIdx < len(m.cmdHistory) {
				m.historyIdx++
				if m.historyIdx < len(m.cmdHistory) {
					m.input.SetValue(m.cmdHistory[m.historyIdx])
				} else {
					m.input.SetValue("")
				}
			}
			return m, nil
		}

		// 普通输入
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
		m.viewport.GotoBottom()
		m.input.Focus()
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	var sb strings.Builder

	sb.WriteString(m.viewport.View())
	sb.WriteString(fmt.Sprintf("┌─%s┐\n", strings.Repeat("─", m.width-2)))

	if m.showAutocomplete {
		sb.WriteString("  ")
		for i, c := range m.candidates {
			if i == m.candidateIdx {
				sb.WriteString(fmt.Sprintf("█ %s", c))
			} else {
				sb.WriteString(fmt.Sprintf("  %s", c))
			}
			if i < len(m.candidates)-1 {
				sb.WriteString(" | ")
			}
		}
		sb.WriteString("\n")
	}

	if m.loading {
		sb.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), m.status))
	} else {
		sb.WriteString(m.input.View())
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("└─%s┘\n", strings.Repeat("─", m.width-2)))
	sb.WriteString("  [Tab=@补全 | Enter=发送 | ↑↓=历史 | /help=命令 | /quit=退出]\n")

	return sb.String()
}

func (m tuiModel) addMessage(role messageRole, content string) {
	m.messages = append(m.messages, chatMessage{role: role, content: content, ts: time.Now()})
}

func (m tuiModel) renderMessages() string {
	var sb strings.Builder
	for _, msg := range m.messages {
		prefix := msg.role.prefix()
		ts := msg.ts.Format("15:04:05")

		switch msg.role {
		case msgUser:
			sb.WriteString(fmt.Sprintf("**%s %s**\n\n`%s`\n\n---\n\n", prefix, ts, msg.content))
		case msgThinking:
			sb.WriteString(fmt.Sprintf("**%s %s** %s\n\n", prefix, ts, msg.content))
		case msgError:
			sb.WriteString(fmt.Sprintf("**%s %s**\n\n```error\n%s\n```\n\n---\n\n", prefix, ts, msg.content))
		case msgAssistant:
			sb.WriteString(fmt.Sprintf("**%s %s**\n\n", prefix, ts))
			rendered, err := m.renderer.Render(msg.content)
			if err != nil {
				rendered = msg.content
			}
			sb.WriteString(rendered)
			sb.WriteString("\n\n---\n\n")
		case msgToolCall:
			sb.WriteString(fmt.Sprintf("**%s %s** `%s`\n\n", prefix, ts, msg.content))
		case msgSystem:
			sb.WriteString(fmt.Sprintf("**%s %s** `%s`\n\n", prefix, ts, msg.content))
		}
	}
	return sb.String()
}

func (m *tuiModel) getCandidates() (cmdNames, toolNames []string) {
	return fetchCmdToolNames(m.agent)
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