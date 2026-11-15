package builtins

import (
	"encoding/json"
	"fmt"
	"strings"

	"mizar/internal/plugins"
	"mizar/internal/skills"
)

// toolSkillSearch 按关键词检索技能（技能很多时不必全量注入系统提示）。
//
// 何时用：系统提示里只列了最相关的少数技能（+「另有 N 个未列出」），
// 需要不在列表里的技能时先搜一下，再用 `skill` 工具按名加载全文。
// SkillSearchTool 导出给 main.go 注册（需要 *skills.Manager 实例）。
func SkillSearchTool(skm *skills.Manager) plugins.Tool {
	if skm == nil {
		return plugins.Tool{}
	}
	return toolSkillSearch(skm)
}

func toolSkillSearch(skm *skills.Manager) plugins.Tool {
	return plugins.Tool{
		Name: "skill_search",
		Description: "按关键词检索技能库（名称/描述/触发词/分类匹配）。何时用：系统提示的技能列表只含最相关的几条、" +
			"你需要的能力不在其中时。参数: {query: string 关键词, limit?: int 默认 10}。返回名字+描述+路径，用 `skill` 按名加载全文。",
		Run: func(args string) (string, error) {
			var p struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", fmt.Errorf("skill_search: 参数解析失败: %w", err)
			}
			if strings.TrimSpace(p.Query) == "" {
				return "", fmt.Errorf("skill_search: query 不能为空")
			}
			hits := skm.Search(p.Query, p.Limit)
			if len(hits) == 0 {
				return fmt.Sprintf("未找到与「%s」相关的技能（全部技能可用 /skills 查看）", p.Query), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "找到 %d 个技能（用 `skill <name>` 加载全文）：\n", len(hits))
			for _, s := range hits {
				cat := s.Category
				if cat == "" {
					cat = "未分类"
				}
				fmt.Fprintf(&sb, "  • %-24s [%s] %s\n", s.Name, cat, truncate(s.Description, 60))
			}
			return strings.TrimSpace(sb.String()), nil
		},
	}
}
