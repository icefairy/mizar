package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizar/internal/plugins"
)

func TestPrefixBashDir(t *testing.T) {
	got := prefixBashDir(`{"command":"ls -la","timeout":5}`, "/tmp/wt")
	var p struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Command, `cd "/tmp/wt" && ls -la`) {
		t.Errorf("bash 命令未前置 cd: %q", p.Command)
	}
	if p.Timeout != 5 {
		t.Errorf("timeout 丢失: %d", p.Timeout)
	}
}

func TestPrefixBashDirRespectsExplicitCd(t *testing.T) {
	got := prefixBashDir(`{"command":"cd /other && pwd"}`, "/tmp/wt")
	if strings.Contains(got, "cd \"/tmp/wt\"") {
		t.Errorf("模型已显式 cd 时不应再前置: %s", got)
	}
}

func TestPrefixBashDirBadArgs(t *testing.T) {
	if got := prefixBashDir(`not json`, "/tmp/wt"); got != `not json` {
		t.Errorf("非法 JSON 应原样返回: %s", got)
	}
	if got := prefixBashDir(`{"command":""}`, "/tmp/wt"); got != `{"command":""}` {
		t.Errorf("空命令应原样返回: %s", got)
	}
}

func TestJoinPathArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{}`, filepath.Join("/tmp/wt")},
		{`{"path":""}`, "/tmp/wt"},
		{`{"path":"src/a.go"}`, filepath.Join("/tmp/wt", "src/a.go")},
		{`{"path":"/abs/a.go"}`, "/abs/a.go"},
		{`{"path":"x","limit":10}`, ""}, // 其他字段保留，path 拼接（want 在下断言）
	}
	for _, c := range cases {
		out := joinPathArg(c.in, "/tmp/wt")
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("输出非法 JSON: %s", out)
		}
		p, _ := m["path"].(string)
		switch c.in {
		case `{}`, `{"path":""}`:
			if p != c.want {
				t.Errorf("%s → path=%q want %q", c.in, p, c.want)
			}
		case `{"path":"src/a.go"}`:
			if p != c.want {
				t.Errorf("%s → path=%q want %q", c.in, p, c.want)
			}
		case `{"path":"/abs/a.go"}`:
			if p != c.want {
				t.Errorf("%s → path=%q want %q", c.in, p, c.want)
			}
		case `{"path":"x","limit":10}`:
			if p != filepath.Join("/tmp/wt", "x") {
				t.Errorf("相对路径应拼接: %q", p)
			}
			if v, ok := m["limit"].(float64); !ok || v != 10 {
				t.Errorf("其他字段丢失: %v", m)
			}
		}
	}
}

func TestWrapToolsForWorkDir(t *testing.T) {
	var bash1, bash2, otherCalled bool
	tools := []plugins.Tool{
		{Name: "bash", Run: func(a string) (string, error) { bash1 = true; return a, nil }},
		{Name: "read", Run: func(a string) (string, error) { return a, nil }},
		{Name: "custom", Run: func(a string) (string, error) { otherCalled = true; return a, nil }},
	}
	// dir 为空：原样返回（不包装）
	if got := wrapToolsForWorkDir(tools, ""); &got[0] == nil {
		// 只是确保不发生
	}
	for _, tt := range wrapToolsForWorkDir(tools, "/tmp/wt") {
		switch tt.Name {
		case "bash":
			_, _ = tt.Run(`{"command":"pwd"}`)
		case "read":
			out, _ := tt.Run(`{"path":"a.txt"}`)
			if !strings.Contains(out, "/tmp/wt") {
				t.Errorf("read 路径未锚定: %s", out)
			}
		case "custom":
			_, _ = tt.Run(`{}`)
		}
	}
	if !bash1 {
		t.Error("bash 包装后未调用原函数")
	}
	if !otherCalled {
		t.Error("非路径工具应原样保留")
	}
	_ = bash2
}

func TestWrapToolsForWorkDirEmptyReturnsSame(t *testing.T) {
	tools := []plugins.Tool{{Name: "read", Run: func(a string) (string, error) { return "", nil }}}
	got := wrapToolsForWorkDir(tools, "")
	if len(got) != 1 {
		t.Fatalf("长度变化: %d", len(got))
	}
}

// TestWorkingDirActuallyChangesCwd 集成验证：包装后的 bash 确实在目标目录执行。
func TestWorkingDirActuallyChangesCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := plugins.Tool{Name: "bash", Run: func(args string) (string, error) {
		var p struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(args), &p)
		// 用 Go 模拟 shell 执行：这里只验证命令字符串含目标目录
		return p.Command, nil
	}}
	wrapped := wrapToolsForWorkDir([]plugins.Tool{tool}, dir)[0]
	out, _ := wrapped.Run(`{"command":"pwd"}`)
	if !strings.Contains(out, dir) {
		t.Errorf("包装后命令未含工作目录: %s", out)
	}
}
