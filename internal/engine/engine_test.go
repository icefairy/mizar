package engine

import (
	"strings"
	"testing"
)

// mockHost 提供全功能宿主函数用于测试。
func mockHost() *HostFuncs {
	return &HostFuncs{
		HTTPGet: func(url string) (string, error) { return "HTTP:" + url, nil },
		HTTPRequest: func(method, url, body, headersJSON string) (string, error) {
			return "REQ:" + method + ":" + url + ":" + body + ":" + headersJSON, nil
		},
		JSONDecode: func(s string) (map[string]any, error) {
			return map[string]any{"parsed": s}, nil
		},
		FSRead:  func(p string) (string, error) { return "file:" + p, nil },
		FSWrite: func(p, c string) error { return nil },
		DBExecBatch: DBExecBatchFn,
		DBClose: DBCloseFn,
		LLMChat: func(messagesJSON string) (string, error) {
			return "mock-llm-reply", nil
		},
		Log: func(msg string) {},
		DBQuery: DBQueryFn,
	}
}

func TestCompileTSBasic(t *testing.T) {
	src := `export function tool_hello(args: string): string { return "hi " + args; }`
	js, err := CompileTS("test.ts", src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(js, "tool_hello") {
		t.Fatalf("compiled js missing tool_hello: %s", js)
	}
}

func TestCompileTSError(t *testing.T) {
	_, err := CompileTS("bad.ts", `export function broken( {`)
	if err == nil {
		t.Fatal("expected compile error")
	}
}

func TestRunAndCall(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("t.ts", `export function tool_add(args: string): string { return String(Number(args)+1); }`)
	if err := e.RunScript("t.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !e.Has("tool_add") {
		t.Fatal("tool_add not registered")
	}
	res, err := e.Call("tool_add", "41")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res != "42" {
		t.Fatalf("want 42 got %v", res)
	}
	names, _ := e.GlobalNames()
	found := false
	for _, n := range names {
		if n == "tool_add" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GlobalNames missing tool_add: %v", names)
	}
}

func TestHostFuncsVisible(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("h.ts", `export function tool_probe(): string { return http_get("/probe") + "|" + fs_read("/x") + "|" + llm_chat("q"); }`)
	if err := e.RunScript("h.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := e.Call("tool_probe")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := "HTTP:/probe|file:/x|mock-llm-reply"
	if res != want {
		t.Fatalf("want %q got %q", want, res)
	}
}

// TestHTTPRequestHost 验证统一 http_request(method,url,body) 可用。
func TestHTTPRequestHost(t *testing.T) {
	e, err := New(mockHost())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()
	js, _ := CompileTS("h.ts", `export function tool_req(): string { return http_request("PUT", "http://x/api", "{\"a\":1}", "{\"X-Test\":\"v\"}"); }`)
	if err := e.RunScript("h.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, err := e.Call("tool_req")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := `REQ:PUT:http://x/api:{"a":1}:{"X-Test":"v"}`
	if res != want {
		t.Fatalf("want %q got %q", want, res)
	}
}
