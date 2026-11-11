package server

// ask.go —— Web 侧的 ask_user_question 问答通道。
//
// 背景：ask_user_question / exit_plan_mode 这类工具需要「阻塞等待人类选择」。
// TUI 通过聊天区提问（cmd/mizar/tui.go 的 askViaChat），但 Server/WebUI 模式下
// builtins.SetAskUser 为 nil，工具只能降级成「请自行决策」——WebUI 用户根本
// 无法选择。
//
// 本文件把这条通道搬到 Web：
//   1. 工具调用 → Server.AskUser(questionsJSON) 创建一条 pending（含 id + channel），
//      通过 WS 广播 ask_user 事件，然后阻塞等待；
//   2. 前端弹出选项框，用户选完 → POST /web/api/answer {id, answers}；
//   3. 服务端把 answers JSON 塞回 channel，工具拿到结果继续跑。
//
// 关键约束：**不设超时**——用户可能几分钟/几小时后才回来选，题目过期比多等更糟。
// 唯一的退出路径是「用户作答」与「任务被 /stop 或中止取消」（此时返回 "[]"）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"mizar/internal/builtins"
)

// pendingAsk 一次待回答的提问。
type pendingAsk struct {
	id        string
	convID    string // 归属会话，跨会话的提交会被拒绝
	questions string // 原始 questions JSON（内含选项，前端直接渲染）
	answerCh  chan string
	created   time.Time
}

// askBroker 管理所有待答问题（线程安全）。
type askBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingAsk
	seq     int
}

func newAskBroker() *askBroker {
	return &askBroker{pending: make(map[string]*pendingAsk)}
}

func (b *askBroker) add(p *pendingAsk) {
	b.mu.Lock()
	b.pending[p.id] = p
	b.mu.Unlock()
}

func (b *askBroker) get(id string) *pendingAsk {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending[id]
}

func (b *askBroker) remove(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// list 返回当前所有待答问题（前端重连/刷新后恢复弹窗用）。
func (b *askBroker) list() []*pendingAsk {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*pendingAsk, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p)
	}
	return out
}

// nextID 生成问题 id（递增 + 时间戳，进程内唯一）。
func (b *askBroker) nextID() string {
	b.mu.Lock()
	b.seq++
	n := b.seq
	b.mu.Unlock()
	return fmt.Sprintf("ask-%d-%d", time.Now().UnixNano(), n)
}

// AskUser 由 ask_user_question / exit_plan_mode 工具调用：广播问题、阻塞等待作答。
//
// 返回值是 answers JSON（[]builtins.Answer 序列化），与 TUI 的 askViaChat 同格式，
// 因此工具侧不需要区分前端形态。
func (s *Server) AskUser(questionsJSON string) (string, error) {
	q := strings.TrimSpace(questionsJSON)
	if q == "" {
		return "[]", nil
	}
	// 校验 JSON 合法性：不合法时立刻返回，避免前端渲染空白弹窗后永远等不到答案。
	var probe []builtins.Question
	if err := json.Unmarshal([]byte(q), &probe); err != nil || len(probe) == 0 {
		return "", fmt.Errorf("ask_user_question: 无效的问题 JSON: %v", err)
	}

	id := s.ask.nextID()
	p := &pendingAsk{
		id:        id,
		convID:    s.RunConv(),
		questions: q,
		answerCh:  make(chan string, 1),
		created:   time.Now(),
	}
	s.ask.add(p)
	s.logf("web ask_user: id=%s conv=%s questions=%d", id, p.convID, len(probe))

	// 广播给前端：questions 用 RawMessage，前端直接拿到数组而不是转义字符串。
	s.hub.broadcast(map[string]any{
		"type": "event", "event": "ask_user",
		"id": id, "questions": json.RawMessage(q),
		"conversation_id": p.convID,
	})
	defer func() {
		s.ask.remove(id)
		// 通知前端关闭弹窗（无论是answered 还是被取消）
		s.hub.broadcast(map[string]any{
			"type": "event", "event": "ask_close",
			"id": id, "conversation_id": p.convID,
		})
	}()

	// 轮询 Aborted：用户点「中止」/ /stop 时不能干等（工具阻塞会让整个 Run 卡住）。
	// 不设等待上限——用户可能很久才回来看一眼，超时会让题目在用户看到前就失效。
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ans := <-p.answerCh:
			s.logf("web ask_user 已作答: id=%s", id)
			return ans, nil
		case <-ticker.C:
			if s.agent.Aborted() {
				s.logf("web ask_user 任务被取消: id=%s", id)
				return "[]", nil
			}
		}
	}
}

// handleWebAnswer POST /web/api/answer {id, answers:[{id,value,values}]}
//
// 把用户在前端弹窗里的选择回填给阻塞中的工具调用。
func (s *Server) handleWebAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		ID      string            `json:"id"`
		Answers []builtins.Answer `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		errJSON(w, http.StatusBadRequest, "id required")
		return
	}
	p := s.ask.get(body.ID)
	if p == nil {
		// 已有答案（重复提交）或问题已因取消而消失：都不算错误，告知前端即可。
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no_pending_question"})
		return
	}
	if len(body.Answers) == 0 {
		errJSON(w, http.StatusBadRequest, "answers required")
		return
	}
	b, err := json.Marshal(body.Answers)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "encode answers failed")
		return
	}
	// 非阻塞发送：channel 有 1 槽缓冲；若已有人填过（重复提交）则丢弃并提示。
	select {
	case p.answerCh <- string(b):
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "already_answered"})
	}
}

// handleWebAskPending GET /web/api/ask → 当前未作答的问题（前端刷新页面后恢复弹窗）。
func (s *Server) handleWebAskPending(w http.ResponseWriter, r *http.Request) {
	list := s.ask.list()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{
			"id":              p.id,
			"questions":       json.RawMessage(p.questions),
			"conversation_id": p.convID,
			"created":         p.created.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": out})
}
