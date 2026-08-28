package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mizar/internal/agent"
	"mizar/internal/config"
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
	agent          *agent.Agent
	store          *session.Store
	sessionID      string
	lines          []chatLine
	stats          tuiStats
	userColor      string // 用户消息颜色（默认 white）
	aiColor        string // AI 回复颜色（默认 green）
	loading        bool
	queue          []string // 排队的待发送消息（LIFO）
	queueVisible   bool     // queueView 当前是否在 flex 中可见
	app            *tview.Application
	textView       *tview.TextView
	queueView      *tview.TextView // 排队消息列表（显示在输入框上方）
	inputField     *tview.TextArea
	statusBar      *tview.TextView
	flex           *tview.Flex
	userScrolledUp bool // 用户是否手动向上滚动过（用于防止新消息强制拉回底部）

	// 钩子通信：每次任务用新 channel
	liveMu sync.Mutex
	evtCh  chan toolCallInfo

	// 流式显示状态（streamMu 保护）：LLM 回复逐 token 回调 → 增量提取 → TUI 实时渲染
	streamMu        sync.Mutex
	streamStep      int                // 当前流式步骤（agent 循环 step）
	streamActive    bool               // 当前步骤是否正在流式输出
	extr            agent.StreamTextExtractor // 增量提取器（提取 reply 的 text 字段）
	streamLastFlush time.Time          // 上次刷新时间戳（节流）
	streamFlushBusy bool               // 一次刷新进行中（防止重入）

	// 等待进度指示（spinner 动画 + 宣传语轮换，状态栏显示）
	spinnerOn   atomic.Bool  // spinner 循环是否运行中
	spinnerIdx  atomic.Int32 // 当前动画帧下标
	spinnerLine atomic.Int32 // 当前宣传语下标
	spinnerSet  atomic.Int64 // 开始时间戳（UnixNano，用于显示已等待秒数）
}

// sgrColor 生成 tview 动态颜色标记：[color]text[::-]
func sgrColor(name, text string) string {
	return "[" + name + "]" + text + "[::-]"
}

// =============================================================================
// 直接渲染（仅在事件循环中调用，不用 QueueUpdateDraw 避免死锁）
// =============================================================================

// renderAllDirect 直接渲染聊天区 + 状态栏（事件循环内用）
func (m *tuiModel) renderAllDirect() {
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
			sb.WriteString(sgrColor(m.userColor, fmt.Sprintf("▶ [%s] %s", t, l.content)) + "\n\n")
		case "bot":
			sb.WriteString(sgrColor(m.aiColor, fmt.Sprintf("▲ [%s]", t)) + "\n")
			sb.WriteString(l.content + "\n\n")
		case "err":
			sb.WriteString(sgrColor("red", fmt.Sprintf("✗ [%s] %s", t, l.content)) + "\n")
		case "tool":
			sb.WriteString(sgrColor("cyan", fmt.Sprintf("🔧 [%s] %s", t, l.content)) + "\n")
		}
	}

	// 流式尾行：当前步骤正在输出的回复（增量渲染，不进 lines 历史）
	m.streamMu.Lock()
	active := m.streamActive && m.extr.Replying()
	text := m.extr.Text()
	m.streamMu.Unlock()
	if active && text != "" {
		sb.WriteString(sgrColor(m.aiColor, "▲ 回复中" + "\n"))
		sb.WriteString(text)
		sb.WriteString(sgrColor(m.aiColor, "▍\n\n"))
	}

	m.textView.SetText(sb.String()).SetDynamicColors(true)
	// 仅在首次渲染或新 bot 回复时自动滚动到底部
	if !m.userScrolledUp {
		m.textView.ScrollToEnd()
	}
	m.statusBarDirect()
}

