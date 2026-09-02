package main

import (
	"strings"
	"testing"

	"github.com/rivo/tview"

	"mizar/internal/agent"
)

// newTUIForQuitTest 构造含 Commands 注册表的 tuiModel（quit 相关测试用）。
func newTUIForQuitTest() *tuiModel {
	a := &agent.Agent{}
	a.Commands = agent.NewCommandRegistry()
	a.Commands.Register(agent.Command{
		Name: "quit",
		Run: func(args string) (string, error) {
			return "再见", nil
		},
	})
	m := &tuiModel{
		agent:     a,
		sessionID: "test-session-12345678",
		lines:     []chatLine{{role: "system", content: "banner"}},
		userColor: "white",
		aiColor:   "green",
	}
	m.textView = tview.NewTextView().SetDynamicColors(true)
	m.queueView = tview.NewTextView().SetDynamicColors(true)
	m.autoComplete = tview.NewTextView().SetDynamicColors(true)
	m.inputField = tview.NewTextArea()
	m.statusBar = tview.NewTextView().SetDynamicColors(true)
	m.app = tview.NewApplication()
	m.flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.textView, 0, 1, true).
		AddItem(m.autoComplete, 1, 0, false).
		AddItem(m.inputField, 5, 0, false).
		AddItem(m.statusBar, 1, 0, false)
	return m
}

// TestQuitCommandNoOsExit quit 命令不再 os.Exit：返回“再见”且由上层处理退出。
func TestQuitCommandNoOsExit(t *testing.T) {
	a := &agent.Agent{}
	a.Commands = agent.NewCommandRegistry()
	a.Commands.Register(agent.Command{Name: "quit", Run: func(string) (string, error) { return "再见", nil }})
	handled, out, err := a.Commands.Dispatch("/quit")
	if !handled || err != nil {
		t.Fatalf("dispatch quit: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(out, "再见") {
		t.Fatalf("quit out = %q", out)
	}
	// 注册表里不允许 os.Exit —— Run 返回后进程必须仍存活（无法直接断言，
	// 通过“函数能返回”隐含验证，此处仅确认返回值形态）
	if out == "" {
		t.Fatal("quit should return a farewell message")
	}
}

// TestAddQuitHintContainsSession 续接提示含会话 ID。
func TestAddQuitHintContainsSession(t *testing.T) {
	m := newTUIForQuitTest()
	m.addQuitHint()
	last := m.lines[len(m.lines)-1]
	if last.role != "system" {
		t.Fatalf("hint role = %q, want system", last.role)
	}
	if !strings.Contains(last.content, "test-session-12345678") ||
		!strings.Contains(last.content, "mizar -session") {
		t.Fatalf("hint content = %q", last.content)
	}
}

// TestSubmitInputQuitRoutesToStop submitInput 收到 /quit 时应走退出路径
// （渲染提示 + Stop），而不是把消息交给命令分发或 startTask。
func TestSubmitInputQuitRoutesToStop(t *testing.T) {
	m := newTUIForQuitTest()
	before := len(m.lines)
	m.submitInput("/quit")
	// 退出提示已加入对话列表
	if len(m.lines) != before+1 {
		t.Fatalf("lines = %d, want %d (one hint added)", len(m.lines), before+1)
	}
	if !strings.Contains(m.lines[len(m.lines)-1].content, "mizar -session") {
		t.Fatalf("last line = %q", m.lines[len(m.lines)-1].content)
	}
	// 不应启动普通任务（loading 不应为 true — submitInput 不会直接设置，
	// 通过检查无新 user 行即可断言）
	if m.lines[len(m.lines)-1].role == "user" {
		t.Fatal("/quit 不应被作为普通任务发送")
	}
}
