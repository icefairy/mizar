// Package lsp 提供 LSP 客户端和服务器实现。
//
// 服务器模式：mizar 自身作为 LSP server，通过 stdio 与外部 IDE/editor 通信。
// 插件可通过 RegisterProvider 注册自定义 diagnostic/completion 能力。
package lsp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	jsonrpc2 "go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// Server 是 mizar 作为 LSP 服务器的实现。
//
// 通过 stdio 接收 JSON-RPC 请求，支持 initialize/initialized/shutdown/exit
// 以及 textDocument/completion/diagnostic 等标准 LSP 方法。
//
// 插件可通过 lsp.Provider 注册自定义能力。
type Server struct {
	conn jsonrpc2.Conn
	mu   sync.Mutex

	// 注册的能力提供者
	diagnosticProviders []DiagnosticProvider
	completionProviders []CompletionProvider

	// 服务端能力
	capabilities protocol.ServerCapabilities

	// 初始化状态
	initialized bool

	// 停止信号
	cancel context.CancelFunc
}

// DiagnosticProvider 插件注册的诊断提供者接口。
type DiagnosticProvider interface {
	// Diagnostics 返回指定文档的诊断列表。
	Diagnostics(ctx context.Context, documentURI string) ([]protocol.Diagnostic, error)
	// Name 返回提供者名称（用于日志/调试）。
	Name() string
}

// CompletionProvider 插件注册的补全提供者接口。
type CompletionProvider interface {
	// Completion 返回指定位置的补全列表。
	Completion(ctx context.Context, documentURI string, line, col int) ([]protocol.CompletionItem, error)
	// Name 返回提供者名称。
	Name() string
}

// ProviderConfig 服务端配置。
type ProviderConfig struct {
	// DiagnosticProviders 诊断提供者列表
	DiagnosticProviders []DiagnosticProvider
	// CompletionProviders 补全提供者列表
	CompletionProviders []CompletionProvider
}

// NewServer 创建新的 LSP 服务器。
func NewServer(cfg ProviderConfig) *Server {
	return &Server{
		diagnosticProviders: cfg.DiagnosticProviders,
		completionProviders: cfg.CompletionProviders,
	}
}

// Serve 启动服务器，通过 stdio 处理 LSP 请求。
// 阻塞直到连接断开或 ctx 取消。
func (s *Server) Serve(ctx context.Context, rw io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	defer cancel()

	stream := jsonrpc2.NewStream(rw)
	conn := jsonrpc2.NewConn(stream)
	s.conn = conn

	conn.Go(ctx, s.handle)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-conn.Done():
		return conn.Err()
	}
}

func (s *Server) handle(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	method := req.Method()
	switch method {
	case "initialize":
		return s.handleInitialize(ctx, req)
	case "initialized":
		return s.handleInitialized(ctx, req)
	case "shutdown":
		return s.handleShutdown(ctx, req)
	case "exit":
		return s.handleExit(ctx, req)
	case "textDocument/diagnostic":
		return s.handleDiagnostic(ctx, req)
	case "textDocument/completion":
		return s.handleCompletion(ctx, req)
	default:
		return nil, jsonrpc2.ErrMethodNotFound
	}
}

// handleInitialize 处理 initialize 请求。
func (s *Server) handleInitialize(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	var params protocol.InitializeParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return nil, jsonrpc2.ErrInvalidRequest
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	resolveProvider := true
	s.capabilities = protocol.ServerCapabilities{
		DiagnosticProvider: &protocol.DiagnosticOptions{},
		CompletionProvider: &protocol.CompletionOptions{
			TriggerCharacters: []string{".", "$", " "},
			ResolveProvider:   &resolveProvider,
		},
	}

	return protocol.InitializeResult{
		Capabilities: s.capabilities,
		ServerInfo: protocol.ServerInfo{
			Name:    "mizar-lsp",
			Version: protocol.NewOptional[string]("0.1.0"),
		},
	}, nil
}

// handleInitialized 处理 initialized 通知。
func (s *Server) handleInitialized(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialized = true
	return nil, nil
}

// handleShutdown 处理 shutdown 请求。
func (s *Server) handleShutdown(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialized = false
	return nil, nil
}

// handleExit 处理 exit 通知。
func (s *Server) handleExit(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return nil, nil
}

// handleDiagnostic 处理 textDocument/diagnostic 请求。
func (s *Server) handleDiagnostic(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	var params protocol.DocumentDiagnosticParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return nil, jsonrpc2.ErrInvalidRequest
	}

	var allDiags []protocol.Diagnostic
	for _, p := range s.diagnosticProviders {
		diags, err := p.Diagnostics(ctx, params.TextDocument.URI.FsPath())
		if err != nil {
			continue
		}
		allDiags = append(allDiags, diags...)
	}

	return protocol.FullDocumentDiagnosticReport{
		Kind:  "full",
		Items: allDiags,
	}, nil
}

