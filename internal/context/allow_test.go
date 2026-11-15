package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContextFilesAllowing(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("PROJECT-INSTRUCTION"), 0o644)
	os.WriteFile(filepath.Join(sub, ".cursorrules"), []byte("CURSOR-RULE"), 0o644)

	// allow 全 false：项目文件被跳过
	out := LoadContextFilesAllowing(sub, func(string) bool { return false })
	if strings.Contains(out, "PROJECT-INSTRUCTION") || strings.Contains(out, "CURSOR-RULE") {
		t.Errorf("未放行的项目文件不应注入:\n%s", out)
	}

	// allow 全 true：项目文件注入
	out = LoadContextFilesAllowing(sub, func(string) bool { return true })
	if !strings.Contains(out, "PROJECT-INSTRUCTION") {
		t.Errorf("放行后应注入项目 AGENTS.md:\n%s", out)
	}
	if !strings.Contains(out, "CURSOR-RULE") {
		t.Errorf("放行后应注入 .cursorrules:\n%s", out)
	}

	// nil allow：等同 LoadContextFiles
	out = LoadContextFilesAllowing(sub, nil)
	if !strings.Contains(out, "PROJECT-INSTRUCTION") {
		t.Errorf("nil allow 应等同全放行:\n%s", out)
	}

	// 选择性放行：只放 AGENTS.md
	out = LoadContextFilesAllowing(sub, func(p string) bool {
		return filepath.Base(p) == "AGENTS.md"
	})
	if !strings.Contains(out, "PROJECT-INSTRUCTION") {
		t.Error("应注入 AGENTS.md")
	}
	if strings.Contains(out, "CURSOR-RULE") {
		t.Errorf(".cursorrules 应被跳过:\n%s", out)
	}
}
