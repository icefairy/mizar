package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// postJSONCookie 是 webui_steer_test.go 里 postJSON 的复用（同包），这里直接调用。

// TestWebCompactCommand 验证 Web 端 /compact：
//   - 命令不发给 LLM，走命令拦截
//   - 压缩当前会话历史并回写 webStore
//   - 返回可读提示（不再「无反应」）
func TestWebCompactCommand(t *testing.T) {
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	// 注入一个 mock 压缩器：Summarize 返回固定摘要，避免真实 LLM 调用
	a.Compactor = agent.DefaultCompactor(func(msgs []agent.Message) (string, error) {
		return "【历史摘要】用户做了一堆事", nil
	})
	a.Compactor.KeepRecentTokens = 500

	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	// 登录
	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login code=%d body=%s", login.code, login.body)
	}

	// 造一个会话 + 多轮历史
	conv := srv.web.store.create("chat")
	convID := conv.ID
	for i := 0; i < 6; i++ {
		srv.web.store.appendMsg(convID, convMessage{Role: "user", Content: strings.Repeat("用户消息内容 ", 40)})
		srv.web.store.appendMsg(convID, convMessage{Role: "assistant", Content: strings.Repeat("助手回复内容 ", 40)})
	}
	before := len(srv.web.store.get(convID).Messages)

	// 发 /compact
	r := postJSON(t, ts.URL+"/web/api/chat", map[string]any{"conversation_id": convID, "message": "/compact"}, login.cookies)
	if r.code != http.StatusOK {
		t.Fatalf("compact code=%d body=%s", r.code, r.body)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(r.body), &resp)
	reply, _ := resp["reply"].(string)
	if !strings.Contains(reply, "已压缩") {
		t.Fatalf("compact reply 应含「已压缩」，got=%q", reply)
	}
	if cmd, _ := resp["command"].(bool); !cmd {
		t.Fatalf("应标记 command=true, body=%s", r.body)
	}
	// 会话历史应被压缩（条数减少）
	after := len(srv.web.store.get(convID).Messages)
	if after >= before {
		t.Fatalf("压缩后条数未减少: before=%d after=%d", before, after)
	}
	// 应含摘要消息
	foundSummary := false
	for _, m := range srv.web.store.get(convID).Messages {
		if strings.Contains(m.Content, "历史摘要") {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Fatal("压缩后会话历史未见摘要内容")
	}
}

// TestWebCompactInsufficient 验证历史不足时的明确提示（不再静默）。
func TestWebCompactInsufficient(t *testing.T) {
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	a.Compactor = agent.DefaultCompactor(func(msgs []agent.Message) (string, error) { return "摘要", nil })

	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	conv := srv.web.store.create("chat")
	// 只放 1 条消息
	srv.web.store.appendMsg(conv.ID, convMessage{Role: "user", Content: "只有一条"})

	r := postJSON(t, ts.URL+"/web/api/chat", map[string]any{"conversation_id": conv.ID, "message": "/compact"}, login.cookies)
	if !strings.Contains(r.body, "消息不足") {
		t.Fatalf("应提示消息不足, body=%s", r.body)
	}
}

// TestWebUnknownSlashCommand 未知斜杠命令应提示，而不是当普通消息发 LLM。
func TestWebUnknownSlashCommand(t *testing.T) {
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw123"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw123"}, nil)
	conv := srv.web.store.create("chat")
	r := postJSON(t, ts.URL+"/web/api/chat", map[string]any{"conversation_id": conv.ID, "message": "/nosuchcmd"}, login.cookies)
	if !strings.Contains(r.body, "未知命令") {
		t.Fatalf("未知命令应提示, body=%s", r.body)
	}
}
