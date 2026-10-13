package agent

// 压缩摘要的序列化与文件追踪（对齐 pi compaction 的 serializeConversation + Cumulative File Tracking）。
//
// 背景：压缩时切点前的消息要交给 LLM 生成摘要。直接拼接 Content 虽然不丢信息
// （tool_call 的 Content 本身就是 {"action":"tool",...} JSON），但有两处不足：
//  1. 工具调用 JSON 冗长，挤占摘要预算、可读性差 → 渲染成单行 `edit(path="/x")`
//  2. 模型不知道这轮到底读/改了哪些文件 → 累计成 <read-files>/<modified-files> 清单
//
// 两者都只影响摘要质量，不改变消息内容本身。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolResultPreviewMax 工具结果在摘要输入里的最大字符数。
// 工具结果常常很长（读文件、命令输出），摘要只需要知道"做过什么"，不需要全文。
const toolResultPreviewMax = 800

// fileToolReads / fileToolWrites 声明哪些工具会产生文件读写（用于文件追踪）。
var fileToolReads = map[string]bool{"read": true, "files_fuzzy": true}
var fileToolWrites = map[string]bool{"write": true, "edit": true}

// serializeConversation 把消息列表序列化为摘要模型的输入文本。
// 工具调用渲染成单行，工具结果截断，普通消息保留正文。
func serializeConversation(msgs []Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		switch {
		case m.Kind == KindToolCall:
			sb.WriteString("[Assistant tool calls]: ")
			sb.WriteString(serializeToolCallLine(m.ToolName, m.ToolArgs))
			sb.WriteString("\n")
		case m.Kind == KindToolResult:
			sb.WriteString("[Tool result]: ")
			sb.WriteString(truncateRunes(stripToolResultPrefix(m.Content), toolResultPreviewMax))
			sb.WriteString("\n")
		case m.Role == RoleUser:
			sb.WriteString("[User]: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		case m.Role == RoleAssistant:
			sb.WriteString("[Assistant]: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		default:
			sb.WriteString("[" + m.Role + "]: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// serializeToolCallLine 把一次工具调用渲染成单行摘要，如：
//
//	edit(path="/x/main.go", edits=[1 item])
//	read(path="/a.go", offset=10)
//
// 解析失败时退化为 `tool(截断原文)`，不返回空串（至少要保留工具名）。
func serializeToolCallLine(tool, args string) string {
	if tool == "" {
		tool = "tool"
	}
	args = strings.TrimSpace(args)
	if args == "" {
		return tool + "()"
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil || len(m) == 0 {
		return fmt.Sprintf("%s(%s)", tool, truncateRunes(oneLine(args), 120))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+formatArgValue(m[k]))
	}
	return tool + "(" + strings.Join(parts, ", ") + ")"
}

// formatArgValue 渲染单个参数值：字符串带引号并截断，数组/对象只报条目数
// （避免把整个文件内容写进摘要输入）。
func formatArgValue(v any) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("%q", truncateRunes(oneLine(t), 80))
	case []any:
		return fmt.Sprintf("[%d item(s)]", len(t))
	case map[string]any:
		return fmt.Sprintf("{%d key(s)}", len(t))
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// extractFileOps 从一次工具调用中提取文件路径，按工具类型归入已读/已改。
func extractFileOps(tool, args string) (reads, modified []string) {
	isRead, isWrite := fileToolReads[tool], fileToolWrites[tool]
	if !isRead && !isWrite {
		return nil, nil
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Path) == "" {
		return nil, nil
	}
	if isRead {
		return []string{p.Path}, nil
	}
	return nil, []string{p.Path}
}

// collectFileTracking 累计整段消息涉及的文件（去重、保持首次出现顺序）。
func collectFileTracking(msgs []Message) (reads, modified []string) {
	seenR, seenM := map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		if m.Kind != KindToolCall || m.ToolName == "" {
			continue
		}
		r, w := extractFileOps(m.ToolName, m.ToolArgs)
		for _, p := range r {
			if !seenR[p] {
				seenR[p] = true
				reads = append(reads, p)
			}
		}
		for _, p := range w {
			if !seenM[p] {
				seenM[p] = true
				modified = append(modified, p)
			}
		}
	}
	return reads, modified
}

// renderFileTracking 渲染 pi 风格的文件清单段；两者都为空时返回空串（不产生空段）。
func renderFileTracking(reads, modified []string) string {
	if len(reads) == 0 && len(modified) == 0 {
		return ""
	}
	var sb strings.Builder
	if len(reads) > 0 {
		sb.WriteString("<read-files>\n")
		for _, p := range reads {
			sb.WriteString(p + "\n")
		}
		sb.WriteString("</read-files>\n")
	}
	if len(modified) > 0 {
		sb.WriteString("<modified-files>\n")
		for _, p := range modified {
			sb.WriteString(p + "\n")
		}
		sb.WriteString("</modified-files>\n")
	}
	return sb.String()
}

// stripToolResultPrefix 去掉 loop 里给工具结果加的 "工具结果: " 前缀。
func stripToolResultPrefix(s string) string {
	return strings.TrimPrefix(s, "工具结果: ")
}

// oneLine 折叠换行与多余空白为单个空格。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes 按 rune 截断（不切坏多字节字符）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
