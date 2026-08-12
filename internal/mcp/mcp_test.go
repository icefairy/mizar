package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- 内置 mock MCP 服务器（stdio） ---

const mockServerSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	ID     int64  ` + "`json:\"id\"`" + `
	Method string ` + "`json:\"method\"`" + `
	Params map[string]any ` + "`json:\"params\"`" + `
}

type resp struct {
	JSONRPC string ` + "`json:\"jsonrpc\"`" + `
	ID      int64  ` + "`json:\"id\"`" + `
	Result  any    ` + "`json:\"result\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		var out resp
		out.JSONRPC = "2.0"
		out.ID = r.ID
		switch r.Method {
		case "initialize":
			out.Result = map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "mock", "version": "1.0"}}
		case "tools/list":
			out.Result = map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "回显", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
			}}
		case "tools/call":
			args, _ := r.Params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			out.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": "echo:" + text}}}
		default:
			out.Result = map[string]any{}
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
	}
}
`

// buildMockServer 编译 mock MCP 服务器到临时目录。
func buildMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(mockServerSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "mcp-mock")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build mock server: %v\n%s", err, out)
	}
	return bin
}

func TestStdioFullFlow(t *testing.T) {
	bin := buildMockServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tr, err := NewStdioTransport(ctx, bin)
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	defer tr.Close()
	c := NewClient("mock", "test", "0.0.1")
	if err := c.Initialize(ctx, tr); err != nil {
		t.Fatalf("init: %v", err)
	}
	tools, err := c.ListTools(ctx, tr)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", tools)
	}
	res, err := c.CallTool(ctx, tr, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Text() != "echo:hello" {
		t.Fatalf("text=%q", res.Text())
	}
}

func TestHTTPFullFlow(t *testing.T) {
	// HTTP mock 服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		id, _ := req["id"].(float64)
		method, _ := req["method"].(string)
		out := map[string]any{"jsonrpc": "2.0", "id": int64(id)}
		switch method {
		case "initialize":
			out["result"] = map[string]any{"protocolVersion": "2025-03-26"}
		case "tools/list":
			out["result"] = map[string]any{"tools": []map[string]any{{"name": "ping", "description": "p"}}}
		case "tools/call":
			out["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}}
		default:
			out["result"] = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tr := NewHTTPTransport(srv.URL, 5*time.Second)
	defer tr.Close()
	c := NewClient("http-mock", "test", "0.0.1")
	if err := c.Initialize(ctx, tr); err != nil {
		t.Fatalf("init: %v", err)
	}
	tools, err := c.ListTools(ctx, tr)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("tools: %+v", tools)
	}
	res, err := c.CallTool(ctx, tr, "ping", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Text() != "pong" {
		t.Fatalf("text=%q", res.Text())
	}
}

func TestExtractSSE(t *testing.T) {
	sse := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"ok\":false}}\n"
	out := extractSSEData([]byte(sse))
	if !strings.Contains(string(out), "ok\":false") {
		t.Fatalf("want last data, got %s", out)
	}
}

func TestStdioMissingBinary(t *testing.T) {
	ctx := context.Background()
	_, err := NewStdioTransport(ctx, "/nonexistent/mcp-server")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// 编译期接口断言
var _ Transport = (*StdioTransport)(nil)
var _ Transport = (*HTTPTransport)(nil)

func TestToolResultText(t *testing.T) {
	res := &ToolResult{Content: []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: "a"}, {Type: "image", Text: ""}, {Type: "text", Text: "b"}}}
	if res.Text() != "ab" {
		t.Fatalf("text=%q", res.Text())
	}
	_ = fmt.Sprint()
}
