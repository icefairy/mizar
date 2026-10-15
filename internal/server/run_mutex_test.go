package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// TestConcurrentRunConflict 验证 Agent.Run 入口串行化：
// 已有 Run 在执行时，第二个请求应立即返回 409，而不是覆盖共享的 Initial/OnLLMStream。
func TestConcurrentRunConflict(t *testing.T) {
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

	// 先占用 runMu，模拟一个正在执行的 Run
	if !srv.acquireRun() {
		t.Fatal("首次 acquireRun 应成功")
	}
	defer srv.releaseRun()

	// 此时 /web/api/run 应立即 409
	jw := postJSON(t, ts.URL+"/web/api/run", map[string]any{"conversation_id": "", "message": "hi"}, login.cookies)
	if jw.code != http.StatusConflict {
		t.Fatalf("busy run: code=%d want 409 body=%s", jw.code, jw.body)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(jw.body), &body)
	if _, ok := body["error"]; !ok {
		t.Fatalf("409 body should carry error, got %s", jw.body)
	}
	close(release)
}
