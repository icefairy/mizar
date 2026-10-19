package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"mizar/internal/agent"
)

// newScrollTestModel 构造一个填充了足够多行的 tuiModel（走 newTuiModel 以拿到
// 真实注册的 InputCapture），供滚动键位断言。
func newScrollTestModel(lines int) *tuiModel {
	m := newTuiModel(&agent.Agent{}, nil, "test", nil)
	m.lines = nil
	for i := 0; i < lines; i++ {
		m.lines = append(m.lines, chatLine{role: "system", content: itoa(i), ts: now()})
	}
	m.renderAllDirect()
	return m
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestScrollChatClampsAndRestoresFollow scrollChat 到顶/到底必须夹紧，
// 且滚到底时恢复自动跟随（userScrolledUp=false）。
func TestScrollChatClampsAndRestoresFollow(t *testing.T) {
	m := newScrollTestModel(200)
	m.scrollChat(-1 << 20)
	if row, _ := m.textView.GetScrollOffset(); row != 0 {
		t.Fatalf("向上跳顶后 offset = %d, want 0", row)
	}
	if !m.userScrolledUp {
		t.Fatal("向上滚后 userScrolledUp 应为 true（暂停自动跟随）")
	}
	m.scrollChat(10)
	if row, _ := m.textView.GetScrollOffset(); row == 0 {
		t.Fatal("向下滚 10 行后 offset 仍为 0")
	}
	m.scrollChat(1 << 20)
	if m.userScrolledUp {
		t.Fatal("滚到底后 userScrolledUp 应复位为 false（恢复自动跟随）")
	}
	if row, total := m.textView.GetScrollOffset(); row+1 < total {
		t.Fatalf("跳底后 offset = %d, total = %d", row, total)
	}
}

// TestVimScrollKeys vim 风格键位必须驱动滚动；裸字母 g/j/k 不得被吞（它们是常用字符）。
func TestVimScrollKeys(t *testing.T) {
	m := newScrollTestModel(200)
	handle := func(ev *tcell.EventKey) *tcell.EventKey {
		return m.app.GetInputCapture()(ev)
	}

	// Ctrl+U 上滚
	handle(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone))
	if !m.userScrolledUp {
		t.Fatal("Ctrl+U 未触发上滚")
	}

	// Alt+K 上滚到顶
	handle(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModAlt))
	if row, _ := m.textView.GetScrollOffset(); row != 0 {
		t.Fatalf("Alt+K 连续上滚后 offset = %d, want 0", row)
	}

	// Alt+J 下滚
	handle(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModAlt))
	if row, _ := m.textView.GetScrollOffset(); row == 0 {
		t.Fatal("Alt+J 未触发下滚")
	}

	// Alt+G 跳顶
	handle(tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModAlt))
	if row, _ := m.textView.GetScrollOffset(); row != 0 {
		t.Fatalf("Alt+G 后 offset = %d, want 0", row)
	}
	if !m.userScrolledUp {
		t.Fatal("Alt+G 跳顶后应暂停自动跟随")
	}

	// Alt+Shift+G（大写 G）跳底
	handle(tcell.NewEventKey(tcell.KeyRune, 'G', tcell.ModAlt|tcell.ModShift))
	if m.userScrolledUp {
		t.Fatal("Alt+Shift+G 跳底后应恢复自动跟随")
	}

	// 裸字母 g/j/k 必须原样交回输入框（不能当快捷键吞掉）
	for _, r := range []rune{'g', 'j', 'k'} {
		if ev := handle(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)); ev == nil {
			t.Fatalf("裸字母 %q 被快捷键吞掉", string(r))
		}
	}
	// Ctrl+C 仍应清空输入框而非退出
	m.inputField.SetText("abc", true)
	handle(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	if m.inputField.GetText() != "" {
		t.Fatal("Ctrl+C 未清空输入框")
	}
}
