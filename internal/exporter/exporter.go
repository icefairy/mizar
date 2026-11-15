// Package exporter 把会话导出为自包含 HTML（P-6，对齐 pi `--export` + highlight.js 归档）。
//
// 设计取舍：
//   - 单文件自包含：CSS 内联、无外部 JS/CDN（内网离线可用），代码高亮用
//     极简正则着色（够用即可，不追求完整语法树）。
//   - 输入是 []agent.Message（会话实际消息），不做任何 LLM 调用。
//   - 工具调用/结果显示为折叠块（details），避免长输出淹没对话。
//   - 所有文本都做 HTML 转义（防 XSS：会话内容可能包含任意网络内容）。
package exporter

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	"mizar/internal/agent"
)

// Options 导出参数。
type Options struct {
	Title     string    // 页面标题（空=Session）
	SessionID string    // 会话 id（页脚展示）
	Model     string    // 模型名（页脚展示，可选）
	Exported  time.Time // 导出时间（零值=now）
}

// ToHTML 把消息列表渲染成完整 HTML 文档。
func ToHTML(msgs []agent.Message, opt Options) string {
	if opt.Title == "" {
		opt.Title = "Session"
	}
	if opt.Exported.IsZero() {
		opt.Exported = time.Now()
	}
	var b strings.Builder
	b.WriteString(htmlHead(opt.Title))
	b.WriteString("<body>\n")
	fmt.Fprintf(&b, "<header><h1>%s</h1>", html.EscapeString(opt.Title))
	if opt.SessionID != "" {
		fmt.Fprintf(&b, `<div class="meta">会话: <code>%s</code></div>`, html.EscapeString(shortID(opt.SessionID)))
	}
	meta := "导出: " + opt.Exported.Format("2006-01-02 15:04:05")
	if opt.Model != "" {
		meta += " ｜ 模型: " + html.EscapeString(opt.Model)
	}
	fmt.Fprintf(&b, `<div class="meta">%s</div></header>`+"\n", html.EscapeString(meta))
	b.WriteString(`<main>` + "\n")

	// 按 Kind 分流渲染
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Kind {
		case agent.KindToolCall:
			// 收集紧随其后的工具结果
			var results []agent.Message
			j := i + 1
			for ; j < len(msgs) && msgs[j].Kind == agent.KindToolResult; j++ {
				results = append(results, msgs[j])
			}
			renderToolCall(&b, m, results)
			i = j - 1
		case agent.KindToolResult:
			// 孤立的结果（前面没有 tool_call）：按普通块渲染
			renderToolResult(&b, m)
		case agent.KindSummary:
			fmt.Fprintf(&b, `<section class="summary"><h3>会话摘要（压缩）</h3><div>%s</div></section>`+"\n", formatText(m.Content))
		default:
			renderMessage(&b, m)
		}
	}

	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

func renderMessage(b *strings.Builder, m agent.Message) {
	cls, label := "assistant", "助手"
	switch m.Role {
	case agent.RoleUser:
		cls, label = "user", "用户"
	case agent.RoleSystem:
		cls, label = "system", "系统"
	}
	ts := ""
	if m.Timestamp != "" {
		ts = `<span class="ts">` + html.EscapeString(shortTime(m.Timestamp)) + `</span>`
	}
	fmt.Fprintf(b, `<section class="msg %s"><div class="who">%s %s</div><div class="body">%s</div></section>`+"\n",
		cls, label, ts, formatText(m.Content))
}

func renderToolCall(b *strings.Builder, call agent.Message, results []agent.Message) {
	name := call.ToolName
	if name == "" {
		name = "tool"
	}
	fmt.Fprintf(b, `<details class="tool"><summary>🔧 %s</summary>`+"\n", html.EscapeString(name))
	if args := strings.TrimSpace(call.ToolArgs); args != "" {
		fmt.Fprintf(b, `<div class="args"><div class="sub">参数</div><pre>%s</pre></div>`+"\n", html.EscapeString(args))
	}
	for _, r := range results {
		renderToolResultInner(b, r)
	}
	b.WriteString("</details>\n")
}

func renderToolResult(b *strings.Builder, m agent.Message) {
	b.WriteString(`<details class="tool"><summary>🔧 工具结果</summary>` + "\n")
	renderToolResultInner(b, m)
	b.WriteString("</details>\n")
}

func renderToolResultInner(b *strings.Builder, m agent.Message) {
	content := resultBody(m.Content)
	if content == "" {
		return
	}
	cls := "result"
	if strings.HasPrefix(content, "error") || strings.Contains(content, `"error"`) {
		cls = "result error"
	}
	fmt.Fprintf(b, `<div class="%s"><div class="sub">结果</div><pre>%s</pre></div>`+"\n", cls, html.EscapeString(truncateText(content, 8000)))
}

