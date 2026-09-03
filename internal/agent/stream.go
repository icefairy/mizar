package agent

import (
	"context"
	"strconv"
	"strings"

	"mizar/internal/plugins"
)

// StreamDelta LLM 流式回复分片。
// 模型在流式过程中逐步吐出内容；Thinking（思考过程）与 Content（回复内容）
// 可能同时为空（如原生 tool_calls 的增量分片），UI 侧据此做增量渲染。
type StreamDelta struct {
	Thinking string // 思考过程分片（如 deepseek reasoning_content），非最终回复
	Content  string // 回复内容分片（含控制 JSON，UI 需自行提取可显示文本）
}

// StreamLLM 可选接口：支持流式回复的 LLM（普通对话，无 tools 参数）。
// 实现时逐分片回调 onToken，同时返回完整回复文本。
type StreamLLM interface {
	// ChatStream 发送消息并流式接收回复分片；回调在请求 goroutine 内同步执行，必须快速返回。
	ChatStream(messages []Message, onToken func(StreamDelta)) (string, error)
}

// StreamToolCallLLM 可选接口：支持流式回复 + 原生工具调用的 LLM。
type StreamToolCallLLM interface {
	// ChatWithToolsStream 发送消息 + 工具定义并流式接收回复分片。
	// 原生 tool_calls 也以分片形式回调（Content 为空），返回完整回复（含工具调用 JSON 文本）。
	ChatWithToolsStream(messages []Message, tools []plugins.Tool, onToken func(StreamDelta)) (string, error)
}

// CancellableStreamToolCallLLM 可选接口：支持流式回复 + 原生工具调用，且请求可被上下文取消。
// 代理循环的 ESC 取消（Agent.Abort）会取消 Run 上下文；实现方应把该上下文绑定到 HTTP
// 请求，使在途流式读取能立即中断（否则任务 goroutine 阻塞到流自然结束，取消不生效）。
type CancellableStreamToolCallLLM interface {
	ChatWithToolsStreamCtx(ctx context.Context, messages []Message, tools []plugins.Tool, onToken func(StreamDelta)) (string, error)
}

// StreamTextExtractor 增量提取模型流式输出中的「可显示回复文本」。
//
// 背景：mizar 的回复协议里，模型每一步的输出都是 JSON 控制文本
// （{"action":"tool",...} 或 {"action":"reply","text":"..."}）。直接渲染
// 会把 JSON 花括号、转义符都显示出来。提取器按增量解析：
//   - action 尚未出现 → 不返回任何内容（保持等待状态）
//   - action=tool      → 控制文本，不显示
//   - action=reply     → 返回 text 字段的增量（解码 \n / \" / \\ / \uXXXX），
//     配合流式逐 token 渲染，UI/Server 看到的就是干净的回复文本。
//
// 供 CLI（TUI/readline）与 Server（SSE）共用；与 parseCallJSON 同源的协议理解。
type StreamTextExtractor struct {
	raw   strings.Builder // 当前步原始内容累积（含控制 JSON）
	shown string          // 已提取并显示的文本（用于增量 diff）
	text  strings.Builder // 累计可显示文本（供整体渲染）
	inRep bool            // 当前步是否已确认是 reply
	rep   bool            // 当前步是否确认需要显示（reply 且 text 非空）
}

// Reset 重置为新步骤（agent 循环每步 LLM 调用开始时调用）。
func (e *StreamTextExtractor) Reset() {
	e.raw.Reset()
	e.shown = ""
	e.text.Reset()
	e.inRep = false
	e.rep = false
}

// Feed 追加一个内容分片，返回该分片新增的可显示文本。
// 调用方将返回值追加到终端/TUI 行 / SSE 流即可实现流式显示。
func (e *StreamTextExtractor) Feed(delta string) string {
	if delta == "" {
		return ""
	}
	e.raw.WriteString(delta)
	s := e.raw.String()

	switch jsonValueOf(s, "action") {
	case "tool":
		// 工具调用控制文本：不显示
		e.inRep = false
		return ""
	case "reply":
		if !e.inRep {
			e.inRep = true
			e.shown = ""
			e.text.Reset()
		}
		t := jsonValueOf(s, "text")
		if strings.HasPrefix(t, e.shown) {
			inc := t[len(e.shown):]
			e.text.WriteString(inc)
			e.shown = t
			if inc != "" {
				e.rep = true
			}
			return inc
		}
		// 非前缀（模型中途改写/重发）：整体替换
		e.text.Reset()
		e.text.WriteString(t)
		e.shown = t
		if t != "" {
			e.rep = true
		}
		return t
	default:
		// action 字段尚未完整到达：不显示（等待）
		return ""
	}
}

// Text 返回当前步累计的可显示文本（供 TUI 整体渲染 / 结束时兜底）。
func (e *StreamTextExtractor) Text() string { return e.text.String() }

// Replying 当前步是否已确认有可显示的回复内容。
func (e *StreamTextExtractor) Replying() bool { return e.rep }

// IsToolCallText 判断文本是否指向工具调用（顶层 action 字段值为 "tool"）。
// 容忍空白与畸形 JSON（jsonValueOf 逐字符扫描，不依赖整体 JSON 合法性）。
// 供 LLM 层识别「模型想调工具但 JSON 畸形」的场景：
// 若不识别，畸形 tool JSON 会被包装成 reply 导致用户看到原始 JSON、任务提前结束。
func IsToolCallText(s string) bool {
	return jsonValueOf(s, "action") == "tool"
}

// jsonValueOf 提取 JSON 顶层字段的字符串值（容忍未闭合 / 转义，供流式增量用）。
// 从首个 '{' 之后查找 "key": 模式（与 parseCallJSON 取段逻辑一致），
// 返回解码后的值；字符串未闭合时解到流末尾。查找不到返回 ""。
func jsonValueOf(s, key string) string {
	brace := strings.Index(s, "{")
	if brace < 0 {
		return ""
	}
	p := strings.Index(s[brace:], `"`+key+`":`)
	if p < 0 {
		return ""
	}
	p += brace + len(key) + 3 // 跳过 "key":
	for p < len(s) && (s[p] == ' ' || s[p] == '\t' || s[p] == '\n') {
		p++
	}
	if p >= len(s) || s[p] != '"' {
		return ""
	}
	p++ // 跳过开引号
	var vb strings.Builder
	for p < len(s) {
		c := s[p]
		if c == '\\' && p+1 < len(s) {
			switch s[p+1] {
			case 'n':
				vb.WriteByte('\n')
				p += 2
			case 't':
				vb.WriteByte('\t')
				p += 2
			case 'r':
				vb.WriteByte('\r')
				p += 2
			case '"':
				vb.WriteByte('"')
				p += 2
			case '\\':
				vb.WriteByte('\\')
				p += 2
			case '/':
				vb.WriteByte('/')
				p += 2
			case 'u':
				if p+5 < len(s) {
					if r, err := strconv.ParseUint(s[p+2:p+6], 16, 32); err == nil {
						vb.WriteRune(rune(r))
						p += 6
						continue
					}
				}
				vb.WriteByte('\\')
				p++
			default:
				vb.WriteByte('\\')
				vb.WriteByte(s[p+1])
				p += 2
			}
			continue
		}
		if c == '"' {
			return vb.String() // 顶层字符串闭合
		}
		vb.WriteByte(c)
		p++
	}
	return vb.String() // 流未闭合：返回已解码内容
}
