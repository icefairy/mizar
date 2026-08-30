package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild_SimpleProject(t *testing.T) {
	dir := t.TempDir()
	// 创建简单的项目结构
	writeFile(t, dir, "main.go", "package main")
	writeFile(t, dir, "utils.go", "package utils")
	writeFile(t, dir, "README.md", "# Hello")
	mkdir(t, dir, "subdir")
	writeFile(t, dir, "subdir/helper.go", "package subdir")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(rm.Files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(rm.Files))
	}
	if len(rm.Dirs) != 1 {
		t.Fatalf("expected 1 subdir, got %d: %v", len(rm.Dirs), rm.Dirs)
	}

	// 验证 LangMap
	goFiles := rm.LangMap[".go"]
	if len(goFiles) != 3 {
		t.Fatalf("expected 3 .go files in LangMap, got %d", len(goFiles))
	}
}

func TestBuild_SkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "*.log")
	writeFile(t, dir, "main.go", "package main")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Files) != 1 {
		t.Fatalf("expected 1 file (hidden skipped), got %d", len(rm.Files))
	}
}

func TestBuild_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(rm.Files))
	}
	if len(rm.Dirs) != 0 {
		t.Fatalf("expected 0 dirs, got %d", len(rm.Dirs))
	}
}

func TestBuild_FilesSorted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "z.go", "")
	writeFile(t, dir, "a.go", "")
	writeFile(t, dir, "m.go", "")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(rm.Files))
	}
	// 应按字母序排列
	if rm.Files[0].RelPath != "a.go" || rm.Files[1].RelPath != "m.go" || rm.Files[2].RelPath != "z.go" {
		t.Fatalf("files not sorted: %v", []string{rm.Files[0].RelPath, rm.Files[1].RelPath, rm.Files[2].RelPath})
	}
}

func TestRender_Basic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\nfunc main() {}")
	writeFile(t, dir, "README.md", "# Test")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	output := rm.Render(100)
	if !strings.Contains(output, "Repo Map") {
		t.Fatalf("expected 'Repo Map' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "2 个") {
		t.Fatalf("expected '2 个' in output, got:\n%s", output)
	}
}

func TestRender_MaxFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, dir, filepath.Join("dir", string(rune('a'+i))+".go"), "")
	}

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	output := rm.Render(3)
	if !strings.Contains(output, "total files shown") {
		t.Fatalf("expected truncation message, got:\n%s", output)
	}
}

func TestRender_SizeFormatting(t *testing.T) {
	dir := t.TempDir()
	// 创建一个大于 1KB 的文件
	bigContent := strings.Repeat("x", 2048)
	writeFile(t, dir, "big.dat", bigContent)
	// 创建一个小文件
	writeFile(t, dir, "small.txt", "hi")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	output := rm.Render(100)
	if !strings.Contains(output, "2 KB") {
		t.Fatalf("expected '2 KB' in output, got:\n%s", output)
	}
}

func TestLangFromExt(t *testing.T) {
	tests := []struct {
		ext      string
		expected string
	}{
		{".go", "Go"},
		{".py", "Python"},
		{".js", "JavaScript"},
		{".ts", "TypeScript"},
		{".tsx", "TypeScript"},
		{".rs", "Rust"},
		{".java", "Java"},
		{".c", "C/C++"},
		{".cpp", "C/C++"},
		{".rb", "Ruby"},
		{".php", "PHP"},
		{".sh", "Shell"},
		{".md", "Markdown"},
		{".yaml", "YAML"},
		{".yml", "YAML"},
		{".json", "JSON"},
		{".toml", "TOML"},
		{".html", "HTML"},
		{".css", "CSS"},
		{".sql", "SQL"},
		{".xyz", "xyz"},
		{"", ""},
	}
	for _, tt := range tests {
		got := langFromExt(tt.ext)
		if got != tt.expected {
			t.Errorf("langFromExt(%q) = %q, want %q", tt.ext, got, tt.expected)
		}
	}
}

func TestBuild_RelPath(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, dir, "a", "b")
	writeFile(t, dir, "a/b/deep.go", "package deep")

	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(rm.Files))
	}
	if rm.Files[0].RelPath != "a/b/deep.go" {
		t.Fatalf("expected 'a/b/deep.go', got %q", rm.Files[0].RelPath)
	}
	if rm.Files[0].Language != "Go" {
		t.Fatalf("expected language 'Go', got %q", rm.Files[0].Language)
	}
}

// --- helpers ---

func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, dir string, subdirs ...string) {
	t.Helper()
	full := dir
	for _, s := range subdirs {
		full = filepath.Join(full, s)
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
}
