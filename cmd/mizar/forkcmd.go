// /fork 会话分支派生（P-2，对齐 pi /fork 简化版）。
//
// 从当前会话历史派生一个新 session ID 并重放前缀——源会话原样保留，
// 新会话独立演进（试错型任务的「存档点」：分叉出去随便折腾，不满意就回来）。
//
// 设计要点：
//   - 分叉范围 = 当前 LLM 上下文 a.Initial（模型看见什么就分叉什么，语义直观）。
//     无参 = 全量；/fork <n> = 只保留前 n 条（回到指定消息位置的分支）。
//   - 血缘记一条 {"type":"fork","parent":...} 元数据行（session.ForkParent 可查，
//     Load 不当作消息）；标题沿用源会话 + " (fork)" 后缀，/sessions 能区分。
//   - 派生状态重建：todo 依赖随前缀回放（SetTodosFromHistory）。子任务句柄不从
//     标记恢复——a.Initial 已剔除 subagent-state 标记，且进程内 manager 仍持有
//     全部句柄；分叉后新的终态快照随新会话落盘（GAP-S4）。
//   - interactive/TUI 的会话 ID 经指针同步（sessionIDFn/*string），分叉后落盘
//     立即切到新会话文件——顺带修掉 /session 切换后仍写旧文件的既有错位。
package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"mizar/internal/agent"
	"mizar/internal/builtins"
	"mizar/internal/session"
)

// forkSession 执行分叉：返回给用户看的摘要。失败时新会话文件可能已写入部分
// 消息——写入是逐条 append，部分写入的会话不带 fork 记录、不被 /sessions
// 之外的入口引用，残留无害。
func forkSession(a *agent.Agent, st *session.Store, titleCache *session.TitleCache, sessionID *string, args string) (string, error) {
	if len(a.Initial) == 0 {
		return "", fmt.Errorf("当前会话没有历史可分叉")
	}
	prefix := a.Initial
	aargs := strings.TrimSpace(args)
	if aargs != "" {
		n, err := strconv.Atoi(aargs)
		if err != nil || n < 0 {
			return "", fmt.Errorf("参数须为非负整数（保留的消息条数），收到 %q", aargs)
		}
		if n > len(prefix) {
			n = len(prefix)
		}
		prefix = prefix[:n]
	}
	if len(prefix) == 0 {
		return "", fmt.Errorf("保留 0 条等于空白会话，请用 /session new")
	}
	parentID := *sessionID
	newID := uuid.NewString()
	for _, m := range prefix {
		if err := st.Append(newID, m); err != nil {
			return "", fmt.Errorf("分叉会话写入失败: %v", err)
		}
	}
	// 血缘记录失败不阻断分叉（消息已落盘，只是 /sessions 里看不出父会话）
	_ = st.AppendFork(newID, parentID)

	// 派生状态：todo 依赖随前缀重建
	builtins.SetTodosFromHistory(prefix)
	a.Initial = append([]agent.Message(nil), prefix...)
	*sessionID = newID

	// 标题：沿用源会话标题 + fork 标记；源无标题时从前缀派生
	title := ""
	if titleCache != nil {
		title = titleCache.Get(parentID)
	}
	if title == "" {
		title = session.DeriveTitle(prefix)
	}
	if titleCache != nil && title != "" {
		_ = titleCache.Set(newID, title+" (fork)")
	}

	shortNew, shortOld := newID, parentID
	if len(shortNew) > 8 {
		shortNew = shortNew[:8]
	}
	if len(shortOld) > 8 {
		shortOld = shortOld[:8]
	}
	log.Printf("会话分叉: %s -> %s（携带 %d 条消息）", shortOld, shortNew, len(prefix))
	return fmt.Sprintf("✓ 已分叉: %s ← %s（携带 %d 条消息，源会话 %s 保持不变）\n当前会话已切换为 %s，此后对话写入新会话",
		shortNew, shortOld, len(prefix), shortOld, shortNew), nil
}
