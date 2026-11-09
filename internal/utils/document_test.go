package utils

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractPDFNoPanic 验证畸形 PDF 只返回错误、不 panic。
// rsc.io/pdf 在遇到缺少 stream / 非法 xref 等结构时会在内部 panic，
// ExtractPDF 必须 recover 成普通 error。
func TestExtractPDFNoPanic(t *testing.T) {
	cases := map[string][]byte{
		"empty":      {},
		"garbage":    []byte("hello world, definitely not a pdf"),
		"truncated":  []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n"),
		"headeronly": []byte("%PDF-1.4\n%%EOF\n"),
		"badxref":    []byte("%PDF-1.5\nstartxref\n999999\n%%EOF\n"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), name+".pdf")
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ExtractPDF panicked: %v", r)
				}
			}()
			text, err := ExtractPDF(p)
			if err != nil {
				t.Logf("returned error (expected): %v", err)
			} else {
				t.Logf("returned text: %q", text)
			}
		})
	}
}

// TestExtractPDFMissingFile 文件不存在时返回错误而不是 panic。
func TestExtractPDFMissingFile(t *testing.T) {
	if _, err := ExtractPDF(filepath.Join(t.TempDir(), "nope.pdf")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
