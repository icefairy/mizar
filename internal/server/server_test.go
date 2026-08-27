package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mizar/internal/agent"
	"mizar/internal/engine"
	"mizar/internal/plugins"
	"mizar/internal/session"
)

// mockLLM 测试用 LLM（scriptLLM 是 agent 包私有，server 测试自备）。
type mockLLM struct{ Fn func(msgs []agent.Message) (string, error) }

func (m *mockLLM) Chat(msgs []agent.Message) (string, error) {
	if m.Fn != nil {
		return m.Fn(msgs)
	}
	return `{"action":"reply","text":"回答完毕"}`, nil
}

func (m *mockLLM) ModelName() string { return "mock" }

// testServer 构建一个测试 Server（httptest 起真 HTTP，方便测 WS）。
func testServer(t *testing.T) (*Server, *httptest.Server, *agent.Agent) {
	t.Helper()
	llm := &mockLLM{}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	return srv, ts, a
}

// testPluginManager 建一个空插件管理器。
func testPluginManager(t *testing.T) *plugins.Manager {
	t.Helper()
	// 空宿主函数集（server 测试不需要插件能力，只测协议层）
	return plugins.NewManager(t.TempDir(), &engine.HostFuncs{})
}

// TestPluginRPCOverWS 插件注册的 rpc_<name> 方法应经 WS 可达。
func TestPluginRPCOverWS(t *testing.T) {
	t.Helper()
	// 带 rpc_* 导出的插件管理器
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "myext.ts"), []byte(`
export function rpc_echo(params) {
  return "echo:" + (params || "");
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	pm := plugins.NewManager(dir, &engine.HostFuncs{})
	if _, failed := pm.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	llm := &mockLLM{}
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	// WS 连接后调用自定义方法 rpc.echo
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	hdr := http.Header{"Authorization": []string{"Bearer test-token"}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "p1", "method": "echo", "params": map[string]any{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := ws.ReadJSON(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != nil {
		t.Fatalf("rpc error: %v", resp["error"])
	}
	// 非 JSON 字符串返回走 {"result": <string>} 兜底
	inner, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("result type=%T val=%v", resp["result"], resp["result"])
	}
	if got := inner["result"]; got != "echo:{\"x\":1}" {
		t.Fatalf("result=%v", got)
	}
}

// TestPluginWSEmit 插件 ws_emit 应广播自定义事件给所有 WS 客户端。
func TestPluginWSEmit(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "emit.ts"), []byte(`
export function rpc_boom(params) {
  ws_emit("my_event", JSON.stringify({level: "warn", msg: "hello"}));
  return "sent";
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &engine.HostFuncs{}
	pm := plugins.NewManager(dir, host)
	if _, failed := pm.LoadAll(); len(failed) > 0 {
		t.Fatalf("load failed: %v", failed)
	}
	llm := &mockLLM{}
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	// 注入 ws_emit → Server 广播，并强制重载使插件引擎拿到该函数
	host.WSEmit = func(event, dataJSON string) { srv.EmitToWS(event, dataJSON) }
	pm.ReloadAll()
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	hdr := http.Header{"Authorization": []string{"Bearer test-token"}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "b1", "method": "boom", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	// 第一条是 ack/result，随后应收到 my_event 广播
	seen := map[string]bool{}
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 3; i++ {
		var resp map[string]any
		if err := ws.ReadJSON(&resp); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if resp["type"] == "event" {
			if resp["event"] != "my_event" {
				t.Fatalf("unexpected event: %v", resp)
			}
			seen["event"] = true
		}
		if resp["id"] == "b1" {
			seen["result"] = true
			break
		}
	}
	if !seen["event"] {
		t.Fatal("did not receive my_event broadcast")
	}
}

// TestOpenAICompat 验证 /v1/models + /v1/chat/completions。
func TestOpenAICompat(t *testing.T) {
	_, ts, _ := testServer(t)

	// /v1/models
	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("models status = %d", resp.StatusCode)
	}
	var models map[string]any
	json.NewDecoder(resp.Body).Decode(&models)
	if models["object"] != "list" {
		t.Fatalf("models object = %v", models["object"])
	}

	// /v1/chat/completions
	body := `{"model":"mizar","messages":[{"role":"user","content":"你好"}]}`
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chat status = %d", resp.StatusCode)
	}
	var chat map[string]any
	json.NewDecoder(resp.Body).Decode(&chat)
	choices := chat["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "回答完毕" {
		t.Fatalf("chat content = %v", msg["content"])
	}
}

// TestRPCSteerAbort 验证 JSON-RPC：run + steer + abort。
func TestRPCSteerAbort(t *testing.T) {
	_, ts, a := testServer(t)

	// ping
	if got := rpcCall(t, ts.URL, "system.ping", nil); got["pong"] != true {
		t.Fatalf("ping = %v", got)
	}

	// tools.list
	tools := rpcCall(t, ts.URL, "tools.list", nil)
	if _, ok := tools["tools"].([]any); !ok {
		t.Fatalf("tools.list = %v", tools)
	}

	// agent.run
	res := rpcCall(t, ts.URL, "agent.run", map[string]any{"task": "你好"})
	if res["reply"] != "回答完毕" {
		t.Fatalf("run reply = %v", res["reply"])
	}

	// steer（注入成功即 ok）
	rpcCall(t, ts.URL, "agent.steer", map[string]any{"message": "方向错了，重来"})

	// abort
	rpcCall(t, ts.URL, "agent.abort", nil)
	if !a.Aborted() {
		t.Fatal("abort not set")
	}
}