// statusBarDirect 直接渲染状态栏（事件循环内用）
func (m *tuiModel) statusBarDirect() {
	s := &m.stats
	var sb strings.Builder

	// 等待进度指示：spinner 动画 + 轮换宣传语 + 已等待秒数
	// （固定在状态栏，不影响聊天区；流式回复真正输出文本后由「▲ 回复中」接管，spinner 才消失。
	//  注意：不能在 streamActive 置真时就隐藏——首个流式 delta 与首个可见文本之间可能有数秒间隔
	//  （如思考阶段），此时若立即隐藏动画会出现「动画没了但内容迟迟不来」的空白期。）
	m.streamMu.Lock()
	streaming := m.streamActive
	streamText := m.extr.Text()
	m.streamMu.Unlock()
	if m.loading && (!streaming || streamText == "") {
		frame := spinnerFrames[int(m.spinnerIdx.Load())%len(spinnerFrames)]
		line := spinnerLines[int(m.spinnerLine.Load())%len(spinnerLines)]
		secs := int((time.Now().UnixNano() - m.spinnerSet.Load()) / int64(time.Second))
		sb.WriteString(sgrColor("yellow", fmt.Sprintf("%s %s 已等待 %d 秒 ", frame, line, secs)))
		sb.WriteString(sgrColor("magenta", "| "))
	}

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
	model := s.ModelName
	if model == "" {
		model = m.agent.Model()
	}
	sb.WriteString(sgrColor("cyan", model))

	sb.WriteString(sgrColor("magenta", " | "))
	sb.WriteString(sgrColor("magenta", "思考: "))
	if level == "off" {
		sb.WriteString(sgrColor("grey", level))
	} else {
		sb.WriteString(sgrColor("magenta", level))
	}

	m.statusBar.SetText(sb.String()).SetDynamicColors(true)
}

// =============================================================================
// 从 goroutine 安全渲染（用 QueueUpdateDraw，但不会造成死锁）
// =============================================================================

// renderAll 从任意 goroutine 安全更新 UI
func (m *tuiModel) renderAll() {
	m.app.QueueUpdateDraw(func() {
		m.renderAllDirect()
	})
}

// updateStatusBar 从任意 goroutine 安全更新状态栏
func (m *tuiModel) updateStatusBar() {
	m.app.QueueUpdateDraw(func() {
		m.statusBarDirect()
	})
}

// =============================================================================
// 添加消息（事件循环内调用直接版，goroutine 调用 QueueUpdate 版）
// =============================================================================

// addChatLine 添加消息并渲染（仅从事件循环调用）
func (m *tuiModel) addChatLine(line chatLine) {
	m.lines = append(m.lines, line)
	m.renderAllDirect()
}

// addChatLineAsync 从 goroutine 安全添加消息
func (m *tuiModel) addChatLineAsync(line chatLine) {
	m.lines = append(m.lines, line)
	m.renderAll()
}

// setLoading 设置加载状态（仅从事件循环调用）。loading 变化联动 spinner 启停。
func (m *tuiModel) setLoading(loading bool) {
	m.loading = loading
	if loading {
		m.startSpinner()
	} else {
		m.stopSpinner()
	}
	m.renderAllDirect()
}

// startSpinner 启动等待指示（仅从事件循环调用）。重复调用安全（已在跑则仅重置计时）。
func (m *tuiModel) startSpinner() {
	m.spinnerSet.Store(time.Now().UnixNano())
	if m.spinnerOn.Swap(true) {
		return // 循环已在运行
	}
	go m.spinnerLoop()
}

// stopSpinner 停止等待指示（仅从事件循环调用）。
func (m *tuiModel) stopSpinner() {
	m.spinnerOn.Store(false)
}

// spinnerLoop spinner 动画循环：每 100ms 换一帧，每 4s 轮换一条宣传语。
// 仅在 loading 且未进入流式回复时刷新状态栏（流式后由「▲ 回复中」接管）。
func (m *tuiModel) spinnerLoop() {
	i := 0
	lineStart := time.Now()
	for m.spinnerOn.Load() {
		m.spinnerIdx.Store(int32(i % len(spinnerFrames)))
		if time.Since(lineStart) >= 4*time.Second {
			lineStart = time.Now()
			m.spinnerLine.Add(1)
		}
		i++
		m.app.QueueUpdateDraw(func() {
			if m.loading {
				m.statusBarDirect()
			}
		})
		time.Sleep(100 * time.Millisecond)
	}
}

