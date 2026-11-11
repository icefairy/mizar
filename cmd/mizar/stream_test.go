package main

import (
	"strings"
	"testing"
)

// TestSpinnerLinesSanity 保证轮播提示语本身可用：
// 非空、不含换行（状态栏/经典模式都是单行覆盖刷新）、不出现重复条目。
func TestSpinnerLinesSanity(t *testing.T) {
	if len(spinnerLines) == 0 {
		t.Fatal("spinnerLines 不应为空")
	}
	seen := map[string]bool{}
	for i, l := range spinnerLines {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("spinnerLines[%d] 为空", i)
		}
		if strings.ContainsAny(l, "\r\n") {
			t.Fatalf("spinnerLines[%d] 含换行：%q", i, l)
		}
		if seen[l] {
			t.Fatalf("spinnerLines[%d] 重复：%q", i, l)
		}
		seen[l] = true
	}
}

// TestSpinnerLinesCoverRecentFeatures 锁住「近期新功能要在轮播里露出」这条要求。
// 新功能上线时同步更新此表，避免提示语长期停留在老特性上。
func TestSpinnerLinesCoverRecentFeatures(t *testing.T) {
	joined := strings.Join(spinnerLines, "\n")
	wants := []string{
		"mizar -c",          // 续接全局最后一次退出的会话
		"ask_user_question", // 交互提问
		"plan",              // 计划模式
		"grill",             // 拷问模式
		"goal",              // 会话目标
		"delegate_task",     // 子代理委派
		"记忆",                // 长期记忆
		"知识库",               // kb_*
	}
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("轮播提示语缺少近期功能关键词 %q", w)
		}
	}
}
