package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestEvtLogSeqMonotonic 事件日志必须为每条事件分配单调递增 seq，
// 且 since(after) 只返回更新的部分——前端靠这个游标补齐断线期间的事件。
func TestEvtLogSeqMonotonic(t *testing.T) {
	l := newEvtLog()
	for i := 0; i < 5; i++ {
		ev := map[string]any{"type": "event", "event": "stream", "delta": "x"}
		seq := l.append("conv-A", ev)
		if seq != int64(i+1) {
			t.Fatalf("seq=%d want %d", seq, i+1)
		}
	}
	evs, last := l.since("conv-A", 2)
	if last != 5 {
		t.Fatalf("last=%d want 5", last)
	}
	if len(evs) != 3 {
		t.Fatalf("since(2) len=%d want 3", len(evs))
	}
	for i, e := range evs {
		if e["seq"] != int64(i+3) {
			t.Fatalf("evs[%d].seq=%v want %d", i, e["seq"], i+3)
		}
	}
	// 会话隔离：别的会话互不影响
	if _, lastB := l.since("conv-B", 0); lastB != 0 {
		t.Fatalf("conv-B last=%d want 0", lastB)
	}
}

// TestEvtLogResetKeepsSeqMonotonic reset 只清缓冲，不清 seq（前端游标不回退）。
func TestEvtLogResetKeepsSeqMonotonic(t *testing.T) {
	l := newEvtLog()
	l.append("c", map[string]any{"a": 1})
	l.append("c", map[string]any{"a": 2})
	l.reset("c")
	if evs, last := l.since("c", 0); len(evs) != 0 || last != 2 {
		t.Fatalf("reset 后 len=%d last=%d，期望 0/2", len(evs), last)
	}
	if seq := l.append("c", map[string]any{"a": 3}); seq != 3 {
		t.Fatalf("reset 后 seq=%d want 3（必须继续递增）", seq)
	}
}

// TestEvtLogCap 超出上限丢最旧的，且 seq 仍连续。
func TestEvtLogCap(t *testing.T) {
	l := newEvtLog()
	for i := 0; i < evtLogCap+10; i++ {
		l.append("c", map[string]any{"i": i})
	}
	evs, last := l.since("c", 0)
	if len(evs) != evtLogCap {
		t.Fatalf("len=%d want %d", len(evs), evtLogCap)
	}
	if last != int64(evtLogCap+10) {
		t.Fatalf("last=%d want %d", last, evtLogCap+10)
	}
	// 保留的是最近的一批
	if evs[len(evs)-1]["i"] != evtLogCap+9 {
		t.Fatalf("最后一条应为最新事件，得到 %v", evs[len(evs)-1]["i"])
	}
}

// TestWebEventsEndpoint /web/api/events 需要登录，并按 after 返回增量。
func TestWebEventsEndpoint(t *testing.T) {
	srv, ts, cookies := newProviderTestServer(t)

	// 未登录 → 401
	r, err := http.Get(ts.URL + "/web/api/events?conversation_id=c1&after=0")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录 code=%d want 401", r.StatusCode)
	}

	// 造 3 条会话事件
	for i := 0; i < 3; i++ {
		srv.emit(map[string]any{
			"type": "event", "event": "stream", "delta": "d",
			"conversation_id": "c1",
		})
	}
	jw := getWithCookies(t, ts.URL+"/web/api/events?conversation_id=c1&after=1", cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", jw.code, jw.body)
	}
	var j struct {
		Events []map[string]any `json:"events"`
		Last   int64            `json:"last"`
	}
	if err := json.Unmarshal([]byte(jw.body), &j); err != nil {
		t.Fatalf("json: %v body=%s", err, jw.body)
	}
	if j.Last != 3 || len(j.Events) != 2 {
		t.Fatalf("last=%d len=%d，期望 3/2 body=%s", j.Last, len(j.Events), jw.body)
	}
}

// TestDraftStoreWriteRead 草稿应可写入/读取/清除（页面被杀后的兜底）。
func TestDraftStoreWriteRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	d := newDraftStore()
	d.append("cabc", "第一段")
	if got := d.get("cabc"); got != "第一段" {
		t.Fatalf("get=%q", got)
	}
	d.append("cabc", "第二段")
	if got := d.get("cabc"); got != "第一段第二段" {
		t.Fatalf("get=%q", got)
	}
	// finish 后清空（正式答复已落盘历史）
	d.finish("cabc")
	if got := d.get("cabc"); got != "" {
		t.Fatalf("finish 后 get=%q，期望空", got)
	}
}

// TestDraftStoreFlushToDisk 超过间隔后应落盘，且新实例（模拟进程重启）能读到。
func TestDraftStoreFlushToDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	d := newDraftStore()
	d.append("c1", "内容")
	// 强制过期，触发一次落盘
	d.mu.Lock()
	d.last["c1"] = time.Now().Add(-2 * draftFlushInterval)
	d.mu.Unlock()
	d.append("c1", "更多")

	d2 := newDraftStore() // 无内存缓冲，只能读磁盘
	if got := d2.get("c1"); got == "" {
		t.Fatal("期望从磁盘读到草稿")
	}
}

// TestHubBroadcastDoesNotBlockOnStuckClient 一个写阻塞的客户端不能拖垮广播。
//
// 这是移动端「一个手机切后台导致全家都不动了」的回归测试：旧实现在持 hub 锁
// 的情况下逐个 WriteJSON，只要有一个连接写阻塞，所有客户端的事件流全部停摆。
func TestHubBroadcastDoesNotBlockOnStuckClient(t *testing.T) {
	// 服务端升级后不读、客户端也不读——写缓冲很快填满，服务端写就会阻塞。
	up := websocket.Upgrader{}
	srvConnCh := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvConnCh <- c
	}))
	defer ts.Close()

	proto := "ws" + strings.TrimPrefix(ts.URL, "http")
	cli, _, err := websocket.DefaultDialer.Dial(proto, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	var srvConn *websocket.Conn
	select {
	case srvConn = <-srvConnCh:
	case <-time.After(5 * time.Second):
		t.Fatal("升级超时")
	}

	h := newHub()
	h.add(newWSClient(srvConn))

	big := strings.Repeat("x", 256*1024)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 8; i++ {
			h.broadcast(map[string]any{"type": "event", "data": big})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("broadcast 被阻塞的客户端卡住（写超时未生效）")
	}
}

type httpResp struct {
	code int
	body string
}

// getWithCookies 带会话 cookie 发 GET。
func getWithCookies(t *testing.T, url string, cookies []*http.Cookie) httpResp {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer r.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return httpResp{code: r.StatusCode, body: sb.String()}
}
