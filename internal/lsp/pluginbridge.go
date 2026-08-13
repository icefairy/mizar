// Package lsp 插件桥接层：JS 插件通过宿主函数 lsp_register_diagnostic /
// lsp_register_completion 注册自定义 LSP Provider，无需 Go 代码。
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// jsDiagnosticProvider 包装 JS 回调为 DiagnosticProvider。
//
// JS 插件签名：lsp_register_diagnostic(name, fn)
//   fn(uri string) => jsonString
//   jsonString: [{"startLine":0,"startChar":0,"endLine":0,"endChar":5,"severity":1,"message":"...","source":"myplugin"}]
//
// severity: 1=ERROR, 2=WARN, 3=INFO, 4=HINT
type jsDiagnosticProvider struct {
	name string
	fn   func(uri string) string // JS 回调（goja 自动适配）
}

// NewJSDiagnosticProvider 创建 JS 诊断提供者桥接。
func NewJSDiagnosticProvider(name string, fn func(uri string) string) DiagnosticProvider {
	return &jsDiagnosticProvider{name: name, fn: fn}
}

func (p *jsDiagnosticProvider) Diagnostics(ctx context.Context, documentURI string) ([]protocol.Diagnostic, error) {
	callMu.Lock()
	defer callMu.Unlock()
	raw := p.fn(documentURI)
	return parseDiagnostics(raw)
}

func (p *jsDiagnosticProvider) Name() string { return p.name }

// jsCompletionProvider 包装 JS 回调为 CompletionProvider。
//
// JS 插件签名：lsp_register_completion(name, fn)
//   fn(uri string, line int, col int) => jsonString
//   jsonString: [{"label":"foo","kind":3,"detail":"...","insertText":"..."}]
//
// kind: 1=text,2=method,3=function,4=constructor,5=field,6=variable,
//       7=class,8=interface,9=module,10=property,11=unit,12=value,
//       13=enum,14=keyword,15=snippet
type jsCompletionProvider struct {
	name string
	fn   func(uri string, line, col int) string // JS 回调
}

// NewJSCompletionProvider 创建 JS 补全提供者桥接。
func NewJSCompletionProvider(name string, fn func(uri string, line, col int) string) CompletionProvider {
	return &jsCompletionProvider{name: name, fn: fn}
}

func (p *jsCompletionProvider) Completion(ctx context.Context, documentURI string, line, col int) ([]protocol.CompletionItem, error) {
	callMu.Lock()
	defer callMu.Unlock()
	raw := p.fn(documentURI, line, col)
	return parseCompletionItems(raw)
}

func (p *jsCompletionProvider) Name() string { return p.name }

// callMu 序列化解 JS 回调调用（goja Runtime 非线程安全）。
var callMu sync.Mutex

// --- JSON 解析（字符串 ↔ protocol 类型） ---

// jsDiagnostic 是 JS 插件返回的诊断 JSON 结构。
type jsDiagnostic struct {
	StartLine int    `json:"startLine"`
	StartChar int    `json:"startChar"`
	EndLine   int    `json:"endLine"`
	EndChar   int    `json:"endChar"`
	Severity  int    `json:"severity"`
	Message   string `json:"message"`
	Source    string `json:"source,omitempty"`
	Code      string `json:"code,omitempty"`
}

// jsCompletionItem 是 JS 插件返回的补全 JSON 结构。
type jsCompletionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind,omitempty"`
	Detail     string `json:"detail,omitempty"`
	SortText   string `json:"sortText,omitempty"`
	InsertText string `json:"insertText,omitempty"`
}

func parseDiagnostics(raw string) ([]protocol.Diagnostic, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var items []jsDiagnostic
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("lsp diagnostic parse: %w", err)
	}
	out := make([]protocol.Diagnostic, 0, len(items))
	for _, d := range items {
		severity := protocol.DiagnosticSeverityError
		switch d.Severity {
		case 1:
			severity = protocol.DiagnosticSeverityError
		case 2:
			severity = protocol.DiagnosticSeverityWarning
		case 3:
			severity = protocol.DiagnosticSeverityInformation
		case 4:
			severity = protocol.DiagnosticSeverityHint
		}
		out = append(out, protocol.Diagnostic{
			Range:    protocol.Range{Start: protocol.Position{Line: uint32(d.StartLine), Character: uint32(d.StartChar)}, End: protocol.Position{Line: uint32(d.EndLine), Character: uint32(d.EndChar)}},
			Severity: severity,
			Message:  protocol.String(d.Message),
			Source:   protocol.NewOptional[string](d.Source),
		})
	}
	return out, nil
}

func parseCompletionItems(raw string) ([]protocol.CompletionItem, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var items []jsCompletionItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("lsp completion parse: %w", err)
	}
	out := make([]protocol.CompletionItem, 0, len(items))
	for _, c := range items {
		item := protocol.CompletionItem{
			Label: c.Label,
			Kind:  protocol.CompletionItemKind(c.Kind),
		}
		if c.Detail != "" {
			item.Detail = protocol.NewOptional[string](c.Detail)
		}
		if c.SortText != "" {
			item.SortText = protocol.NewOptional[string](c.SortText)
		}
		if c.InsertText != "" {
			item.InsertText = protocol.NewOptional[string](c.InsertText)
		}
		out = append(out, item)
	}
	return out, nil
}

// Ensure uri import is used.
var _ = uri.File