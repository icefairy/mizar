// 用户自定义子代理类型（GAP-S1 第二层，对齐 CodeBuddy .codebuddy/agents/ 自定义 agent 定义）。
//
// 内置四角色（leaf/explore/implement/review）是行为预设 + 只读白名单；
// 本层让用户按项目定义具名类型：`~/.mizar/agents/*.md`，frontmatter 声明
// 工具白名单与默认模型，正文是该类型的 system 附加层：
//
//	---
//	name: tester
//	description: 跑测试并归因失败用例
//	tools: [bash, read, grep]   # 白名单；缺省 = 全量（按内置角色规则）
//	model: glm-4-flash          # 该类型默认模型；缺省 = 继承主模型
//	---
//	你是测试型子代理。只跑测试与收集失败证据，不修改被测代码。
//
// delegate_task 的 role 传类型名即命中；加载与 SKILL.md 生态同构，
// 与 per-task 换模型（req.Model）共用同一条 modelDeriver 派生路径。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentType 一个用户自定义子代理类型。
type AgentType struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tools       []string `json:"tools,omitempty"` // 工具白名单；空 = 全量共享注册表
	Model       string   `json:"model,omitempty"` // 类型默认模型；空 = 继承
	System      string   `json:"system,omitempty"` // 正文：注入子代理任务指令的行为边界
}

// LoadAgentTypeFiles 加载目录下全部 *.md 类型定义（目录不存在返回 nil, nil——
// 没有自定义类型是常态，不算错误）。解析失败的文件跳过并随 error 列表返回，
// 不阻断其余类型。
func LoadAgentTypeFiles(dir string) ([]AgentType, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // 目录不存在 = 无自定义类型
	}
	var out []AgentType
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("agents: 读取 %s: %w", e.Name(), err))
			continue
		}
		at, err := parseAgentTypeMD(string(b))
		if err != nil {
			errs = append(errs, fmt.Errorf("agents: 解析 %s: %w", e.Name(), err))
			continue
		}
		out = append(out, at)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// parseAgentTypeMD 解析单个类型定义：frontmatter（name/description/tools/model）
// + 正文（system 附加层）。name 必填，重名由 SetAgentTypes 去重（后写胜出）。
func parseAgentTypeMD(src string) (AgentType, error) {
	src = strings.TrimSpace(strings.TrimPrefix(src, "\uFEFF"))
	if !strings.HasPrefix(src, "---") {
		return AgentType{}, fmt.Errorf("缺少 frontmatter（文件须以 --- 开头）")
	}
	rest := src[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return AgentType{}, fmt.Errorf("frontmatter 未闭合（缺第二行 ---）")
	}
	fm := rest[:end]
	body := strings.TrimSpace(rest[end+4:])

	var at AgentType
	at.System = body
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "name":
			at.Name = v
		case "description":
			at.Description = v
		case "model":
			at.Model = v
		case "tools":
			at.Tools = parseToolsList(v)
		}
	}
	if at.Name == "" {
		return AgentType{}, fmt.Errorf("frontmatter 缺少 name")
	}
	return at, nil
}

// parseToolsList 解析 tools 字段：支持 [a, b, c] 与 a,b,c 两种写法。
func parseToolsList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(strings.Trim(part, "\"'"))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SetAgentTypes 注册自定义类型（main.go 启动时加载后调用；重名后写胜出）。
func (sm *SubagentManager) SetAgentTypes(types []AgentType) {
	sm.mu.Lock()
	m := make(map[string]AgentType, len(types))
	for _, at := range types {
		m[strings.ToLower(at.Name)] = at
	}
	sm.customTypes = m
	sm.mu.Unlock()
}

// AgentTypes 返回已注册类型（按名称排序；delegate_task 描述列清单用）。
func (sm *SubagentManager) AgentTypes() []AgentType {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]AgentType, 0, len(sm.customTypes))
	for _, at := range sm.customTypes {
		out = append(out, at)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// customType 按 role 名查自定义类型（大小写不敏感）。
func (sm *SubagentManager) customType(role string) (AgentType, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	at, ok := sm.customTypes[strings.ToLower(strings.TrimSpace(role))]
	return at, ok
}
