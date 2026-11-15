// 延迟工具加载（C-9，对齐 CodeBuddy ToolSearch）——照抄技能渐进披露（SkillInjectMode: index）思路。
//
// 问题：工具定义全量注入上下文，几十个工具（含插件/MCP）每次请求都要带全部
// schema，占用大量 token 且多数用不到。
//
// 解法（index 模式，opt-in）：
//   - 系统提示只列「核心工具 + 其余工具的索引（名称+一句话描述）」；
//   - 发给 LLM 的 tools 清单也只含核心工具 + tool_search；
//   - 模型需要某个工具时先调 tool_search(query 或 names) → 该工具 schema 进入
//     本会话的活跃集合，下一轮即可正常调用。
//
// 为什么索引是静态的：索引段不随加载变化 → system prompt 前缀保持字节稳定，
// 不破坏缓存（与技能 index 模式同一取舍）。
package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"mizar/internal/plugins"
)

// ToolInjectFull / ToolInjectIndex 工具注入模式。
const (
	ToolInjectFull  = "full"  // 全量注入（默认，现行为）
	ToolInjectIndex = "index" // 索引 + 按需加载
)

// coreToolNames 永远直接注入的工具（任务主干，按需加载会让主循环寸步难行）。
var coreToolNames = map[string]bool{
	"bash": true, "read": true, "write": true, "edit": true,
	"grep": true, "find": true, "ls": true, "respond": true,
	"todo": true, "ask_user_question": true, "skill": true,
	"delegate_task": true, "get_subagent": true, "list_subagents": true,
	"cancel_subagent": true, "steer_subagent": true,
	"tool_search": true, // 索引模式的门闩，自身永远可用
}

// IsCoreTool 报告某工具是否属于核心集（永远直接注入）。
func IsCoreTool(name string) bool { return coreToolNames[name] }

// ToolIndexAgent 侧状态：当前会话已按需加载的工具名集合。
type toolIndexState struct {
	mu     sync.Mutex
	loaded map[string]bool // 已通过 tool_search 加载的工具名
}

// SetToolInjectMode 设置工具注入模式（full 默认 / index 按需）。
func (a *Agent) SetToolInjectMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != ToolInjectIndex {
		mode = ToolInjectFull
	}
	a.toolInjectMode = mode
	a.ReloadTools()
}

// ToolInjectMode 返回当前注入模式（空 = full）。
func (a *Agent) ToolInjectMode() string {
	if a.toolInjectMode == ToolInjectIndex {
		return ToolInjectIndex
	}
	return ToolInjectFull
}

// deferToolsManagers 返回工具注册表实例（nil 安全）。
func (a *Agent) toolManager() *plugins.Manager { return a.Plugins }

// ensureToolIndex 惰性初始化会话级工具索引状态（并发安全的前提：先建再锁）。
func (a *Agent) ensureToolIndex() {
	if a.toolIndex == nil {
		a.toolIndex = &toolIndexState{loaded: map[string]bool{}}
	}
}

// LoadToolForSession 把一个工具标记为本会话已加载（tool_search 成功后调用）。
func (a *Agent) LoadToolForSession(name string) {
	a.ensureToolIndex()
	a.toolIndex.mu.Lock()
	a.toolIndex.loaded[name] = true
	a.toolIndex.mu.Unlock()
}

// IsToolLoadedForSession 报告工具是否已被按需加载（非 index 模式下恒 true）。
func (a *Agent) IsToolLoadedForSession(name string) bool {
	if a.ToolInjectMode() != ToolInjectIndex || IsCoreTool(name) {
		return true
	}
	if a.toolIndex == nil {
		return false
	}
	a.toolIndex.mu.Lock()
	defer a.toolIndex.mu.Unlock()
	return a.toolIndex.loaded[name]
}

