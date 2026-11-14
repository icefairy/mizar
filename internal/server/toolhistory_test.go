package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"io"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// toolPluginServer 构造一个带 rpc/tool 插件的 Server，用于验证工具过程落盘。
func toolPluginServer(t *testing.T) (*Server, *httptest.Server, []*http.Cookie) {
	t.Helper()
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw"}
	srv := New(cfg, agent.New(&mockLLM{}, testPluginManager(t)), session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login=%d %s", login.code, login.body)
	}
	return srv, ts, login.cookies
}

// TestAppendToolMsgPersists 工具过程应落盘，含 reason/args/result，且 role 保持 assistant+kind=tool。
func TestAppendToolMsgPersists(t *testing.T) {
	dir := t.TempDir()
	st := newWebStore(filepath.Join(dir, "webui.json"))
	c := st.create("agent")

	st.appendToolMsg(c.ID, "read", `{"path":"/tmp/a.txt"}`, "我先看一下这个文件", "文件内容 ABC", nil)

	got := st.get(c.ID)
	if got == nil || len(got.Messages) != 1 {
		t.Fatalf("messages=%v", got)
	}
	m := got.Messages[0]
	if m.Role != "assistant" || m.Kind != "tool" {
		t.Fatalf("role=%q kind=%q, want assistant/tool", m.Role, m.Kind)
	}
	if m.Tool != "read" || m.Reason != "我先看一下这个文件" || m.Result != "文件内容 ABC" {
		t.Fatalf("字段丢失: %+v", m)
	}
	if !strings.Contains(m.Args, "/tmp/a.txt") {
		t.Fatalf("args=%q", m.Args)
	}
	// 重新从磁盘加载，确认确实持久化（不只是内存）
	st2 := newWebStore(filepath.Join(dir, "webui.json"))
	st2.load()
	got2 := st2.get(c.ID)
	if got2 == nil || len(got2.Messages) != 1 || got2.Messages[0].Kind != "tool" {
		t.Fatalf("重新加载后丢失: %+v", got2)
	}
}

// TestAppendToolMsgError 执行失败要记 err，并把摘要标成 ✗。
func TestAppendToolMsgError(t *testing.T) {
	st := newWebStore(filepath.Join(t.TempDir(), "webui.json"))
	c := st.create("agent")
	st.appendToolMsg(c.ID, "bash", `{"command":"boom"}`, "", "", errors.New("exit status 1"))
	m := st.get(c.ID).Messages[0]
	if m.Err == "" || !strings.Contains(m.Err, "exit status 1") {
		t.Fatalf("err=%q", m.Err)
	}
	if !strings.HasPrefix(m.Content, "✗") {
		t.Fatalf("摘要=%q，应以 ✗ 开头", m.Content)
	}
}

// TestAppendToolMsgTruncates 超长输出要截断并标注原长，避免 webui.json 膨胀。
func TestAppendToolMsgTruncates(t *testing.T) {
	st := newWebStore(filepath.Join(t.TempDir(), "webui.json"))
	c := st.create("agent")
	huge := strings.Repeat("x", toolFieldLimit+500)
	st.appendToolMsg(c.ID, "read", "", "", huge, nil)
	m := st.get(c.ID).Messages[0]
	if len(m.Result) >= len(huge) {
		t.Fatalf("未截断: len=%d", len(m.Result))
	}
	if !strings.Contains(m.Result, "已截断") {
		t.Fatalf("截断标记缺失: %q", m.Result[len(m.Result)-60:])
	}
}

// TestToolMessagesExcludedFromLLMContext 关键：kind=tool 不能进 LLM 上下文。
// OpenAI 协议要求 tool 消息必须紧跟带 tool_calls 的 assistant 消息，裸塞会 400。
func TestToolMessagesExcludedFromLLMContext(t *testing.T) {
	var got []convMessage
	for _, m := range []convMessage{
		{Role: "user", Content: "帮我读文件"},
		{Role: "assistant", Kind: "tool", Tool: "read", Content: "✓ read"},
		{Role: "assistant", Content: "读完了，内容是 ABC"},
		{Role: "user", Content: "谢谢"},
	} {
		if m.isText() {
			got = append(got, m)
		}
	}
	if len(got) != 3 {
		t.Fatalf("过滤后=%d 条, want 3: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Kind != "" && m.Kind != "text" {
			t.Fatalf("工具消息漏进上下文: %+v", m)
		}
	}
}

// TestToolMsgVisibleOverAPI /web/api/conversation 应把工具过程一并返回（前端刷新靠它）。
func TestToolMsgVisibleOverAPI(t *testing.T) {
	srv, ts, cookies := toolPluginServer(t)
	defer ts.Close()
	c := srv.web.store.create("agent")
	srv.web.store.appendMsg(c.ID, convMessage{Role: "user", Content: "读文件"})
	srv.web.store.appendToolMsg(c.ID, "read", `{"path":"/a"}`, "先看一下", "ABC", nil)
	srv.web.store.appendMsg(c.ID, convMessage{Role: "assistant", Content: "内容是 ABC"})

	res := getConvWithCookies(t, ts.URL+"/web/api/conversation?id="+c.ID, cookies)
	if res.code != http.StatusOK {
		t.Fatalf("code=%d %s", res.code, res.body)
	}
	var out struct {
		Conversation struct {
			Messages []convMessage `json:"messages"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal([]byte(res.body), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := out.Conversation.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages=%d, want 3", len(msgs))
	}
	tool := msgs[1]
	if tool.Kind != "tool" || tool.Tool != "read" || tool.Reason != "先看一下" || tool.Result != "ABC" {
		t.Fatalf("工具消息未完整返回: %+v", tool)
	}
}

// TestWebStoreOldDataCompat 旧数据没有 kind 字段 → 视为正文，不能丢历史。
func TestWebStoreOldDataCompat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "webui.json")
	old := `{"conversations":[{"id":"c1","title":"旧会话","mode":"chat","messages":[{"role":"user","content":"老问题"},{"role":"assistant","content":"老回答"}]}]}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newWebStore(p)
	st.load()
	c := st.get("c1")
	if c == nil || len(c.Messages) != 2 {
		t.Fatalf("旧数据加载失败: %+v", c)
	}
	for _, m := range c.Messages {
		if !m.isText() {
			t.Fatalf("旧消息被误判为非正文: %+v", m)
		}
	}
}

// 工具辅助：带 cookie 的 GET。
func getConvWithCookies(t *testing.T, url string, cookies []*http.Cookie) resp {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{code: res.StatusCode, body: string(b)}
}
