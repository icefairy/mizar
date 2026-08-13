// Package mcp 实现 MCP (Model Context Protocol) 客户端，支持 stdio 和 HTTP (streamable) 传输。
// 协议参考: https://modelcontextprotocol.io/specification
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// 协议常量
const (
	JSONRPCVersion = "2.0"
	// 方法
	MethodInitialize     = "initialize"
	MethodToolsList      = "tools/list"
	MethodToolsCall      = "tools/call"
	MethodNotificationsInitialized = "notifications/initialized"
)

// MCPError 协议错误。
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *MCPError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// rpcRequest JSON-RPC 请求。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

// Tool MCP 工具描述。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// ToolResult 工具调用结果。
type ToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// Text 提取结果文本。
func (r *ToolResult) Text() string {
	var sb bytes.Buffer
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// Client MCP 客户端（传输无关）。
type Client struct {
	mu      sync.Mutex
	nextID  int64
	server  string
	name    string
	version string
}

// NewClient 创建客户端。
func NewClient(server, name, version string) *Client {
	if version == "" {
		version = "0.1.0"
	}
	return &Client{server: server, name: name, version: version}
}

// Transport 传输接口。
type Transport interface {
	// Call 发送请求并等待匹配 ID 的响应。
	Call(ctx context.Context, req rpcRequest) (json.RawMessage, error)
	// Close 关闭传输。
	Close() error
}

// Initialize 握手。
func (c *Client) Initialize(ctx context.Context, tr Transport) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	params := map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo": map[string]any{
			"name":    c.name,
			"version": c.version,
		},
	}
	_, err := tr.Call(ctx, rpcRequest{JSONRPC: JSONRPCVersion, ID: id, Method: MethodInitialize, Params: params})
	return err
}

// ListTools 列出工具。
func (c *Client) ListTools(ctx context.Context, tr Transport) ([]Tool, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	raw, err := tr.Call(ctx, rpcRequest{JSONRPC: JSONRPCVersion, ID: id, Method: MethodToolsList})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode tools: %w", err)
	}
	return res.Tools, nil
}

// CallTool 调用工具。
func (c *Client) CallTool(ctx context.Context, tr Transport, name string, args map[string]any) (*ToolResult, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	raw, err := tr.Call(ctx, rpcRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  MethodToolsCall,
		Params:  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return nil, err
	}
	var res ToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode result: %w", err)
	}
	return &res, nil
}

// ---------------------------------------------------------------------------
// stdio 传输
// ---------------------------------------------------------------------------

// StdioTransport 通过子进程 stdin/stdout 通信（JSON-RPC 按行）。
type StdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	pending map[int64]chan json.RawMessage
}

// NewStdioTransport 启动 MCP 服务器子进程。
func NewStdioTransport(ctx context.Context, command string, args ...string) (*StdioTransport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, _ := cmd.StderrPipe()
	go func() {
		// 丢弃 stderr，避免管道阻塞
		io.Copy(io.Discard, stderr)
	}()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	tr := &StdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		scanner: bufio.NewScanner(stdout),
		pending: make(map[int64]chan json.RawMessage),
	}
	go tr.readLoop()
	return tr, nil
}

func (t *StdioTransport) readLoop() {
	t.scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for t.scanner.Scan() {
		line := t.scanner.Bytes()
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // 忽略非响应消息
		}
		t.mu.Lock()
		ch, ok := t.pending[resp.ID]
		if ok {
			delete(t.pending, resp.ID)
		}
		t.mu.Unlock()
		if !ok {
			continue
		}
		if resp.Error != nil {
			ch <- nil // 错误通过特殊标记传回（result 为 nil）
			close(ch)
			continue
		}
		ch <- resp.Result
		close(ch)
	}
}

// Call 实现 Transport。
func (t *StdioTransport) Call(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	t.mu.Lock()
	ch := make(chan json.RawMessage, 1)
	t.pending[req.ID] = ch
	t.mu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	_, err = t.stdin.Write(append(b, '\n'))
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		if res == nil {
			return nil, fmt.Errorf("mcp rpc error on %s", req.Method)
		}
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close 关闭子进程。
func (t *StdioTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return t.cmd.Wait()
}

// ---------------------------------------------------------------------------
// HTTP (streamable) 传输
// ---------------------------------------------------------------------------

// HTTPTransport 通过 HTTP POST JSON-RPC 通信。
type HTTPTransport struct {
	url    string
	client *http.Client
}

// NewHTTPTransport 创建 HTTP 传输。
func NewHTTPTransport(url string, timeout time.Duration) *HTTPTransport {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &HTTPTransport{url: url, client: &http.Client{Timeout: timeout}}
}

// Call 实现 Transport。
func (t *HTTPTransport) Call(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	// 支持 SSE 响应（event: message\ndata: {...}）
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("event:")) || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		body = extractSSEData(body)
	}
	var out rpcResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, truncate(string(body), 200))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// Close 无资源需要释放。
func (t *HTTPTransport) Close() error { return nil }

// extractSSEData 从 SSE 文本中提取最后一个 data: 的 JSON。
func extractSSEData(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	var last []byte
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			last = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
	}
	return last
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
