package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// resp 记录一次 HTTP 调用的结果。
type resp struct {
	code    int
	body    string
	cookies []*http.Cookie
}

// postJSON marshals body 并 POST 到 url；可选携带 cookie。
func postJSON(t *testing.T, url string, payload map[string]any, cookies []*http.Cookie) resp {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return resp{code: r.StatusCode, body: string(body), cookies: r.Cookies()}
}

// TestWebSteerEndpoint 验证 /web/api/steer 的 HTTP 行为：
//   - 未登录 → 401（webAuth 拦截）
//   - 空内容 → 400
//   - 登录后携带 cookie → 内容注入 agent.Steer（PendingSteer 为 true）
func TestWebSteerEndpoint(t *testing.T) {
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	// 1) 未登录 → 401
	jw := postJSON(t, ts.URL+"/web/api/steer", map[string]any{"content": "hi"}, nil)
	if jw.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated steer: code=%d want 401 body=%s", jw.code, jw.body)
	}

	// 2) 登录拿 cookie
	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login code=%d want 200 body=%s", login.code, login.body)
	}

	// 3) 空内容 → 400
	empty := postJSON(t, ts.URL+"/web/api/steer", map[string]any{"content": "   "}, login.cookies)
	if empty.code != http.StatusBadRequest {
		t.Fatalf("empty steer: code=%d want 400 body=%s", empty.code, empty.body)
	}

	// 4) 无活跃 Run 时：不注入（避免消息被静默丢弃），返回 running=false
	idle := postJSON(t, ts.URL+"/web/api/steer", map[string]any{"content": "换个方向"}, login.cookies)
	if idle.code != http.StatusOK {
		t.Fatalf("steer code=%d want 200 body=%s", idle.code, idle.body)
	}
	if a.PendingSteer() {
		t.Fatal("无活跃 Run 时不应注入 steer（前端会改作新任务补发）")
	}
	if !strings.Contains(idle.body, `"running":false`) {
		t.Fatalf("idle steer body should report running=false, got %s", idle.body)
	}
}

// TestWebSteerInjectsWhileRunning 有活跃 Run 时 steer 应真正注入。
func TestWebSteerInjectsWhileRunning(t *testing.T) {
	release := make(chan struct{})
	llm := &mockLLM{Fn: func(msgs []agent.Message) (string, error) {
		<-release
		return `{"action":"reply","text":"回答完毕"}`, nil
	}}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login code=%d body=%s", login.code, login.body)
	}

	// 后台起一个 Run（会阻塞在 mock LLM 上），确保有活跃循环
	done := make(chan struct{})
	go func() { _, _ = a.Run("长任务"); close(done) }()
	// 等待 Run 进入活跃状态
	deadline := time.Now().Add(2 * time.Second)
	for !a.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.Running() {
		close(release)
		t.Fatal("Run 未进入活跃状态")
	}

	ok := postJSON(t, ts.URL+"/web/api/steer", map[string]any{"content": "换个方向"}, login.cookies)
	if ok.code != http.StatusOK {
		t.Fatalf("steer code=%d body=%s", ok.code, ok.body)
	}
	if !a.PendingSteer() {
		t.Fatal("活跃 Run 期间 steer 应被注入")
	}
	if !strings.Contains(ok.body, `"running":true`) {
		t.Fatalf("body should report running=true, got %s", ok.body)
	}
	close(release)
	<-done
}
