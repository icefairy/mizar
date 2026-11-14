package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/plugins"
	"mizar/internal/session"
)

// newPluginTestServer 构造带 rpc_* 插件的 Server + httptest（照 server_test.go 模式）。
func newPluginTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "myext.ts"), []byte(`
export function rpc_echo(params) {
  const p = JSON.parse(params || "{}");
  return JSON.stringify({echo: p.x});
}
export function rpc_add(params) {
  const p = JSON.parse(params || "{}");
  return String((p.a || 0) + (p.b || 0));
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	pm := plugins.NewManager(dir, &engine.HostFuncs{})
	if _, failed := pm.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	a := agent.New(&mockLLM{}, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	return srv, httptest.NewServer(srv.mux)
}

// TestRoutesAdmin 路由注册表应覆盖全端点，且含插件动态端点。
func TestRoutesAdmin(t *testing.T) {
	srv, ts := newPluginTestServer(t)
	defer ts.Close()
	_ = srv

	res := do(t, ts, "GET", "/admin/routes", "test-token", nil)
	if res.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", res.code, res.body)
	}
	var out struct {
		Version string     `json:"version"`
		Routes  []RouteMeta `json:"routes"`
	}
	if err := json.Unmarshal([]byte(res.body), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 静态表全量在
	if len(out.Routes) < len(routeTable) {
		t.Fatalf("routes=%d < static=%d", len(out.Routes), len(routeTable))
	}
	// 插件动态端点：rpc_echo / rpc_add × (/rpc 项 + /ext/ 项) = 4
	var dyn int
	for _, r := range out.Routes {
		if r.Group == "plugin" {
			dyn++
		}
	}
	if dyn != 4 {
		t.Fatalf("plugin dynamic routes = %d, want 4", dyn)
	}
	// 关键端点抽查
	joined := strings.Join(pathsOf(out.Routes), ",")
	for _, want := range []string{"/admin/routes", "/ext/", "/web/api/frontend", "/v1/chat/completions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("route table missing %q\n%s", want, joined)
		}
	}
	// 无鉴权 401
	res401 := do(t, ts, "GET", "/admin/routes", "", nil)
	if res401.code != http.StatusUnauthorized {
		t.Errorf("no token: code=%d, want 401", res401.code)
	}
}

// TestExtRPCRoot 列出可用插件方法。
func TestExtRPCRoot(t *testing.T) {
	_, ts := newPluginTestServer(t)
	defer ts.Close()

	res := do(t, ts, "GET", "/ext/", "test-token", nil)
	if res.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", res.code, res.body)
	}
	var out map[string][]string
	if err := json.Unmarshal([]byte(res.body), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	methods := out["methods"]
	joined := strings.Join(methods, ",")
	if !strings.Contains(joined, "ext/echo") || !strings.Contains(joined, "ext/add") {
		t.Fatalf("methods=%v", methods)
	}
}

// TestExtRPCCall GET 无参 + POST 带参，插件函数应经 /ext/ 直达。
func TestExtRPCCall(t *testing.T) {
	_, ts := newPluginTestServer(t)
	defer ts.Close()

	// POST 带 JSON body（经 plugins.CallRPC 的超时 / panic 兜底通道）
	res := do(t, ts, "POST", "/ext/add", "test-token", []byte(`{"a":3,"b":4}`))
	if res.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", res.code, res.body)
	}
	if strings.TrimSpace(res.body) != "7" {
		t.Fatalf("add out=%q, want 7", res.body)
	}
	// GET 无参（params={} 兜底）；插件 JSON.stringify({echo: undefined}) 丢 undefined 键 → "{}"
	res = do(t, ts, "GET", "/ext/echo", "test-token", nil)
	if res.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", res.code, res.body)
	}
	if strings.TrimSpace(res.body) != "{}" {
		t.Fatalf("echo out=%q, want {}", res.body)
	}
	// 未知方法 404
	res = do(t, ts, "GET", "/ext/nope", "test-token", nil)
	if res.code != http.StatusNotFound {
		t.Errorf("unknown: code=%d, want 404", res.code)
	}
}

// pathsOf 取路径列（测试断言用）。
func pathsOf(rs []RouteMeta) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Path)
	}
	return out
}

// do 带 Bearer token 的 GET/POST 小工具（本文件专用）。
func do(t *testing.T, ts *httptest.Server, method, path, token string, body []byte) struct {
	code int
	body string
} {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return struct {
		code int
		body string
	}{res.StatusCode, string(b)}
}
