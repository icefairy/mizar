package server

// drafts.go —— 运行中草稿的落盘（长任务「页面被刷新/被杀」也不丢中间内容）。
//
// 与 evtlog.go 的分工：
//   - evtlog 解决「页面还活着，只是 WS 断了/被冻结」→ 按 seq 补齐事件。
//   - drafts 解决「页面整个被系统杀掉/用户刷新/换了台设备」→ 事件缓冲无从投递，
//     只把「当前正在生成的答复正文」定期落盘，重开后先显示它，再接上后续流。
//
// 存储用独立小文件（~/.mizar/drafts/<conv>.txt），不写 webui.json：
// 后者是整文件 JSON 重写，长任务每 2 秒全量落盘代价过高。

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// draftFlushInterval 草稿落盘最小间隔（避免每个 token 都写盘）。
const draftFlushInterval = 1500 * time.Millisecond

// draftStore 管理运行中草稿。
type draftStore struct {
	dir  string
	mu   sync.Mutex
	buf  map[string]*draftBuf
	last map[string]time.Time
}

type draftBuf struct {
	text  string
	step  int
	think bool // 当前处于思考阶段（正文尚未开始）
}

func newDraftStore() *draftStore {
	dir := filepath.Join(mizarHomeDir(), "drafts")
	return &draftStore{buf: map[string]*draftBuf{}, last: map[string]time.Time{}, dir: dir}
}

// mizarHomeDir ~/.mizar（与 webui.json 同目录）。
func mizarHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".mizar")
}

// append 追加一段增量正文；按间隔把内容刷到磁盘。
func (d *draftStore) append(conv, delta string) {
	if d == nil || conv == "" || delta == "" {
		return
	}
	d.mu.Lock()
	b := d.buf[conv]
	if b == nil {
		b = &draftBuf{}
		d.buf[conv] = b
	}
	b.text += delta
	needFlush := time.Since(d.last[conv]) >= draftFlushInterval
	text := b.text
	if needFlush {
		d.last[conv] = time.Now()
	}
	d.mu.Unlock()
	if needFlush {
		d.write(conv, text)
	}
}

// finish 本轮结束：清掉内存缓冲并删除磁盘草稿（正式答复已落盘 history）。
func (d *draftStore) finish(conv string) {
	if d == nil || conv == "" {
		return
	}
	d.mu.Lock()
	delete(d.buf, conv)
	delete(d.last, conv)
	d.mu.Unlock()
	_ = os.Remove(d.path(conv))
}

// get 读取草稿（内存优先，其次磁盘）。
func (d *draftStore) get(conv string) string {
	if d == nil || conv == "" {
		return ""
	}
	d.mu.Lock()
	if b, ok := d.buf[conv]; ok {
		t := b.text
		d.mu.Unlock()
		return t
	}
	d.mu.Unlock()
	b, err := os.ReadFile(d.path(conv))
	if err != nil {
		return ""
	}
	return string(b)
}

func (d *draftStore) path(conv string) string {
	// conv id 由服务端生成（c+数字+hex），不含路径分隔符；仍做一次净化防御。
	safe := make([]rune, 0, len(conv))
	for _, r := range conv {
		if r == '/' || r == '\\' || r == '.' || r == 0 {
			continue
		}
		safe = append(safe, r)
	}
	return filepath.Join(d.dir, string(safe)+".txt")
}

func (d *draftStore) write(conv, text string) {
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(d.path(conv), []byte(text), 0o600)
}

// handleWebDraft GET /web/api/draft?conversation_id=<id>
// 返回该会话「正在生成但尚未结束」的答复草稿。页面刷新/被系统杀掉后重开时，
// 前端用它先渲染出已有进度，随后接上 WS 的后续增量。
func (s *Server) handleWebDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	conv := r.URL.Query().Get("conversation_id")
	running := s.runLive() && (conv == "" || s.RunConv() == conv)
	text := ""
	if running {
		text = s.draft.get(conv)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation_id": conv,
		"draft":           text,
		"running":         running,
		"running_conv":    s.RunConv(),
	})
}
