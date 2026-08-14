package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildEchoServer 编译一个极简 stdio MCP server（echo 工具），返回二进制路径。
func buildEchoServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	code := `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type rpcReq struct {
	ID     int64  ` + "`json:\"id\"`" + `
	Method string ` + "`json:\"method\"`" + `
	Params json.RawMessage ` + "`json:\"params\"`" + `
}

type rpcRes struct {
	JSONRPC string      ` + "`json:\"jsonrpc\"`" + `
	ID      int64       ` + "`json:\"id\"`" + `
	Result  interface{} ` + "`json:\"result,omitempty\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req rpcReq
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			enc.Encode(rpcRes{"2.0", req.ID, map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "echo", "version": "0.0.1"},
			}})
		case "tools/list":
			enc.Encode(rpcRes{"2.0", req.ID, map[string]any{"tools": []map[string]any{{
				"name": "echo", "description": "回显",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
					"text": map[string]any{"type": "string"},
				}},
			}}}})
		case "tools/call":
			var params struct {
				Name      string         ` + "`json:\"name\"`" + `
				Arguments map[string]any ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(req.Params, &params)
			text, _ := params.Arguments["text"].(string)
			enc.Encode(rpcRes{"2.0", req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echo:" + text}},
			}})
		case "notifications/initialized":
			// 忽略
		}
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "echo-mcp")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build echo server: %v\n%s", err, out)
	}
	return bin
}

// TestMCPCallStdio 验证 mcp_call 通过 stdio server 完整调用。
func TestMCPCallStdio(t *testing.T) {
	bin := buildEchoServer(t)
	reg := NewMCPRegistry([]MCPServerConf{
		{Name: "echo", Command: bin},
	})
	defer reg.Close()
	fn := reg.CallFn()

	res, err := fn("echo", "echo", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res != "echo:hello" {
		t.Fatalf("want echo:hello, got %q", res)
	}
}

// TestMCPCallUnconfigured 未配置 server 时报错。
func TestMCPCallUnconfigured(t *testing.T) {
	reg := NewMCPRegistry(nil)
	fn := reg.CallFn()
	_, err := fn("redis", "get", `{"key":"a"}`)
	if err == nil {
		t.Fatal("want error for unconfigured")
	}
}

// TestMCPCallUnknownServer 配置了但没这个 server 时报错。
func TestMCPCallUnknownServer(t *testing.T) {
	reg := NewMCPRegistry([]MCPServerConf{{Name: "echo", Command: "/nonexistent"}})
	fn := reg.CallFn()
	_, err := fn("nope", "x", "{}")
	if err == nil || !containsStr(err.Error(), "未找到") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// TestMCPCallBadArgsJSON argsJSON 非法时报错。
func TestMCPCallBadArgsJSON(t *testing.T) {
	reg := NewMCPRegistry([]MCPServerConf{{Name: "echo", Command: "/nonexistent"}})
	fn := reg.CallFn()
	_, err := fn("echo", "x", "not-json")
	if err == nil {
		t.Fatal("want error for bad argsJSON")
	}
}

// TestHostMCPCallVisible 验证 mcp_call 宿主函数在 goja 中可见可调。
func TestHostMCPCallVisible(t *testing.T) {
	bin := buildEchoServer(t)
	reg := NewMCPRegistry([]MCPServerConf{{Name: "echo", Command: bin}})
	defer reg.Close()

	host := mockHost()
	host.MCPCall = reg.CallFn()
	e, err := New(host)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer e.Close()

	js, _ := CompileTS("mc.ts", `export function tool_ping(): string {
  return mcp_call("echo", "echo", JSON.stringify({text: "hi"}));
}`)
	if err := e.RunScript("mc.ts", js); err != nil {
		t.Fatalf("run: %v", err)
	}
	out, err := e.Call("tool_ping")
	if err != nil {
		t.Fatalf("call tool_ping: %v", err)
	}
	if out != "echo:hi" {
		t.Fatalf("want echo:hi, got %q", out)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 防止 json/time 未使用的编译告警（保留给未来 HTTP 用例）。
var _ = json.Marshal
var _ = time.Second
