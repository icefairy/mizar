package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/config"
	"mizar/internal/session"
)

// newProviderTestServer 用隔离的 HOME 起测试 Server（避免污染真实 ~/.mizar）。
func newProviderTestServer(t *testing.T) (*Server, *httptest.Server, []*http.Cookie) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// 预置旧版单供应商配置，验证自动升级后 provider 接口可用
	cfgPath := filepath.Join(home, ".mizar", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"base_url":"http://legacy.local/v1","api_key":"sk-legacy","model":"legacy-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	cfg.Version = "v9.9.9"
	cfg.Web = WebAuth{Enabled: true, Username: "admin", Password: "pw"}
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	login := postJSON(t, ts.URL+"/web/api/login", map[string]any{"username": "admin", "password": "pw"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login: %d %s", login.code, login.body)
	}
	return srv, ts, login.cookies
}

// TestWebVersionEndpoint 登录页可拿到版本号（无需登录）。
func TestWebVersionEndpoint(t *testing.T) {
	_, ts, _ := newProviderTestServer(t)
	r, err := http.Get(ts.URL + "/web/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	buf := make([]byte, 256)
	n, _ := r.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "v9.9.9") {
		t.Fatalf("version 应为 v9.9.9, got %s", buf[:n])
	}
}

// TestWebProvidersCRUD 收藏夹增删切换 + 旧配置自动迁移。
func TestWebProvidersCRUD(t *testing.T) {
	_, ts, cookies := newProviderTestServer(t)

	// GET：旧配置已自动迁移为一条 provider
	req, _ := http.NewRequest("GET", ts.URL+"/web/api/providers", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, r)
	if !strings.Contains(body, "legacy.local") {
		t.Fatalf("应包含迁移出的旧供应商: %s", body)
	}
	if !strings.Contains(body, "sk-") && !strings.Contains(body, "mask") {
		t.Logf("body=%s", body)
	}
	if strings.Contains(body, "sk-legacy") {
		t.Fatalf("API Key 不应明文返回: %s", body)
	}

	// add
	jw := postJSON(t, ts.URL+"/web/api/providers", map[string]any{
		"action": "add", "name": "home", "base_url": "http://127.0.0.1:3002/v1", "api_key": "sk-home-key",
	}, cookies)
	if jw.code != http.StatusOK || !strings.Contains(jw.body, "127.0.0.1:3002") {
		t.Fatalf("add: %d %s", jw.code, jw.body)
	}

	// use
	jw = postJSON(t, ts.URL+"/web/api/providers", map[string]any{"action": "use", "name": "home"}, cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("use: %d %s", jw.code, jw.body)
	}
	// 落盘校验：config.json 的 BaseURL 应已切换
	cfg, err := config.Load(filepath.Join(os.Getenv("HOME"), ".mizar", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:3002/v1" || cfg.APIKey != "sk-home-key" {
		t.Fatalf("切换后配置未落盘: %+v", cfg)
	}
	if cfg.ActiveProvider != "home" {
		t.Fatalf("active 应为 home, got %q", cfg.ActiveProvider)
	}

	// del
	jw = postJSON(t, ts.URL+"/web/api/providers", map[string]any{"action": "del", "name": "home"}, cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("del: %d %s", jw.code, jw.body)
	}
	cfg, _ = config.Load(filepath.Join(os.Getenv("HOME"), ".mizar", "config.json"))
	if cfg.FindProvider("home") != nil {
		t.Fatal("删除后不应还能找到 home")
	}

	// del 不存在 → 404
	jw = postJSON(t, ts.URL+"/web/api/providers", map[string]any{"action": "del", "name": "nope"}, cookies)
	if jw.code != http.StatusNotFound {
		t.Fatalf("del missing: %d %s", jw.code, jw.body)
	}
}

// TestWebStatusEndpoint 状态栏接口返回 tokens/上下文/速度结构。
func TestWebStatusEndpoint(t *testing.T) {
	srv, ts, cookies := newProviderTestServer(t)
	// 制造一条会话并写入历史，验证上下文估算
	c := srv.web.store.create("agent")
	srv.web.store.appendMsg(c.ID, convMessage{Role: "user", Content: strings.Repeat("你好", 50)})

	req, _ := http.NewRequest("GET", ts.URL+"/web/api/status?id="+c.ID, nil)
	for _, x := range cookies {
		req.AddCookie(x)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, r)
	for _, key := range []string{"tokens", "context", "speed", "model", "thinking", "running"} {
		if !strings.Contains(body, key) {
			t.Fatalf("status 缺少字段 %s: %s", key, body)
		}
	}
}

func readAll(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	b := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		b = append(b, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(b)
}
