package plugins

import (
	"regexp"
	"strings"
)

// 从 Tool.Description 中解析 Args 模式生成 JSON Schema，供 OpenAI function calling 使用。
// Description 约定格式（对模型友好，同时可编程解析）：
//
//	"Args: {command: string, timeout?: number(seconds)}"
//
// 支持：
//   - 字段名: string/number/integer/boolean/array/object/any
//   - 可选字段：名后加 ?（如 timeout?）
//   - 可读注释：(seconds)、(number) 等括号内容作为 description 补充
//   - 顶层参数名省略时的隐式 {args: ...} 兜底

var argsRe = regexp.MustCompile(`(?s)Args:\s*\{([^}]*)\}`)

// typeHintRe 匹配字段注释，如 "timeout?: number(seconds)" → name=timeout, type=number, note=seconds
var typeHintRe = regexp.MustCompile(`^\s*(\w+)\??\s*:\s*(\w+)(?:\(([^)]*)\))?\s*$`)

// ArgsSchema 从 Description 提取 Args JSON Schema；解析失败返回 nil。
func (t Tool) ArgsSchema() map[string]any {
	m := argsRe.FindStringSubmatch(t.Description)
	if len(m) < 2 {
		return nil
	}
	inner := m[1]
	props := map[string]any{}
	var required []string
	for _, part := range splitTopLevel(inner) {
		sm := typeHintRe.FindStringSubmatch(part)
		if len(sm) < 3 {
			continue
		}
		name, typ, note := sm[1], sm[2], ""
		if len(sm) > 3 {
			note = sm[3]
		}
		p := map[string]any{
			"type":        jsonType(typ),
			"description": fieldDesc(name, note),
		}
		props[name] = p
		if !strings.HasSuffix(strings.TrimSpace(part), "?,") &&
			!strings.Contains(strings.TrimSpace(part), "?,") &&
			!strings.HasSuffix(strings.TrimSpace(part), "?") &&
			!strings.Contains(strings.TrimSpace(part), "?") {
			// 可选字段是 name? 或 name?:
			if !strings.Contains(strings.TrimSpace(part), "?") {
				required = append(required, name)
			}
		}
	}
	if len(props) == 0 {
		// 无显式字段，兜底 {args: string}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"args": map[string]any{"type": "string", "description": "Arguments JSON string"},
			},
			"required": []string{"args"},
		}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	} else {
		out["additionalProperties"] = true
	}
	return out
}

func jsonType(t string) string {
	switch strings.ToLower(t) {
	case "string", "str", "text", "path", "pattern", "command":
		return "string"
	case "number", "float", "double":
		return "number"
	case "integer", "int", "limit", "offset", "count":
		return "integer"
	case "boolean", "bool":
		return "boolean"
	case "array", "list", "[]":
		return "array"
	case "object", "json", "map":
		return "object"
	default:
		return "string"
	}
}

func fieldDesc(name, note string) string {
	if note != "" {
		return strings.TrimSpace(note)
	}
	return "Parameter " + name
}

// splitTopLevel 按顶层逗号分割（不含括号内的逗号）
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	var cur strings.Builder
	for _, r := range s {
		switch r {
		case '{', '(':
			depth++
		case '}', ')':
			depth--
		}
		if r == ',' && depth == 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
