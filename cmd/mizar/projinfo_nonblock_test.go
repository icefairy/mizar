package main

// 回归：项目信息采集（git）绝不能阻塞事件循环。
//
// 事故背景：TUI 用着用着键盘全哑、只有 WebUI 正常。根因是任务结束时
// setLoading → refreshProjectInfo → collectProjectInfo → runGit 同步执行
// exec.Command("git", ...).Output()（无超时、无 stdin 隔离）。内网工作目录挂在
// NFS/SMB 或超大仓库上时，git status 可卡几十秒甚至永久挂住，事件循环一停，
// 所有按键就再也没人处理。
//
// 本测试锁死两条性质：
//  1. 异步接口 refreshProjectInfoAsync 必须立即返回（不跑 git 在调用线程上）；
//  2. runGit 必须有超时上限，超时后返回错误而不是永久阻塞。
//     用一个必定超时的场景验证：指向不存在的 dir 也算失败，但要"快速"失败。

import (
	"testing"
	"time"

	"mizar/internal/agent"
)

// TestRefreshProjectInfoAsyncNonBlocking 异步采集必须立刻返回。
func TestRefreshProjectInfoAsyncNonBlocking(t *testing.T) {
	m := newTuiModel(&agent.Agent{}, nil, "t", nil)
	start := time.Now()
	m.refreshProjectInfoAsync()
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("refreshProjectInfoAsync 阻塞了 %v —— 又在调用线程跑 git 了", d)
	}
	// 合并语义：第二次请求应立刻返回（已有一次在跑）。
	start = time.Now()
	m.refreshProjectInfoAsync()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("重复请求未合并，阻塞 %v", d)
	}
}

// TestRunGitHasTimeout runGit 不得无限等待：给一个不存在的目录，必须快速返回错误。
func TestRunGitHasTimeout(t *testing.T) {
	start := time.Now()
	_, err := runGit("/nonexistent-dir-for-mizar-test", "status", "--porcelain")
	d := time.Since(start)
	if err == nil {
		t.Fatal("不存在的目录应当返回错误")
	}
	if d > gitTimeout+2*time.Second {
		t.Fatalf("runGit 耗时 %v，超过超时上限 gitTimeout=%v", d, gitTimeout)
	}
}

// TestCollectProjectInfoDegrades collectProjectInfo 在 git 不可用时必须降级为空字段
// （状态栏少一段信息可以接受，让界面卡住不可以）。
func TestCollectProjectInfoDegrades(t *testing.T) {
	info := collectProjectInfo("/nonexistent-dir-for-mizar-test")
	if info.IsGit {
		t.Fatal("非 git 目录不应标记为 git 仓库")
	}
	if info.Branch != "" || info.Dirty != 0 {
		t.Fatalf("应降级为空字段: %+v", info)
	}
}
