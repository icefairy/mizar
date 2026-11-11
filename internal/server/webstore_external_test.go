package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWebstoreExternalModificationReloaded 验证外部修改文件后，
// 内存不再用旧数据覆盖外部修改（自动重载 + 无改动不回写）。
func TestWebstoreExternalModificationReloaded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "webui.json")
	st := newWebStore(p)
	st.create("chat")
	st.create("chat")
	if got := len(st.list()); got != 2 {
		t.Fatalf("want 2 convs, got %d", got)
	}

	// 模拟外部进程改写文件：只留 1 个会话
	type fileShape struct {
		Convs []*conversation `json:"conversations"`
		Set   *webSettings    `json:"settings"`
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var fs fileShape
	if err := json.Unmarshal(data, &fs); err != nil {
		t.Fatal(err)
	}
	fs.Convs = fs.Convs[:1]
	out, _ := json.MarshalIndent(fs, "", "  ")
	// 保证 mtime 变化
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}

	// 列表应看到外部修改后的 1 条
	if got := len(st.list()); got != 1 {
		t.Fatalf("after external edit, want 1 conv, got %d", got)
	}

	// 无改动操作不应回写（磁盘仍是 1 条）
	_ = st.get("nonexistent")
	d2, _ := os.ReadFile(p)
	var fs2 fileShape
	_ = json.Unmarshal(d2, &fs2)
	if len(fs2.Convs) != 1 {
		t.Fatalf("stale memory overwrote file: got %d convs", len(fs2.Convs))
	}
}
