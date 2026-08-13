// Package lsp 提供 LSP 客户端和服务端实现。
// 客户端用于连接 gopls/tsserver 等语言服务器，获取诊断、定义跳转、补全等能力。
package lsp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	jsonrpc2 "go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// Client 封装与语言服务器 (gopls/tsserver) 的 LSP 通信。
type Client struct {
	conn jsonrpc2.Conn
	cmd  *exec.Cmd
	mu   sync.Mutex

	capabilities protocol.ServerCapabilities
	openDocs     map[string]int32

	config ClientConfig
	cancel context.CancelFunc
}

// ClientConfig 客户端配置。
type ClientConfig struct {
	ServerBinary string
	ServerArgs   []string
	Timeout      time.Duration
	Workspace    string
}

// NewClient 创建新的 LSP 客户端并启动语言服务器。
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.ServerBinary == "" {
		return nil, fmt.Errorf("lsp: server binary path required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())

	args := []string{"--stdio"}
	if len(cfg.ServerArgs) > 0 {
		args = append(args, cfg.ServerArgs...)
	}
	cmd := exec.CommandContext(ctx, cfg.ServerBinary, args...)
	cmd.Dir = cfg.Workspace

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("lsp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("lsp: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("lsp: start server: %w", err)
	}

	combined := &rpcPipe{stdin: stdin, stdout: stdout}
	stream := jsonrpc2.NewStream(combined)
	conn := jsonrpc2.NewConn(stream)

	c := &Client{
		conn:     conn,
		cmd:      cmd,
		openDocs: make(map[string]int32),
		config:   cfg,
		cancel:   cancel,
	}

	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("lsp: initialize: %w", err)
	}
	return c, nil
}

// Close 关闭 LSP 客户端。
func (c *Client) Close() error {
	c.cancel()
	return c.cmd.Wait()
}

func (c *Client) initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	params := protocol.InitializeParams{
		ClientInfo: protocol.ClientInfo{
			Name:    "mizar",
			Version: protocol.NewOptional[string]("0.1.0"),
		},
	}

	var result protocol.InitializeResult
	_, err := c.conn.Call(ctx, "initialize", params, &result)
	if err != nil {
		return err
	}

	if err := c.conn.Notify(ctx, "initialized", protocol.InitializedParams{}); err != nil {
		return err
	}

	c.capabilities = result.Capabilities
	return nil
}

// DidOpen 通知服务器打开文档。
func (c *Client) DidOpen(ctx context.Context, uriStr, content, languageID string) error {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	c.mu.Lock()
	v := c.openDocs[uriStr]
	v++
	c.openDocs[uriStr] = v
	c.mu.Unlock()

	params := protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        uri.File(uriStr),
			LanguageID: protocol.LanguageKind(languageID),
			Version:    v,
			Text:       content,
		},
	}
	return c.conn.Notify(ctx, "textDocument/didOpen", params)
}

// DidChange 通知服务器文档内容变更。
func (c *Client) DidChange(ctx context.Context, uriStr string, oldText, newText string, line, col int) error {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	c.mu.Lock()
	v := c.openDocs[uriStr]
	v++
	c.openDocs[uriStr] = v
	c.mu.Unlock()

	oldLen := len(oldText)
	newLen := len(newText)
	length := oldLen - newLen
	if length < 0 {
		length = 0
	}

	params := protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
			Version:                v,
		},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			&protocol.TextDocumentContentChangePartial{
				Range: protocol.Range{
					Start: protocol.Position{Line: uint32(line), Character: uint32(col)},
					End:   protocol.Position{Line: uint32(line), Character: uint32(col + length)},
				},
				Text: newText,
			},
		},
	}
	return c.conn.Notify(ctx, "textDocument/didChange", params)
}

// DidClose 通知服务器关闭文档。
func (c *Client) DidClose(ctx context.Context, uriStr string) error {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	c.mu.Lock()
	delete(c.openDocs, uriStr)
	c.mu.Unlock()

	params := protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
	}
	return c.conn.Notify(ctx, "textDocument/didClose", params)
}

// Diagnostics 获取指定文档的诊断信息。
func (c *Client) Diagnostics(ctx context.Context, uriStr string) ([]DiagnosticInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	params := protocol.DocumentDiagnosticParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
	}

	var report protocol.FullDocumentDiagnosticReportOrUnchangedDocumentDiagnosticReport
	_, err := c.conn.Call(ctx, "textDocument/diagnostic", params, &report)
	if err != nil {
		return nil, err
	}

	diagnostics, err := extractDiagnostics(report)
	if err != nil {
		return nil, err
	}

	out := make([]DiagnosticInfo, 0, len(diagnostics))
	for _, d := range diagnostics {
		msg := extractString(d.Message)
		source, _ := d.Source.Get()
		out = append(out, DiagnosticInfo{
			Severity:  int(d.Severity),
			Message:   msg,
			Source:    source,
			StartLine: int(d.Range.Start.Line),
			EndLine:   int(d.Range.End.Line),
			StartChar: int(d.Range.Start.Character),
			EndChar:   int(d.Range.End.Character),
		})
	}
	return out, nil
}

// Definition 跳转到定义。
func (c *Client) Definition(ctx context.Context, uriStr string, line, col int) ([]Location, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	params := protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
			Position:     protocol.Position{Line: uint32(line), Character: uint32(col)},
		},
	}

	var locations []protocol.Location
	if _, err := c.conn.Call(ctx, "textDocument/definition", params, &locations); err != nil {
		var links []protocol.LocationLink
		if _, err2 := c.conn.Call(ctx, "textDocument/definition", params, &links); err2 == nil {
			return locationLinksToLocations(links), nil
		}
		return nil, err
	}
	return locationsToLocations(locations), nil
}

