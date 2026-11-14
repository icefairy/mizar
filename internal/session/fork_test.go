package session

import (
	"os"
	"path/filepath"
	"testing"

	"mizar/internal/agent"
)

// P-2 /fork：分叉血缘记录（append-only 元数据行）。
//
// 血缘记录写进会话 JSONL，但 Load/LoadRaw 不把它当消息返回；
// ForkParent 读出父会话。

func TestAppendForkAndForkParent(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)

	// 非分叉会话：ForkParent 返回空
	if err := st.Append("plain", agent.Message{Role: agent.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	parent, err := st.ForkParent("plain")
	if err != nil || parent != "" {
		t.Fatalf("非分叉会话应返回空血缘: parent=%q err=%v", parent, err)
	}

	// 分叉会话：血缘可查
	if err := st.Append("child", agent.Message{Role: agent.RoleUser, Content: "q1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendFork("child", "plain"); err != nil {
		t.Fatalf("AppendFork: %v", err)
	}
	parent, err = st.ForkParent("child")
	if err != nil || parent != "plain" {
		t.Fatalf("血缘应可查: parent=%q err=%v", parent, err)
	}

	// Load 不把 fork 记录当消息
	msgs, err := st.Load("child")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "q1" {
		t.Fatalf("fork 记录不应出现在消息里: %+v", msgs)
	}

	// 不存在的会话
	if _, err := st.ForkParent(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("不存在的会话应报错")
	}
	_ = os.Remove(filepath.Join(dir, "nope.jsonl"))
}
