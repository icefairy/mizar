package main

import (
	"reflect"
	"testing"

	"mizar/internal/agent"
)

// TestRecentUserMessagesFiltersInternal 历史浏览只应循环「普通用户消息」：
// AI 回复（assistant）、工具结果/工具调用（role=user 但 kind 非空）均不应出现；
// 且顺序为最新在前（↑ 第一次按回最新一条）。
func TestRecentUserMessagesFiltersOnly(t *testing.T) {
	m := &tuiModel{
		agent: &agent.Agent{
			Initial: []agent.Message{
				{Role: agent.RoleUser, Content: "最早的问题"},                       // 普通用户消息 → 应保留
				{Role: agent.RoleAssistant, Content: "AI 回复一"},                   // AI 回复 → 排除
				{Role: agent.RoleUser, Kind: agent.KindToolResult, Content: "工具结果: {...}"}, // 工具结果 → 排除
				{Role: agent.RoleAssistant, Kind: agent.KindToolCall, Content: `{"tool":"bash"}`}, // 工具调用 → 排除
				{Role: agent.RoleUser, Content: "第二次提问"},                       // 普通用户消息 → 保留
				{Role: agent.RoleAssistant, Content: "AI 回复二"},                   // AI 回复 → 排除
				{Role: agent.RoleUser, Content: "刚刚的问题"},                       // 最新普通用户消息 → 最前
			},
		},
	}
	got := m.recentUserMessages()
	want := []string{"刚刚的问题", "第二次提问", "最早的问题"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recentUserMessages = %#v, want %#v", got, want)
	}
}

// TestRecentUserMessagesEmpty 无用户消息时返回空（↑ 键不 panic）。
func TestRecentUserMessagesEmpty(t *testing.T) {
	m := &tuiModel{agent: &agent.Agent{Initial: []agent.Message{
		{Role: agent.RoleAssistant, Content: "只有 AI"},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, Content: "工具结果"},
	}}}
	if got := m.recentUserMessages(); len(got) != 0 {
		t.Fatalf("want empty, got %#v", got)
	}
}