// resultBody 从工具结果消息的 Content 中提取可读正文。
// 会话里 KindToolResult 的 Content 形如 `工具结果: {"tool_name":"x","args":"...","output":"...","error":"..."}`。
func resultBody(content string) string {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "工具结果: ") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "工具结果: "))
	}
	if !strings.HasPrefix(s, "{") {
		return s
	}
	var tr struct {
		ToolName string `json:"tool_name"`
		Args     string `json:"args"`
		Output   string `json:"output"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &tr); err != nil {
		return s
	}
	if tr.Error != "" {
		out := tr.Output
		if out != "" {
			out += "\n"
		}
		return out + "error: " + tr.Error
	}
	return tr.Output
}

// formatText 把纯文本转成 HTML：保留换行（CSS pre-wrap）、转义、简单代码高亮。
func formatText(s string) string {
	if s == "" {
		return ""
	}
	// 检测 ``` 代码块并高亮
	var out strings.Builder
	lines := strings.Split(s, "\n")
	inFence := false
	var codeBuf strings.Builder
	flushCode := func() {
		if codeBuf.Len() > 0 {
			out.WriteString(`<pre class="code"><code>`)
			out.WriteString(highlight(codeBuf.String()))
			out.WriteString(`</code></pre>`)
			codeBuf.Reset()
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				flushCode()
				inFence = false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			codeBuf.WriteString(line)
			codeBuf.WriteString("\n")
			continue
		}
		out.WriteString(html.EscapeString(line))
		out.WriteString("\n")
	}
	if inFence {
		flushCode()
	}
	return out.String()
}

// highlight 极简代码着色：注释、字符串、关键字（保守，不破坏 HTML 结构。
//
// 注意：输入已经过 html.EscapeString，因此这里匹配的是转义后的文本，
// 高亮只插入 <span>，不会再引入未转义的 <>。
func highlight(code string) string {
	escaped := html.EscapeString(code)
	// 关键字（Go/JS/Python 常见词），用词边界近似（前后非字母数字）
	keywords := []string{"func", "return", "if", "else", "for", "while", "var", "const", "let",
		"package", "import", "type", "struct", "interface", "class", "def", "true", "false", "nil", "null"}
	for _, kw := range keywords {
		escaped = replaceKeywords(escaped, kw)
	}
	return escaped
}

func replaceKeywords(s, kw string) string {
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(s[i:], kw)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		abs := i + idx
		before := byte(' ')
		if abs > 0 {
			before = s[abs-1]
		}
		after := byte(' ')
		if end := abs + len(kw); end < len(s) {
			after = s[end]
		}
		if !isWordByte(before) && !isWordByte(after) {
			b.WriteString(s[i:abs])
			b.WriteString(`<span class="kw">` + kw + `</span>`)
			i = abs + len(kw)
		} else {
			b.WriteString(s[i : abs+len(kw)])
			i = abs + len(kw)
		}
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…（已截断，共 " + fmt.Sprintf("%d", len(s)) + " 字节）"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func shortTime(ts string) string {
	// RFC3339 带毫秒：取 "YYYY-MM-DD HH:MM"
	if len(ts) >= 16 {
		t := strings.Replace(ts[:16], "T", " ", 1)
		return t
	}
	return ts
}

func htmlHead(title string) string {
	return `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<style>
:root{--bg:#0d1117;--fg:#e6edf3;--muted:#8b949e;--user:#1f6feb22;--ai:#23863622;--border:#30363d;--code:#161b22}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.6 -apple-system,Segoe UI,Roboto,"PingFang SC","Microsoft YaHei",sans-serif}
header{padding:24px 20px;border-bottom:1px solid var(--border);position:sticky;top:0;background:var(--bg);z-index:1}
header h1{margin:0 0 6px;font-size:20px}
.meta{color:var(--muted);font-size:13px}
main{max-width:900px;margin:0 auto;padding:20px}
section.msg{border:1px solid var(--border);border-radius:10px;padding:12px 14px;margin:12px 0}
section.user{background:var(--user)}
section.assistant{background:var(--ai)}
section.system{background:#2d333b55}
.who{font-weight:600;font-size:13px;color:var(--muted);margin-bottom:6px}
.ts{font-weight:400;margin-left:8px;font-size:12px}
.body{white-space:pre-wrap;word-break:break-word}
pre{background:var(--code);border:1px solid var(--border);border-radius:8px;padding:10px;overflow:auto;margin:6px 0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:13px}
.kw{color:#ff7b72}
details.tool{border:1px dashed var(--border);border-radius:8px;margin:10px 0;padding:6px 10px;background:#161b2255}
details.tool summary{cursor:pointer;color:var(--muted);font-size:13px}
.sub{color:var(--muted);font-size:12px;margin-top:6px}
.result.error{border-color:#f85149}
section.summary{border-left:3px solid #d29922;padding:8px 12px;margin:12px 0;color:var(--muted);font-size:14px}
section.summary h3{margin:0 0 6px;font-size:14px}
</style>
</head>`
}
