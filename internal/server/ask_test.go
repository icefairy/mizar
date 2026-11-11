package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
	"mizar/internal/session"
)

func newAskTestServer(t *testing.T) (*Server, *httptest.Server, []*http.Cookie) {
	t.Helper()
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login code=%d body=%s", login.code, login.body)
	}
	return srv, ts, login.cookies
}

// TestAskUserRoundTrip AskUser 广播后由 HTTP 作答，应返回 answers JSON。
func TestAskUserRoundTrip(t *testing.T) {
	srv, ts, cookies := newAskTestServer(t)
	defer ts.Close()

	questions := `[{"id":"q1","question":"选哪个？","options":[{"label":"A"},{"label":"B"}]}]`
	type res struct {
		ans string
		err error
	}
	done := make(chan res, 1)
	go func() {
		a, err := srv.AskUser(questions)
		done <- res{a, err}
	}()

	// 等待 pending 注册
	var id string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list := srv.ask.list()
		if len(list) == 1 {
			id = list[0].id
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("pending 问题未注册")
	}

	jw := postJSON(t, ts.URL+"/web/api/answer", map[string]any{
		"id": id,
		"answers": []map[string]any{
			{"id": "q1", "value": "B"},
		},
	}, cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("answer code=%d body=%s", jw.code, jw.body)
	}
	if !strings.Contains(jw.body, `"ok":true`) {
		t.Fatalf("answer body=%s", jw.body)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("AskUser err=%v", r.err)
		}
		var ans []map[string]any
		if err := json.Unmarshal([]byte(r.ans), &ans); err != nil {
			t.Fatalf("answers 非合法 JSON: %q", r.ans)
		}
		if len(ans) != 1 || ans[0]["value"] != "B" {
			t.Fatalf("answers=%v", ans)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AskUser 未返回（作答未生效）")
	}
}

// TestAskPendingEndpoint /web/api/ask 应列出未答问题（刷新页面后恢复弹窗）。
func TestAskPendingEndpoint(t *testing.T) {
	srv, ts, cookies := newAskTestServer(t)
	defer ts.Close()

	questions := `[{"id":"q1","question":"继续？","options":[{"label":"是"},{"label":"否"}]}]`
	done := make(chan struct{})
	go func() { _, _ = srv.AskUser(questions); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(srv.ask.list()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	r, err := httpGet(ts.URL+"/web/api/ask", cookies)
	if err != nil {
		t.Fatalf("GET ask: %v", err)
	}
	if !strings.Contains(r, `"pending"`) || !strings.Contains(r, "继续？") {
		t.Fatalf("pending 响应异常: %s", r)
	}
	// 取消任务放行 goroutine
	srv.agent.Abort()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Abort 后 AskUser 未退出")
	}
}

// TestAskUserAbortReturnsEmpty 任务被取消时 AskUser 返回 "[]"（不阻塞、不报错）。
func TestAskUserAbortReturnsEmpty(t *testing.T) {
	srv, ts, _ := newAskTestServer(t)
	defer ts.Close()

	done := make(chan string, 1)
	go func() {
		a, _ := srv.AskUser(`[{"id":"q1","question":"等很久？"}]`)
		done <- a
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(srv.ask.list()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	srv.agent.Abort()
	select {
	case a := <-done:
		if a != "[]" {
			t.Fatalf("Abort 后应返回 \"[]\"，实际 %q", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Abort 后 AskUser 未返回")
	}
}

// httpGet 带 cookie 的 GET，返回 body（测试辅助）。
func httpGet(url string, cookies []*http.Cookie) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
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
	return sb.String(), nil
}
