package server

import (
	"strings"

	"mizar/internal/agent"
	"mizar/internal/llm"
)

// ============================================================================
// 自动会话标题：对话超过 3 条消息后，用小模型把用户提问 + AI 回答提炼成
// 一句 12 字以内的主题，作为左侧会话列表标题（浏览器标题也会同步）。
//
// 设计要点：
//   - 只在「消息条数达到阈值且比上次提炼时又多了消息」时才调用模型，避免每轮都花钱；
//   - 用户可以手动重命名，之后（TitleManual）不再被自动覆盖；
//   - 提炼模型可在设置里单独指定（settings.title_model）；留空则用当前主模型。
//   - 失败静默：标题提炼是锦上添花，绝不能影响正常对话。
// ============================================================================

const (
	titleMinMsgs  = 3  // 消息条数超过该值才自动提炼（>3 条由调用处判断）
	titleMaxRunes = 12 // 标题最大字数
)

// maybeAutoTitle 异步尝试为该会话生成/刷新自动标题。
func (s *Server) maybeAutoTitle(convID string) {
	if convID == "" || s.web == nil {
		return
	}
	c := s.web.store.get(convID)
	if c == nil {
		return
	}
	if !shouldAutoTitle(c) {
		return
	}
	go s.genTitle(convID, c, len(c.Messages))
}

// shouldAutoTitle 判断该会话此刻是否需要（重新）自动生成标题。
// 纯函数，便于单测：
//   - 消息条数必须超过 titleMinMsgs（需求：超过 3 条才总结）；
//   - 用户手动改过标题（TitleManual）则永不覆盖；
//   - 已生成过且新增消息不足 2 条时跳过，省模型调用。
func shouldAutoTitle(c *conversation) bool {
	if c == nil {
		return false
	}
	n := len(c.Messages)
	if n <= titleMinMsgs {
		return false
	}
	if c.TitleManual {
		return false
	}
	if c.TitleGenAt > 0 && n-c.TitleGenAt < 2 {
		return false
	}
	return true
}

// genTitle 调模型提炼标题并写回存储。
func (s *Server) genTitle(convID string, c *conversation, genAt int) {
	defer func() { _ = recover() }()
	client := s.agent.LLM
	if client == nil {
		return
	}
	// 只取最近 12 条消息，控制成本
	msgs := c.Messages
	if len(msgs) > 12 {
		msgs = msgs[len(msgs)-12:]
	}
	var sb strings.Builder
	for _, m := range msgs {
		role := "用户"
		if m.Role == "assistant" {
			role = "AI"
		}
		txt := strings.TrimSpace(m.Content)
		if txt == "" {
			continue
		}
		if r := []rune(txt); len(r) > 300 {
			txt = string(r[:300]) + "…"
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(txt)
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return
	}

	set := s.web.store.getSettings()
	titleModel := strings.TrimSpace(set.TitleModel)

	sys := "你是会话标题生成器。根据下面的对话内容，提炼一个不超过12个字的中文主题短语。" +
		"要求：只输出标题本身，不要引号、不要标点结尾、不要解释、不要前缀。"

	req := llm.AIChatRequest{
		System:   sys,
		Thinking: "off",
		Messages: []llm.AIChatMsg{{Role: agent.RoleUser, Content: sb.String()}},
	}
	// 单独指定的小模型（留空用主模型 —— AIChat 内部空 Model 即用当前模型）
	req.Model = titleModel
	req.MaxTokens = 32

	var out string
	var err error
	if ai, ok := client.(interface {
		AIChat(llm.AIChatRequest) (string, error)
	}); ok {
		out, err = ai.AIChat(req)
	} else {
		return
	}
	if err != nil {
		s.logf("auto title 失败: %v", err)
		return
	}
	title := sanitizeTitle(out)
	if title == "" {
		return
	}
	if s.web.store.setAutoTitle(convID, title, genAt) {
		s.logf("auto title: %s → %s", convID, title)
	}
}

// sanitizeTitle 清洗模型输出：去引号/换行/首尾空白，截断到 titleMaxRunes 字。
func sanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	// 只取第一行
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'“”‘’「」『』《》")
	s = strings.Trim(s, "。.，,、：:；;！!？? ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > titleMaxRunes {
		r = r[:titleMaxRunes]
	}
	return strings.TrimSpace(string(r))
}
