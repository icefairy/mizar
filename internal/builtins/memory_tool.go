// 结构化记忆工具（C-7）：读写四分类事实清单。
//
// 存储路由（2026-11-16）：
//   - user/feedback/reference → 全局 ~/.mizar/memory/MEMORY.md（跨项目事实，
//     写入要非常谨慎：仅用户明确要求或确属跨会话的用户级事实）；
//   - project → 项目记忆文件 ~/.mizar/sessions/<路径key>/MEMORY.md
//     （按工作路径分文件，与该项目会话同目录，仿 sessions 结构）。
//
// 与 mem_* 插件工具（HMEM 语义检索）互补：这里是小而精、每会话自动注入的
// 固定分类事实；海量/易变的语义记忆优先存 mem_*，不要堆进全局 MEMORY.md。
package builtins

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"mizar/internal/memory"
	"mizar/internal/plugins"
)

// MemoryTool 返回结构化记忆工具。projectStore 是当前项目的项目记忆存储
//（nil=无项目上下文，project 条目退回全局文件）。onChanged 在写操作成功后
// 调用（供调用方刷新 Agent 的系统提示注入，nil 则忽略）。
func MemoryTool(store *memory.Store, projectStore *memory.Store, onChanged func()) plugins.Tool {
	if store == nil {
		return plugins.Tool{}
	}
	// target 按分类路由存储：project → 项目文件，其余 → 全局。
	target := func(cat string) *memory.Store {
		if cat == memory.CategoryProject && projectStore != nil {
			return projectStore
		}
		return store
	}
	return plugins.Tool{
		Name: "memory",
		Description: "读写结构化长期记忆。四类：user 用户偏好 / feedback 用户反馈 / " +
			"project 项目事实 / reference 参考资料。" +
			"路由：project 写入当前项目的记忆文件（按项目路径分文件）；其余写全局 MEMORY.md。" +
			"写入全局要非常谨慎——仅当用户明确要求或确属跨项目的用户级事实时才 add；" +
			"项目相关的用 project；海量的语义记忆优先用 mem_* 插件工具，不要堆进这里。" +
			"参数: {action: add|list|remove|count, category?: string, text?: string, index?: int}",
		Run: func(args string) (string, error) {
			var p struct {
				Action   string `json:"action"`
				Category string `json:"category"`
				Text     string `json:"text"`
				Index    int    `json:"index"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("memory: 解析参数失败: %w", err)
			}
			action := strings.ToLower(strings.TrimSpace(p.Action))
			if action == "" {
				action = "list"
			}
			switch action {
			case "add", "write", "save":
				if strings.TrimSpace(p.Text) == "" {
					return "", fmt.Errorf("memory: add 需要 text")
				}
				cat := memory.NormalizeCategory(p.Category)
				t := target(cat)
				if err := t.Add(cat, p.Text); err != nil {
					return "", err
				}
				if onChanged != nil {
					onChanged()
				}
				return fmt.Sprintf("✓ 已记入 %s 分类（%s）", cat, t.Path()), nil
			case "list", "show":
				m, err := store.List(p.Category)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				total := 0
				writeSection := func(cat string, items []string) {
					if len(items) == 0 {
						return
					}
					sb.WriteString(fmt.Sprintf("## %s\n", cat))
					for i, it := range items {
						sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, it))
						total++
					}
				}
				for _, cat := range memory.Categories() {
					items, ok := m[cat]
					if !ok || len(items) == 0 {
						continue
					}
					if cat == memory.CategoryProject && projectStore != nil {
						// project 分类展示项目记忆文件的内容
						pm, err := projectStore.List(memory.CategoryProject)
						if err == nil {
							writeSection(cat, pm[memory.CategoryProject])
							continue
						}
					}
					writeSection(cat, items)
				}
				if total == 0 {
					return "（结构化记忆为空；用 memory action=add 记录）", nil
				}
				return strings.TrimSpace(sb.String()), nil
			case "remove", "delete", "rm":
				if p.Index <= 0 {
					return "", fmt.Errorf("memory: remove 需要 index（1-based，取自 list 序号）")
				}
				cat := memory.NormalizeCategory(p.Category)
				t := target(cat)
				ok, err := t.Remove(cat, p.Index)
				if err != nil {
					return "", err
				}
				if !ok {
					return "", fmt.Errorf("memory: 删除失败")
				}
				if onChanged != nil {
					onChanged()
				}
				return "✓ 已删除", nil
			case "count":
				n, err := store.Count()
				if err != nil {
					return "", err
				}
				if projectStore != nil {
					pn, err := projectStore.Count()
					if err != nil {
						return "", err
					}
					n += pn
				}
				return fmt.Sprintf("结构化记忆共 %d 条（全局 %s%s）", n, store.Path(), func() string {
					if projectStore != nil {
						return " + 项目 " + projectStore.Path()
					}
					return ""
				}()), nil
			default:
				keys := append([]string{}, "add", "list", "remove", "count")
				sort.Strings(keys)
				return "", fmt.Errorf("memory: 未知 action %q（支持 %s）", action, strings.Join(keys, "/"))
			}
		},
	}
}