// handleCompletion 处理 textDocument/completion 请求。
func (s *Server) handleCompletion(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	var params protocol.CompletionParams
	if err := json.Unmarshal(req.Params(), &params); err != nil {
		return nil, jsonrpc2.ErrInvalidRequest
	}

	var allItems []protocol.CompletionItem
	for _, p := range s.completionProviders {
		items, err := p.Completion(ctx, params.TextDocument.URI.FsPath(), int(params.Position.Line), int(params.Position.Character))
		if err != nil {
			continue
		}
		allItems = append(allItems, items...)
	}

	return protocol.CompletionList{
		IsIncomplete: true,
		Items:        allItems,
	}, nil
}

// RegisterProvider 注册能力提供者（供插件使用）。
func (s *Server) RegisterProvider(p any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch v := p.(type) {
	case DiagnosticProvider:
		s.diagnosticProviders = append(s.diagnosticProviders, v)
	case CompletionProvider:
		s.completionProviders = append(s.completionProviders, v)
	}
}

// RemoveProvider 按名字移除提供者（Diagnostic 与 Completion 同名都移除）。
func (s *Server) RemoveProvider(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	diags := s.diagnosticProviders[:0]
	for _, p := range s.diagnosticProviders {
		if p.Name() != name {
			diags = append(diags, p)
		}
	}
	s.diagnosticProviders = diags
	comps := s.completionProviders[:0]
	for _, p := range s.completionProviders {
		if p.Name() != name {
			comps = append(comps, p)
		}
	}
	s.completionProviders = comps
}

// --- 全局服务器实例（供插件注册使用） ---

var (
	globalServer  *Server
	globalServerMu sync.Mutex

	// pending 是在 server 初始化前注册的 provider，SetGlobalServer 时自动 drain。
	pendingDiag []DiagnosticProvider
	pendingComp []CompletionProvider
)

// GlobalServer 返回全局 LSP 服务器实例（如果已创建）。
func GlobalServer() *Server {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	return globalServer
}

// SetGlobalServer 设置全局 LSP 服务器实例。同时 drain pending provider。
func SetGlobalServer(s *Server) {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	globalServer = s
	for _, p := range pendingDiag {
		s.RegisterProvider(p)
	}
	for _, p := range pendingComp {
		s.RegisterProvider(p)
	}
	pendingDiag = nil
	pendingComp = nil
}

// RegisterDiagnosticProvider 注册诊断提供者。server 未初始化时暂存到 pending 队列。
func RegisterDiagnosticProvider(p DiagnosticProvider) error {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	if globalServer == nil {
		pendingDiag = append(pendingDiag, p)
		return nil
	}
	globalServer.RegisterProvider(p)
	return nil
}

// RegisterCompletionProvider 注册补全提供者。server 未初始化时暂存到 pending 队列。
func RegisterCompletionProvider(p CompletionProvider) error {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	if globalServer == nil {
		pendingComp = append(pendingComp, p)
		return nil
	}
	globalServer.RegisterProvider(p)
	return nil
}

// UnregisterDiagnosticProvider 按名字移除诊断提供者（插件热重载时用）。
// 同名 provider 全部移除。
func UnregisterDiagnosticProvider(name string) {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	// 清 pending
	kept := pendingDiag[:0]
	for _, p := range pendingDiag {
		if p.Name() != name {
			kept = append(kept, p)
		}
	}
	pendingDiag = kept
	if globalServer != nil {
		globalServer.RemoveProvider(name)
	}
}

// UnregisterCompletionProvider 按名字移除补全提供者（插件热重载时用）。
func UnregisterCompletionProvider(name string) {
	globalServerMu.Lock()
	defer globalServerMu.Unlock()
	kept := pendingComp[:0]
	for _, p := range pendingComp {
		if p.Name() != name {
			kept = append(kept, p)
		}
	}
	pendingComp = kept
	if globalServer != nil {
		globalServer.RemoveProvider(name)
	}
}

// --- stdio 模式便捷函数 ---

// ServeStdio 通过标准输入输出启动 LSP 服务器。
func ServeStdio(ctx context.Context, cfg ProviderConfig) error {
	s := NewServer(cfg)
	SetGlobalServer(s)
	return s.Serve(ctx, &stdioPipe{})
}

type stdioPipe struct{}

func (p *stdioPipe) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (p *stdioPipe) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (p *stdioPipe) Close() error                { return nil }