// renderQueue 渲染排队消息列表（仅从事件循环调用）
func (m *tuiModel) renderQueue() {
	if len(m.queue) == 0 {
		m.queueView.SetText("").SetDynamicColors(true)
		// 队列空时隐藏 queueView
		if m.queueVisible {
			m.queueVisible = false
			m.flex.RemoveItem(m.queueView)
		}
		return
	}
	var sb strings.Builder
	sb.WriteString(sgrColor("yellow", fmt.Sprintf("排队 (%d条)  Alt+↑ 取回：", len(m.queue))))
	for i, q := range m.queue {
		// 截断长消息，每行最多 60 字符
		content := q
		if len(content) > 60 {
			content = content[:57] + "..."
		}
		sb.WriteString("\n")
		sb.WriteString(sgrColor("yellow", fmt.Sprintf("%d: %s", i+1, content)))
	}
	m.queueView.SetText(sb.String()).SetDynamicColors(true)
	// 队列非空时显示 queueView
	if !m.queueVisible {
		m.queueVisible = true
		m.flex.AddItem(m.queueView, 0, 0, false)
	}
}

// setLoadingAsync 从 goroutine 安全设置加载状态（loading 赋值挪入事件循环回调，避免数据竞争）
func (m *tuiModel) setLoadingAsync(loading bool) {
	m.app.QueueUpdateDraw(func() {
		m.loading = loading
		if !loading {
			m.stopSpinner()
		}
		m.renderAllDirect()
		// 任务结束后，如果有排队的消息则立即发送（在事件循环中安全）
		if !loading && len(m.queue) > 0 {
			q := m.queue[len(m.queue)-1]
			m.queue = m.queue[:len(m.queue)-1]
			m.startTask(q)
		}
	})
}

func (m *tuiModel) startTask(input string) {
	// 持久化用户消息
	if len(m.lines) > 0 && m.store != nil && m.sessionID != "" {
		m.store.Append(m.sessionID, agent.Message{Role: agent.RoleUser, Content: input})
	}

	m.liveMu.Lock()
	evtCh := make(chan toolCallInfo, 20)
	m.evtCh = evtCh
	m.liveMu.Unlock()

	// 挂载流式回调：LLM 逐 token 增量 → 增量提取（reply 的 text 字段）→ 节流刷新聊天区。
	// 每次任务重新挂载（闭包捕获本次提取器状态）；任务结束/被替换时由 goroutine 卸载。
	m.streamMu.Lock()
	m.streamStep = -1
	m.streamActive = false
	m.extr.Reset()
	m.streamLastFlush = time.Now()
	m.streamFlushBusy = false
	m.agent.OnLLMStream = func(step int, d agent.StreamDelta) {
		m.streamMu.Lock()
		if step != m.streamStep {
			// 新步骤（新一轮 LLM 调用）：重置提取器
			m.streamStep = step
			m.streamActive = true
			m.extr.Reset()
		}
		if d.Content != "" {
			m.extr.Feed(d.Content)
		}
		// 节流（60ms）：避免每个 token 都触发一次全量渲染
		flush := !m.streamFlushBusy && time.Since(m.streamLastFlush) >= 60*time.Millisecond
		if flush {
			m.streamFlushBusy = true
			m.streamLastFlush = time.Now()
			m.streamMu.Unlock()
			m.flushStream()
			return
		}
		m.streamMu.Unlock()
	}
	m.streamMu.Unlock()

	m.setLoading(true)
	m.stats.RequestStartTime = time.Now()
	m.addChatLine(chatLine{role: "user", content: input, ts: time.Now()})

	go func() {
		reply, err := m.agent.Run(input)

		// 任务结束：卸载流式回调、停止流式渲染，并强制刷新一次补上尾部增量
		m.streamMu.Lock()
		m.streamActive = false
		m.agent.OnLLMStream = nil
		m.streamMu.Unlock()
		m.app.QueueUpdateDraw(func() {
			m.renderAllDirect()
			if !m.userScrolledUp {
				m.textView.ScrollToEnd()
			}
		})

		// 任务有效性检查：若用户已取消并发出新任务（evtCh 被替换），丢弃本次结果
		m.liveMu.Lock()
		mine := m.evtCh == evtCh
		if mine {
			m.evtCh = nil
		}
		m.liveMu.Unlock()
		if !mine {
			return
		}

		elapsed := time.Since(m.stats.RequestStartTime)

		if ut, ok := m.agent.LLM.(interface{ LastUsage() *agent.Usage }); ok {
			if u := ut.LastUsage(); u != nil {
				m.stats.AddUsage(u, elapsed)
			}
		}
		if ut, ok := m.agent.LLM.(interface{ ThinkingEnabled() string }); ok {
			m.stats.ThinkingLevel = ut.ThinkingEnabled()
		}
		m.stats.ModelName = m.agent.Model()

		// 无论成功与否，都追加本次 user 消息到初始历史（供下次 Run 继承）
		m.agent.Initial = append(m.agent.Initial, agent.Message{Role: agent.RoleUser, Content: input})

		if err != nil {
			m.addChatLineAsync(chatLine{role: "err", content: err.Error(), ts: time.Now()})
		} else {
			m.addChatLineAsync(chatLine{role: "bot", content: reply, ts: time.Now()})
			if m.store != nil && m.sessionID != "" {
				m.store.Append(m.sessionID, agent.Message{Role: agent.RoleAssistant, Content: reply})
			}
			// 追加 bot 回复到初始历史（工具调用/结果不追加，它们是内部实现细节）
			m.agent.Initial = append(m.agent.Initial, agent.Message{Role: agent.RoleAssistant, Content: reply})
		}

		m.setLoadingAsync(false)
		// queue 在 setLoadingAsync 的 QueueUpdateDraw 回调中处理
	}()
}

