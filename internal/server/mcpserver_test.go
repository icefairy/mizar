package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mizar/internal/agent"
	"mizar/internal/builtins"
	"mizar/internal/mcp"
	"mizar/internal/plugins"
	"mizar/internal/session"
)

func newMCPTestServer(t *testing.T, token string) *Server {
	t.Helper()
	a := agent.New(&mockLLM{}, testPluginManager(t))
	cfg := NewConfig("127.0.0.1:0", token)
	cfg.SetAll(true, true, true)
	return New(cfg, a, session.New(t.TempDir()))
}

func TestMCPRequiresToken(t *testing.T) {
	srv := newMCPTestServer(t, "")
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("code=%d want 403", resp.StatusCode)
	}
}

func TestMCPToolsListAndCall(t *testing.T) {
	srv := newMCPTestServer(t, "tok")
	srv.agent.Plugins.RegisterBuiltin(plugins.Tool{
		Name:        "echo",
		Description: "echo the JSON args",
		Run:         func(args string) (string, error) { return "echo:" + args, nil },
	})
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	tr := mcp.NewHTTPTransportWithHeaders(ts.URL+"/mcp", 5*time.Second, map[string]string{
		"Authorization": "Bearer tok",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient("mizar-test", "test", "0.0.1")
	if err := client.Initialize(ctx, tr); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tools, err := client.ListTools(ctx, tr)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	found := false
	for _, tl := range tools {
		if tl.Name == "echo" {
			found = true
			if tl.InputSchema["type"] != "object" {
				t.Fatalf("echo inputSchema type=%v", tl.InputSchema["type"])
			}
		}
	}
	if !found {
		t.Fatalf("tools/list missing echo; got %d tools", len(tools))
	}

	res, err := client.CallTool(ctx, tr, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("tools/call echo: %v", err)
	}
	if res.IsError || !strings.Contains(res.Text(), `"text":"hi"`) {
		t.Fatalf("echo result: isError=%v text=%q", res.IsError, res.Text())
	}

	// 工具不存在：协议成功，isError=true（MCP 规范）
	bad, err := client.CallTool(ctx, tr, "definitely_not_a_tool", map[string]any{})
	if err != nil {
		t.Fatalf("unknown tool should not be protocol error: %v", err)
	}
	if !bad.IsError || !strings.Contains(bad.Text(), "not found") {
		t.Fatalf("unknown tool result: isError=%v text=%q", bad.IsError, bad.Text())
	}
}

// TestMCPUploadFile 验证 MCP tools/call 能把 base64 内容经 upload_file 落盘。
func TestMCPUploadFile(t *testing.T) {
	srv := newMCPTestServer(t, "tok")
	dir := t.TempDir()
	srv.agent.WorkDir = dir
	for _, tl := range builtins.All("", nil) {
		if tl.Name == "upload_file" {
			srv.agent.Plugins.RegisterBuiltin(tl)
		}
	}
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	tr := mcp.NewHTTPTransportWithHeaders(ts.URL+"/mcp", 5*time.Second, map[string]string{
		"Authorization": "Bearer tok",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient("mizar-test", "test", "0.0.1")
	if err := client.Initialize(ctx, tr); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	res, err := client.CallTool(ctx, tr, "upload_file", map[string]any{
		"path": "sub/x.bin", "content": "AAEC /w==", "encoding": "base64",
	})
	if err != nil {
		t.Fatalf("upload_file: %v", err)
	}
	if res.IsError {
		t.Fatalf("upload_file isError: %s", res.Text())
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub", "x.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x01, 0x02, 0xff}
	if len(b) != len(want) {
		t.Fatalf("file=%x want %x", b, want)
	}
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("file=%x want %x", b, want)
		}
	}
}

func TestMCPUnknownMethodAndNotification(t *testing.T) {
	srv := newMCPTestServer(t, "tok")
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	post := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post(`{"jsonrpc":"2.0","id":"x","method":"nope"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unknown method code=%d", resp.StatusCode)
	}
	// 通知无 id → 202
	notif := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer notif.Body.Close()
	if notif.StatusCode != http.StatusAccepted {
		t.Fatalf("notification code=%d want 202", notif.StatusCode)
	}
}
