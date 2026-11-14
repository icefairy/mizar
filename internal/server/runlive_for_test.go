package server

import (
	"testing"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// TestRunLiveForCoversAcquireWindow 关键回归：acquireRunFor 成功但 agent.Run()
// 还没置 runActive 的窗口期，runLiveFor(conv) 必须返回 true。
//
// 这正是「Agent 模式下打断仍显示排队」的根因：首个 LLM 请求还没返回时
// a.Running() 是 false，steer 据此判成没有活跃循环 → 前端显示排队，
// 但这一轮明明已经开始、注入是能生效的。
func TestRunLiveForCoversAcquireWindow(t *testing.T) {
	cfg := NewConfig("127.0.0.1:0", "tok")
	srv := New(cfg, agent.New(&mockLLM{}, testPluginManager(t)), session.New(t.TempDir()))

	const conv = "conv-a"
	// 窗口期：抢到锁但 Run 未启动
	if !srv.acquireRunFor(conv) {
		t.Fatal("acquireRunFor 应成功")
	}
	if srv.agent.Running() {
		t.Fatal("前置条件不成立：agent 不应处于 Running")
	}
	if !srv.runLiveFor(conv) {
		t.Fatal("窗口期内 runLiveFor 必须为 true（否则打断被误报成排队）")
	}

	// 释放后应恢复 false
	srv.releaseRunFor(conv)
	if srv.runLiveFor(conv) {
		t.Fatal("释放后 runLiveFor 应为 false")
	}
}

// TestRunLiveForIsPerConversation busyRun 不能跨会话污染：
// A 在窗口期时 B 必须仍被判为空闲（否则 B 的消息会被误判成需要打断）。
func TestRunLiveForIsPerConversation(t *testing.T) {
	cfg := NewConfig("127.0.0.1:0", "tok")
	srv := New(cfg, agent.New(&mockLLM{}, testPluginManager(t)), session.New(t.TempDir()))

	if !srv.acquireRunFor("conv-a") {
		t.Fatal("acquire conv-a 应成功")
	}
	defer srv.releaseRunFor("conv-a")

	if !srv.runLiveFor("conv-a") {
		t.Fatal("conv-a 应在跑")
	}
	if srv.runLiveFor("conv-b") {
		t.Fatal("conv-b 空闲，不能被 conv-a 的窗口期污染")
	}
}

// TestRunLiveForEmptyFallsBackToGlobal 空 convID 走全局判断（非 Web 入口语义）。
func TestRunLiveForEmptyFallsBackToGlobal(t *testing.T) {
	cfg := NewConfig("127.0.0.1:0", "tok")
	srv := New(cfg, agent.New(&mockLLM{}, testPluginManager(t)), session.New(t.TempDir()))

	if srv.runLiveFor("") {
		t.Fatal("初始状态应为空闲")
	}
	if !srv.acquireRunFor("") {
		t.Fatal("acquire 空会话应成功")
	}
	defer srv.releaseRunFor("")
	if !srv.runLiveFor("") {
		t.Fatal("空会话窗口期内应为 true")
	}
}
