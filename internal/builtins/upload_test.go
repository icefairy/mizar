package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadFileTextAndBase64(t *testing.T) {
	tool := findTool(t, "upload_file")
	dir := filepath.Join(t.TempDir(), "ws")

	out, err := tool.RunInDir(dir, `{"path":"sub/a.txt","content":"hello 上传"}`)
	if err != nil || !strings.Contains(out, "uploaded") {
		t.Fatalf("text upload: %q %v", out, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sub", "a.txt")); err != nil || string(b) != "hello 上传" {
		t.Fatalf("text file content: %v %q", err, string(b))
	}

	// base64 里的空白（空格）应被忽略，解码后是二进制字节
	out, err = tool.RunInDir(dir, `{"path":"bin.dat","content":"AAEC /w==","encoding":"base64"}`)
	if err != nil {
		t.Fatalf("base64 upload: %v (%s)", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bin.dat"))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x01, 0x02, 0xff}
	if len(b) != len(want) {
		t.Fatalf("decoded len=%d want %d (%x)", len(b), len(want), b)
	}
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("decoded=%x want %x", b, want)
		}
	}
}

func TestUploadFileRejectsBadInput(t *testing.T) {
	tool := findTool(t, "upload_file")
	dir := t.TempDir()

	cases := []struct {
		name string
		args string
		want string
	}{
		{"missing path", `{"content":"x"}`, "path"},
		{"escape", `{"path":"../escape.txt","content":"x"}`, "escapes workspace"},
		{"bad base64", `{"path":"x.bin","content":"!!!","encoding":"base64"}`, "invalid base64"},
		{"bad encoding", `{"path":"x.txt","content":"x","encoding":"hex"}`, "unsupported encoding"},
	}
	for _, c := range cases {
		if _, err := tool.RunInDir(dir, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: got %v want contains %q", c.name, err, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape file created: %v", err)
	}
}