// ActiveTools 返回本次请求应发给 LLM 的工具清单。
// full 模式 = 全量；index 模式 = 核心工具 + 本会话已加载的工具。
func (a *Agent) ActiveTools() []plugins.Tool {
	if a.Plugins == nil {
		return nil
	}
	all := a.Plugins.Tools()
	if a.ToolInjectMode() != ToolInjectIndex {
		return all
	}
	a.ensureToolIndex()
	a.toolIndex.mu.Lock()
	defer a.toolIndex.mu.Unlock()
	out := make([]plugins.Tool, 0, len(all))
	for _, t := range all {
		if IsCoreTool(t.Name) || a.toolIndex.loaded[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// DeferredTools 返回 index 模式下被推迟的工具（名称+描述，按名排序）。
func (a *Agent) DeferredTools() []plugins.Tool {
	if a.Plugins == nil || a.ToolInjectMode() != ToolInjectIndex {
		return nil
	}
	all := a.Plugins.Tools()
	a.ensureToolIndex()
	a.toolIndex.mu.Lock()
	defer a.toolIndex.mu.Unlock()
	var out []plugins.Tool
	for _, t := range all {
		if IsCoreTool(t.Name) || a.toolIndex.loaded[t.Name] {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// renderToolIndexSection 生成索引模式的系统提示段（静态、缓存友好）。
func (a *Agent) renderToolIndexSection() string {
	deferred := a.DeferredTools()
	if len(deferred) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 按需加载的工具（延迟注入）\n")
	sb.WriteString("下列工具的完整参数定义尚未注入。需要使用时先调用 `tool_search` 加载：\n")
	sb.WriteString("  tool_search {\"names\": [\"db_query\"]}  或  {\"query\": \"数据库 查询\"}\n")
	sb.WriteString("加载成功后，下一轮即可正常调用该工具（本会话内保持可用）。\n\n")
	for _, t := range deferred {
		desc := t.Description
		if i := strings.IndexAny(desc, "\n"); i >= 0 {
			desc = desc[:i]
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, clampRunes(desc, 100)))
	}
	return sb.String()
}

// clampRunes 按字符数截断（避免切坏多字节字符）。
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ToolSearchTool 返回 tool_search 工具（index 模式下注册）。
func ToolSearchTool(a *Agent) plugins.Tool {
	return plugins.Tool{
		Name: "tool_search",
		Description: "按名称或关键词搜索并加载被延迟注入的工具定义。何时用：系统提示「按需加载的工具」列表里有你需要的能力、" +
			"但该工具还调不到时。参数: {names?: [string] 精确工具名, query?: string 关键词, limit?: int 默认 5}。加载后本会话保持可用。",
		Run: func(args string) (string, error) {
			var p struct {
				Names []string `json:"names"`
				Query string   `json:"query"`
				Limit int      `json:"limit"`
			}
			_ = json.Unmarshal([]byte(args), &p)
			if len(p.Names) == 0 && strings.TrimSpace(p.Query) == "" {
				return "", fmt.Errorf("tool_search: 需要 names 或 query")
			}
			if p.Limit <= 0 {
				p.Limit = 5
			}
			if a.Plugins == nil {
				return "", fmt.Errorf("tool_search: 无工具注册表")
			}
			var hits []plugins.Tool
			var missing []string
			if len(p.Names) > 0 {
				for _, n := range p.Names {
					n = strings.TrimSpace(n)
					if n == "" {
						continue
					}
					if t, ok := a.Plugins.Get(n); ok {
						hits = append(hits, t)
					} else {
						missing = append(missing, n)
					}
				}
			}
			if p.Query != "" {
				hits = append(hits, a.searchToolsByKeyword(p.Query, p.Limit)...)
			}
			// 去重 + 加载
			seen := map[string]bool{}
			var sb strings.Builder
			loaded := 0
			for _, t := range hits {
				if seen[t.Name] {
					continue
				}
				seen[t.Name] = true
				a.LoadToolForSession(t.Name)
				loaded++
				sb.WriteString(fmt.Sprintf("\n### %s\n%s\n", t.Name, t.Description))
			}
			if loaded == 0 {
				msg := "未找到匹配的工具。"
				if len(missing) > 0 {
					msg += "不存在的名字: " + strings.Join(missing, ", ")
				}
				return msg, nil
			}
			var out strings.Builder
			out.WriteString(fmt.Sprintf("✓ 已加载 %d 个工具（下一轮起可调用）：", loaded))
			out.WriteString(sb.String())
			if len(missing) > 0 {
				out.WriteString("\n未找到: " + strings.Join(missing, ", "))
			}
			return out.String(), nil
		},
	}
}

// searchToolsByKeyword 在延迟工具里按关键词模糊匹配（名称命中优先，其次描述）。
func (a *Agent) searchToolsByKeyword(query string, limit int) []plugins.Tool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	terms := strings.Fields(q)
	type scored struct {
		t plugins.Tool
		s int
	}
	var cands []scored
	for _, t := range a.DeferredTools() {
		name := strings.ToLower(t.Name)
		desc := strings.ToLower(t.Description)
		s := 0
		for _, term := range terms {
			if strings.Contains(name, term) {
				s += 3
			}
			if strings.Contains(desc, term) {
				s++
			}
		}
		if s > 0 {
			cands = append(cands, scored{t: t, s: s})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].s != cands[j].s {
			return cands[i].s > cands[j].s
		}
		return cands[i].t.Name < cands[j].t.Name
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]plugins.Tool, len(cands))
	for i, c := range cands {
		out[i] = c.t
	}
	return out
}
