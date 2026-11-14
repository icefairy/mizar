package main

import (
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// P-2 /fork 会话分支派生：forkSession 核心逻辑。

func newForkTestEnv(t *testing.T) (*session.Store, *session.TitleCache, *string, *agent.Agent) {
	t.Helper()
	st := session.New(t.TempDir())
	titleCache, err := session.NewTitleCache(st.PathKey())
	if err != nil {
		// NewTitleCache 的目录参数与 Store 的路径作用域无关（标题缓存独立目录），
		// 失败不阻断：forkSession 对 nil titleCache 容错
		titleCache = nil
	}
	sid := ""
	a := &agent.Agent{}
	return st, titleCache, &sid, a
}

func TestForkSession_FullCopy(t *testing.T) {
	st, tc, sid, a := newForkTestEnv(t)
	parent := "11111111-1111-1111-1111-111111111111"
	*sid = parent
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: "第一问"},
		{Role: agent.RoleAssistant, Content: "第一答"},
		{Role: agent.RoleUser, Content: "第二问"},
	}
	for _, m := range msgs {
		if err := st.Append(parent, m); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := st.Load(parent)
	if err != nil {
		t.Fatal(err)
	}
	a.Initial = loaded
	if tc != nil {
		_ = tc.Set(parent, "父标题")
	}

	out, err := forkSession(a, st, tc, sid, "")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !strings.Contains(out, "已分叉") {
		t.Fatalf("输出异常: %q", out)
	}
	newID := *sid
	if newID == parent || newID == "" {
		t.Fatalf("应切换到新会话 ID: %q", newID)
	}
	// 新会话文件 = 父会话全量前缀
	copied, err := st.Load(newID)
	if err != nil || len(copied) != 3 || copied[2].Content != "第二问" {
		t.Fatalf("新会话应携带全部 3 条消息: n=%d err=%v", len(copied), err)
	}
	// 血缘指向父会话
	if p, _ := st.ForkParent(newID); p != parent {
		t.Fatalf("血缘应指向父会话: %q", p)
	}
	// a.Initial 是副本（与父历史不共享底层数组）
	if len(a.Initial) != 3 {
		t.Fatalf("Initial 应为前缀副本: %d", len(a.Initial))
	}
	// 标题带 fork 标记
	if tc != nil && !strings.Contains(tc.Get(newID), "父标题") {
		t.Fatalf("新会话标题应沿用父标题: %q", tc.Get(newID))
	}
}

func TestForkSession_PrefixCut(t *testing.T) {
	st, tc, sid, a := newForkTestEnv(t)
	parent := "22222222-2222-2222-2222-222222222222"
	*sid = parent
	for _, c := range []string{"一", "二", "三", "四"} {
		_ = st.Append(parent, agent.Message{Role: agent.RoleUser, Content: c})
	}
	a.Initial, _ = st.Load(parent)

	if _, err := forkSession(a, st, tc, sid, "2"); err != nil {
		t.Fatalf("fork 2: %v", err)
	}
	copied, _ := st.Load(*sid)
	if len(copied) != 2 || copied[1].Content != "二" {
		t.Fatalf("应只保留前 2 条: %+v", copied)
	}
	// 超界参数截断到全量
	if _, err := forkSession(a, st, tc, sid, "999"); err != nil {
		t.Fatalf("fork 999: %v", err)
	}
	copied, _ = st.Load(*sid)
	if len(copied) != 2 {
		t.Fatalf("超界应截断到当前上下文全长: %d", len(copied))
	}
}

func TestForkSession_Errors(t *testing.T) {
	st, tc, sid, a := newForkTestEnv(t)
	// 空历史
	if _, err := forkSession(a, st, tc, sid, ""); err == nil {
		t.Fatal("空历史应报错")
	}
	// 坏参数
	*sid = "33333333-3333-3333-3333-333333333333"
	_ = st.Append(*sid, agent.Message{Role: agent.RoleUser, Content: "hi"})
	a.Initial, _ = st.Load(*sid)
	if _, err := forkSession(a, st, tc, sid, "abc"); err == nil {
		t.Fatal("非数字参数应报错")
	}
	if _, err := forkSession(a, st, tc, sid, "-1"); err == nil {
		t.Fatal("负数参数应报错")
	}
	// 参数 0 = 空会话，拒绝
	if _, err := forkSession(a, st, tc, sid, "0"); err == nil {
		t.Fatal("保留 0 条应报错（用 /session new）")
	}
	// 出错后当前会话不应被切换
	if *sid != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("失败分叉不应切换会话: %q", *sid)
	}
}
