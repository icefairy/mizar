package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 工具层统一兜底：任何畸形输入下，工具必须返回 (输出, error)，不能 panic。
// 这里直接跑内置工具（不经 plugins.Manager），验证工具自身不崩。
func TestBuiltinToolsNeverPanic(t *testing.T) {
	dir := t.TempDir()
	binFile := filepath.Join(dir, "binary.bin")
	if err := os.WriteFile(binFile, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	badReadme := filepath.Join(dir, "broken.pdf")
	os.WriteFile(badReadme, []byte("%PDF-1.7\n garbage"), 0o644)

	emptyRead := filepath.Join(dir, "empty.txt")
	os.WriteFile(emptyRead, nil, 0o644)

	cases := []struct {
		tool string
		args string
	}{
		{"read", `{"path":"` + badReadme + `"}`},
		{"read", `{"path":"` + binFile + `"}`},
		{"read", `{"path":"` + emptyRead + `"}`},
		{"read", `{"path":"/nonexistent/x.txt"}`},
		{"read", `{}`},
		{"read", `not-json`},
		{"ls", `{"path":"/nonexistent/dir"}`},
		{"ls", `{}`},
		{"find", `{"pattern":"["}`},                // 非法 glob
		{"grep", `{"pattern":"("}`},                // 非法正则
		{"grep", `{"pattern":"x","path":"/nope"}`}, // 不存在目录
		{"bash", `{"command":"exit 3"}`},
		{"bash", `{"command":""}`},
		{"bash", `{"command":"echo hi","timeout":1}`},
		{"edit", `{"path":"/nonexistent/f.txt","oldText":"a","newText":"b"}`},
		{"edit", `{"path":"` + emptyRead + `","oldText":"","newText":"b"}`},
		{"repo_map", `{"path":"/nonexistent"}`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.tool+"|"+c.args[:min(len(c.args), 24)], func(t *testing.T) {
			ch := make(chan struct{}, 1)
			var pan interface{}
			go func() {
				defer func() {
					if r := recover(); r != nil {
						pan = r
					}
					ch <- struct{}{}
				}()
				_, _ = call(t, c.tool, c.args)
			}()
			select {
			case <-ch:
				if pan != nil {
					t.Fatalf("工具 %s panic: %v（在 Manager.Call 里会被兜住，但工具自身也应健壮）", c.tool, pan)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("工具 %s 卡死", c.tool)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = strings.Contains