// References 查找所有引用。
func (c *Client) References(ctx context.Context, uriStr string, line, col int) ([]Location, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	params := protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
			Position:     protocol.Position{Line: uint32(line), Character: uint32(col)},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	}

	var locations []protocol.Location
	_, err := c.conn.Call(ctx, "textDocument/references", params, &locations)
	if err != nil {
		return nil, err
	}
	return locationsToLocations(locations), nil
}

// Completion 获取补全建议。
func (c *Client) Completion(ctx context.Context, uriStr string, line, col int) ([]CompletionItem, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()

	params := protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(uriStr)},
			Position:     protocol.Position{Line: uint32(line), Character: uint32(col)},
		},
	}

	var items []protocol.CompletionItem
	if _, err := c.conn.Call(ctx, "textDocument/completion", params, &items); err != nil {
		var list protocol.CompletionList
		if _, err2 := c.conn.Call(ctx, "textDocument/completion", params, &list); err2 == nil {
			items = list.Items
		} else {
			return nil, err
		}
	}

	out := make([]CompletionItem, 0, len(items))
	for _, item := range items {
		detail, _ := item.Detail.Get()
		sortText, _ := item.SortText.Get()
		insertText, _ := item.InsertText.Get()
		out = append(out, CompletionItem{
			Label:      item.Label,
			Kind:       int(item.Kind),
			Detail:     detail,
			SortText:   sortText,
			InsertText: insertText,
		})
	}
	return out, nil
}

// ServerCapabilities 返回服务端能力信息。
func (c *Client) ServerCapabilities() protocol.ServerCapabilities {
	return c.capabilities
}

// --- 辅助类型 ---

type rpcPipe struct {
	stdin  io.Writer
	stdout io.Reader
}

func (p *rpcPipe) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *rpcPipe) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *rpcPipe) Close() error {
	if w, ok := p.stdin.(io.Closer); ok {
		w.Close()
	}
	if r, ok := p.stdout.(io.Closer); ok {
		r.Close()
	}
	return nil
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Location struct {
	URI   string `json:"uri"`
	Start Position `json:"range.start"`
	End   Position `json:"range.end"`
}

type DiagnosticInfo struct {
	Severity  int    `json:"severity"`
	Message   string `json:"message"`
	Source    string `json:"source,omitempty"`
	Code      string `json:"code,omitempty"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	StartChar int    `json:"startChar"`
	EndChar   int    `json:"endChar"`
}

type CompletionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind,omitempty"`
	Detail     string `json:"detail,omitempty"`
	SortText   string `json:"sortText,omitempty"`
	InsertText string `json:"insertText,omitempty"`
}

func extractDiagnostics(report protocol.FullDocumentDiagnosticReportOrUnchangedDocumentDiagnosticReport) ([]protocol.Diagnostic, error) {
	switch v := report.(type) {
	case *protocol.FullDocumentDiagnosticReport:
		return v.Items, nil
	case *protocol.UnchangedDocumentDiagnosticReport:
		return nil, nil
	default:
		return nil, fmt.Errorf("lsp: unknown diagnostic report type: %T", report)
	}
}

func extractString(msg protocol.InlayHintTooltip) string {
	switch v := msg.(type) {
	case *protocol.MarkupContent:
		return v.Value
	case protocol.String:
		return string(v)
	default:
		if v != nil {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
}

func locationsToLocations(locs []protocol.Location) []Location {
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		out = append(out, Location{
			URI:   l.URI.FsPath(),
			Start: Position{Line: int(l.Range.Start.Line), Character: int(l.Range.Start.Character)},
			End:   Position{Line: int(l.Range.End.Line), Character: int(l.Range.End.Character)},
		})
	}
	return out
}

func locationLinksToLocations(links []protocol.LocationLink) []Location {
	out := make([]Location, 0, len(links))
	for _, l := range links {
		out = append(out, Location{
			URI:   l.TargetURI.FsPath(),
			Start: Position{Line: int(l.TargetRange.Start.Line), Character: int(l.TargetRange.Start.Character)},
			End:   Position{Line: int(l.TargetRange.End.Line), Character: int(l.TargetRange.End.Character)},
		})
	}
	return out
}

// MarshalDiagnostics 将诊断列表序列化为可读字符串。
func MarshalDiagnostics(diags []DiagnosticInfo) string {
	if len(diags) == 0 {
		return "（无诊断）"
	}
	var sb strings.Builder
	for _, d := range diags {
		sb.WriteString(fmt.Sprintf("[%s] L%d:C%d - %s (%s)\n",
			severityName(d.Severity), d.StartLine+1, d.StartChar+1, d.Message, d.Source))
	}
	return sb.String()
}

func severityName(s int) string {
	switch s {
	case 1:
		return "ERROR"
	case 2:
		return "WARN"
	case 3:
		return "INFO"
	case 4:
		return "HINT"
	default:
		return "UNKN"
	}
}

// MarshalLocations 将 Location 列表序列化为可读字符串。
func MarshalLocations(locs []Location) string {
	if len(locs) == 0 {
		return "（无匹配）"
	}
	if len(locs) == 1 {
		l := locs[0]
		return fmt.Sprintf("%s:%d:%d", l.URI, l.Start.Line+1, l.Start.Character+1)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d 个匹配:\n", len(locs)))
	for _, l := range locs {
		sb.WriteString(fmt.Sprintf("- %s:%d:%d\n", l.URI, l.Start.Line+1, l.Start.Character+1))
	}
	return sb.String()
}