// TestWSStreamAndSteer 验证 WS：连接 → run → 收到事件 → steer ack。
func TestWSStreamAndSteer(t *testing.T) {
	_, ts, _ := testServer(t)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 发 agent.steer，应收到 ack
	conn.WriteJSON(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "agent.steer", Params: json.RawMessage(`{"message":"纠正"}`)})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack map[string]any
	conn.ReadJSON(&ack)
	if ack["type"] != "ack" {
		t.Fatalf("ack = %v", ack)
	}
}

// TestDynamicSwitch 动态开关：关闭后 503，再打开恢复。
func TestDynamicSwitch(t *testing.T) {
	_, ts, _ := testServer(t)

	// 初始状态全开
	status := rpcCall(t, ts.URL, "system.ping", nil) // 不依赖
	_ = status
	// 关掉 jsonrpc
	rpcCall(t, ts.URL, "system.ping", nil) // ok before
	body := `{"service":"jsonrpc","enabled":false}`
	req, _ := http.NewRequest("POST", ts.URL+"/admin/switch", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 关闭后 /rpc 应返回 503
	req2, _ := http.NewRequest("POST", ts.URL+"/rpc", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"system.ping"}`))
	req2.Header.Set("Authorization", "Bearer test-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("disabled rpc status = %d, want 503", resp2.StatusCode)
	}

	// 再打开
	body = `{"service":"jsonrpc","enabled":true}`
	req3, _ := http.NewRequest("POST", ts.URL+"/admin/switch", bytes.NewBufferString(body))
	req3.Header.Set("Authorization", "Bearer test-token")
	resp3, _ := http.DefaultClient.Do(req3)
	resp3.Body.Close()
	// 重建 rpc 请求（body 不可复用）
	req4, _ := http.NewRequest("POST", ts.URL+"/rpc", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"system.ping"}`))
	req4.Header.Set("Authorization", "Bearer test-token")
	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Fatalf("re-enabled rpc status = %d, want 200", resp4.StatusCode)
	}
}

// TestAuth 无 token 返回 401。
func TestAuth(t *testing.T) {
	_, ts, _ := testServer(t)
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", resp.StatusCode)
	}
}

// rpcCall 辅助：发起 JSON-RPC 请求并返回 result。
func rpcCall(t *testing.T, baseURL, method string, params any) map[string]any {
	t.Helper()
	var paramsRaw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		paramsRaw = b
	} else {
		paramsRaw = json.RawMessage(`{}`)
	}
	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(paramsRaw),
	})
	req, _ := http.NewRequest("POST", baseURL+"/rpc", bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if e, ok := out["error"]; ok {
		t.Fatalf("rpc %s error: %v", method, e)
	}
	result, _ := out["result"].(map[string]any)
	return result
}

// streamMockLLM 支持流式的 mock：按分片回调后返回完整 reply。
type streamMockLLM struct {
	chunks []string // 依次回调的 content 分片
}

func (m *streamMockLLM) Chat(msgs []agent.Message) (string, error) { return strings.Join(m.chunks, ""), nil }
func (m *streamMockLLM) ModelName() string                          { return "mock-stream" }
func (m *streamMockLLM) ChatStream(msgs []agent.Message, onToken func(agent.StreamDelta)) (string, error) {
	for _, c := range m.chunks {
		onToken(agent.StreamDelta{Content: c})
	}
	return strings.Join(m.chunks, ""), nil
}

// TestChatCompletionsStream 验证 stream=true 时输出 OpenAI 兼容 SSE。
func TestChatCompletionsStream(t *testing.T) {
	// 流式 mock：分片为协议 JSON 增量（agent 循环可解析；提取器转发干净 reply 文本）
	llm := &streamMockLLM{chunks: []string{"{\"action\":\"reply\",\"text\":\"第", "一段，", "第二段\"}"}}
	pm := testPluginManager(t)
	a := agent.New(llm, pm)
	cfg := NewConfig("127.0.0.1:0", "test-token")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	body := `{"model":"mock-stream","stream":true,"messages":[{"role":"user","content":"讲故事"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}
	data, _ := io.ReadAll(resp.Body)
	s := string(data)
	// 断言各增量分片均出现在 SSE 流中（分片是独立的 content 增量，不保证连续子串）
	for _, want := range []string{"data: [DONE]", `"role":"assistant"`, `"content":"第"`, `"content":"一段，"`, `"content":"第二段"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("SSE 缺少 %q:\n%s", want, s)
		}
	}
	// 回复应为提取后的干净文本，不应泄漏协议 JSON 控制字符
	if strings.Contains(s, `"action"`) {
		t.Fatalf("SSE 泄漏协议 JSON:\n%s", s)
	}
}

// TestChatCompletionsStreamFallback 验证不支持流式时以 SSE 格式一次性输出。
func TestChatCompletionsStreamFallback(t *testing.T) {
	srv, ts, _ := testServer(t) // mockLLM 不支持流式
	_ = srv
	body := `{"model":"mock","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	s := string(data)
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("回退 SSE 缺少 [DONE]:\n%s", s)
	}
	if !strings.Contains(s, `"content":"回答完毕"`) {
		t.Fatalf("回退 SSE 缺少完整回复:\n%s", s)
	}
}
