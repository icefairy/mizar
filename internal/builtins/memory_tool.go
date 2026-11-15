// 结构化记忆工具（C-7）：读写 ~/.mizar/memory/MEMORY.md 的四分类事实清单。
//
// 与 mem_* 插件工具（HMEM 语义检索）互补：这里是小而精、每会话自动注入的
// 固定分类事实；mem_* 是海量记忆的按需检索。
package builtins

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"mizar/internal/memory"
	"mizar/internal/plugins"
)

// MemoryTool 返回结构化记忆工具。onChanged 在写操作成功后调用（供调用方
// 刷新 Agent 的系统提示注入，nil 则忽略）。
func MemoryTool(store *memory.Store, onChanged func()) plugins.Tool {
	if store == nil {
		return plugins.Tool{}
	}
	return plugins.Tool{
		Name: "memory",
		Description: "读写结构化长期记忆（~/.mizar/memory/MEMORY.md，四类：user 用户偏好 / feedback 用户反馈 / " +
			"project 项目事实 / reference 参考资料）。何时用：用户告知了需跨会话保留的偏好或事实时用 action=add 记录；" +
			"需要回忆已知约定时用 action=list。参数: {action: add|list|remove|count, category?: string, text?: string, index?: int}",
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
				if err := store.Add(cat, p.Text); err != nil {
					return "", err
				}
				if onChanged != nil {
					onChanged()
				}
				return fmt.Sprintf("✓ 已记入 %s 分类（%s）", cat, store.Path()), nil
			case "list", "show":
				m, err := store.List(p.Category)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				total := 0
				for _, cat := range memory.Categories() {
					items, ok := m[cat]
					if !ok || len(items) == 0 {
						continue
					}
					sb.WriteString(fmt.Sprintf("## %s\n", cat))
					for i, it := range items {
						sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, it))
						total++
					}
				}
				if total == 0 {
					return "（结构化记忆为空；用 memory action=add 记录）", nil
				}
				return strings.TrimSpace(sb.String()), nil
			case "remove", "delete", "rm":
				if p.Index <= 0 {
					return "", fmt.Errorf("memory: remove 需要 index（1-based，取自 list 序号）")
				}
				ok, err := store.Remove(p.Category, p.Index)
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
				return fmt.Sprintf("结构化记忆共 %d 条（%s）", n, store.Path()), nil
			default:
				keys := append([]string{}, "add", "list", "remove", "count")
				sort.Strings(keys)
				return "", fmt.Errorf("memory: 未知 action %q（支持 %s）", action, strings.Join(keys, "/"))
			}
		},
	}
}
