package lsp

import (
	"context"
	"encoding/json"
	"testing"

	"go.lsp.dev/protocol"
)

func TestParseDiagnostics(t *testing.T) {
	raw := `[{"startLine":3,"startChar":0,"endLine":3,"endChar":4,"severity":1,"message":"error here","source":"test"},{"startLine":5,"startChar":2,"endLine":5,"endChar":6,"severity":2,"message":"warn here"}]`
	diags, err := parseDiagnostics(raw)
	if err != nil {
		t.Fatalf("parseDiagnostics: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(diags))
	}
	d := diags[0]
	if d.Range.Start.Line != 3 || d.Range.Start.Character != 0 {
		t.Errorf("unexpected start range: %+v", d.Range.Start)
	}
	if d.Severity != protocol.DiagnosticSeverityError {
		t.Errorf("expected ERROR severity, got %v", d.Severity)
	}
	src, _ := d.Source.Get()
	if src != "test" {
		t.Errorf("expected source 'test', got %q", src)
	}
	msg, ok := d.Message.(protocol.String)
	if !ok || msg != "error here" {
		t.Errorf("expected message 'error here', got %v (type %T)", d.Message, d.Message)
	}
	// 第二条 source 为空 → Optional unset
	d2 := diags[1]
	if d2.Severity != protocol.DiagnosticSeverityWarning {
		t.Errorf("expected WARN severity, got %v", d2.Severity)
	}
	_, set := d2.Source.Get()
	if set {
		t.Errorf("expected unset source for second diag")
	}
}

func TestParseDiagnosticsEmpty(t *testing.T) {
	for _, raw := range []string{"", "[]", "{}", "null", "  "} {
		diags, err := parseDiagnostics(raw)
		if err != nil {
			t.Fatalf("parseDiagnostics(%q): %v", raw, err)
		}
		if len(diags) != 0 {
			t.Errorf("parseDiagnostics(%q): expected 0, got %d", raw, len(diags))
		}
	}
}

func TestParseDiagnosticsInvalid(t *testing.T) {
	if _, err := parseDiagnostics(`{not json}`); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseCompletionItems(t *testing.T) {
	raw := `[{"label":"func main()","kind":3,"detail":"main 函数","insertText":"func main() {}"}]`
	items, err := parseCompletionItems(raw)
	if err != nil {
		t.Fatalf("parseCompletionItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Label != "func main()" {
		t.Errorf("unexpected label: %q", it.Label)
	}
	if it.Kind != protocol.CompletionItemKind(3) {
		t.Errorf("unexpected kind: %v", it.Kind)
	}
	det, _ := it.Detail.Get()
	if det != "main 函数" {
		t.Errorf("unexpected detail: %q", det)
	}
	ins, _ := it.InsertText.Get()
	if ins != "func main() {}" {
		t.Errorf("unexpected insertText: %q", ins)
	}
}

// TestJSDiagnosticProviderRoundtrip 端到端：JS 风格回调 → provider → LSP 协议对象。
func TestJSDiagnosticProviderRoundtrip(t *testing.T) {
	p := NewJSDiagnosticProvider("test", func(uri string) string {
		return `[{"startLine":0,"startChar":0,"endLine":0,"endChar":3,"severity":2,"message":"warn on ` + uri + `"}]`
	})
	if p.Name() != "test" {
		t.Errorf("unexpected name: %s", p.Name())
	}
	diags, err := p.Diagnostics(context.Background(), "/tmp/x.go")
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diag, got %d", len(diags))
	}
	if diags[0].Range.End.Character != 3 {
		t.Errorf("unexpected end char: %d", diags[0].Range.End.Character)
	}
}

// TestJSDiagnosticProviderCallbackError JS 回调抛出错误时的行为。
func TestJSDiagnosticProviderCallbackError(t *testing.T) {
	p := NewJSDiagnosticProvider("boom", func(uri string) string {
		// 模拟 JS 回调返回非法数据
		_ = json.Unmarshal([]byte(`1`), &struct{}{})
		return `[`
	})
	if _, err := p.Diagnostics(context.Background(), "/tmp/x.go"); err == nil {
		t.Error("expected error for invalid JSON from callback")
	}
}