// flushStream 流式刷新：把当前提取到的回复文本快照送到事件循环渲染。
// 从流式回调 goroutine 调用；通过 QueueUpdateDraw 在事件循环中安全更新 TUI。
func (m *tuiModel) flushStream() {
	m.app.QueueUpdateDraw(func() {
		m.streamMu.Lock()
		active := m.streamActive && m.extr.Replying()
		text := m.extr.Text()
		m.streamFlushBusy = false
		m.streamMu.Unlock()

		if !active || text == "" {
			return
		}
		m.renderAllDirect()
		if !m.userScrolledUp {
			m.textView.ScrollToEnd()
		}
	})
}

// =============================================================================
// 构造
// =============================================================================

func newTuiModel(a *agent.Agent, st *session.Store, sid string) *tuiModel {
	m := &tuiModel{
		agent:     a,
		store:     st,
		sessionID: sid,
		userColor: "white",
		aiColor:   "green",
		lines: []chatLine{
			{role: "system", content: banner(), ts: time.Now()},
		},
		stats: tuiStats{ModelName: a.Model()},
	}
	// 从配置加载颜色设置
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		if cfg.UserColor != "" {
			m.userColor = cfg.UserColor
		}
		if cfg.AiColor != "" {
			m.aiColor = cfg.AiColor
		}
	}
	// 若有加载的历史消息，渲染到聊天区
	if len(a.Initial) > 0 {
		for _, msg := range a.Initial {
			role := "bot"
			if msg.Role == agent.RoleUser {
				role = "user"
			}
			m.lines = append(m.lines, chatLine{role: role, content: msg.Content, ts: time.Now()})
		}
	}

	// 文本视图（聊天区，可滚动）
	m.textView = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true)
	m.textView.SetBorder(true).
		SetTitle(" 开阳 Mizar ").
		SetTitleAlign(tview.AlignLeft)

	// 输入框（多行 textarea，仿"派"的编辑器形态：支持多行输入，Enter 发送，Alt+Enter 换行）
	m.inputField = tview.NewTextArea().
		SetLabel("> ").
		SetPlaceholder("输入任务（Enter 发送，Alt+Enter 换行），/help 查看命令，/quit 退出")

	// 状态栏（无边框，flex 只用 fixedSize=1 不够放边框+内容）
	m.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(false)

	// 排队消息列表（显示在输入框上方，无边框）
	m.queueView = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(false)

	// 布局：垂直排列（聊天区 flex → 排队列表 → 输入框 → 状态栏）
	m.flex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.queueView, 0, 0, false).
		AddItem(m.inputField, 1, 0, false).
		AddItem(m.statusBar, 1, 0, false)

	// 应用
	m.app = tview.NewApplication()
	m.app.EnableMouse(true)
	m.app.EnablePaste(true)  // 启用 bracketed paste：粘贴多行文本时作为整体处理

	// 鼠标事件捕获：消耗点击事件（不让 textView 窃取焦点），滚轮正常传递
	m.app.SetMouseCapture(func(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
		// 左键点击/拖拽：消耗掉，焦点保持在输入框
		// 鼠标滚轮滚动：正常传递（TextView 自带 MouseHandler 处理）
		// 文本选择：按住 Shift 拖动，由终端模拟器处理（tcell 鼠标跟踪模式下不拦截 Shift+拖拽）
		switch action {
		case tview.MouseLeftDown, tview.MouseLeftUp, tview.MouseLeftClick, tview.MouseLeftDoubleClick:
			return nil, tview.MouseConsumed
		}
		return event, action
	})

	// 全局按键捕获：Enter 发送、Alt+Enter 换行
	m.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers()&tcell.ModAlt == 0 {
			send := m.inputField.GetText()
			m.inputField.SetText("", true)
			m.submitInput(send)
			return nil
		}
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
		// Ctrl+C 不退出：清空输入框
		if event.Key() == tcell.KeyCtrlC {
			m.inputField.SetText("", true)
			return nil
		}
		// Alt+↑ 取回最后一条排队消息
		if event.Key() == tcell.KeyUp && event.Modifiers()&tcell.ModAlt != 0 && len(m.queue) > 0 {
			last := m.queue[len(m.queue)-1]
			m.queue = m.queue[:len(m.queue)-1]
			m.inputField.SetText(last, true)
			m.renderAllDirect()
			m.renderQueue()
			return nil
		}
		// PgUp/PgDn 滚动聊天历史
		if event.Key() == tcell.KeyPgUp {
			m.userScrolledUp = true
			row, _ := m.textView.GetScrollOffset()
			if row >= 10 {
				m.textView.ScrollTo(row-10, 0)
			} else {
				m.textView.ScrollToBeginning()
			}
			return nil
		}
		if event.Key() == tcell.KeyPgDn {
			row, _ := m.textView.GetScrollOffset()
			total := m.textView.GetWrappedLineCount()
			if row+10 < total {
				m.textView.ScrollTo(row+10, 0)
			} else {
				m.userScrolledUp = false
				m.textView.ScrollToEnd()
			}
			return nil
		}
		return event
	})

	// TextArea 的 finished 仅由 Tab/Esc 触发（发送用 Enter，见下方全局键盘捕获）
	m.inputField.SetFinishedFunc(func(key tcell.Key) {
		if key != tcell.KeyTab {
			return
		}
		send := m.inputField.GetText()
		m.inputField.SetText("", true)
		m.submitInput(send)
	})

	// 钩子只注册一次：从 m.evtCh 发送工具调用
	if a.Hooks != nil {
		a.Hooks.OnToolCall(func(ctx *agent.HookContext) error {
			call := toolCallInfo{Tool: ctx.Tool, Args: truncateArgs(ctx.Args)}
			// 工具调用步：流式控制文本不显示——重置提取器、停止流式渲染（后续由工具行接替）
			m.streamMu.Lock()
			m.streamActive = false
			m.streamStep = -1
			m.extr.Reset()
			m.streamMu.Unlock()
			m.addChatLineAsync(chatLine{role: "tool", content: call.Tool + "(" + call.Args + ")", ts: time.Now()})
			return nil
		})
		a.Hooks.OnToolResult(func(ctx *agent.HookContext) error {
			// 工具结果：仅显示错误，正常结果不显示（避免刷屏）
			if ctx.Err != nil {
				m.addChatLineAsync(chatLine{role: "err", content: ctx.Tool + " 失败: " + ctx.Err.Error(), ts: time.Now()})
			}
			return nil
		})
	}

	return m
}

// submitInput 处理发送：命令派发 / 退出 / 排队 / 启动任务
func (m *tuiModel) submitInput(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}

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
			// /color 命令立即重绘聊天区使新颜色生效
			if strings.HasPrefix(s, "/color") {
				m.app.QueueUpdateDraw(m.renderAllDirect)
			} else {
				m.statusBarDirect()
			}
			return
		}
	}

	if s == "/quit" || s == "/exit" {
		m.app.Stop()
		return
	}

	if m.loading {
		m.queue = append(m.queue, s)
		m.renderAllDirect()
		m.renderQueue()
		return
	}

	m.startTask(s)
}

func (m *tuiModel) Run() error {
	// 初始渲染 banner（直接 SetText，因事件循环尚未启动）
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
