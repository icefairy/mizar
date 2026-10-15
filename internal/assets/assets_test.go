package assets

import (
	"os"
	"path/filepath"
	"testing"
)

// 释放后：文件应存在，且重复释放不覆盖用户改动。
func TestReleaseNoOverwrite(t *testing.T) {
	ext := t.TempDir()
	sk := t.TempDir()
	r1 := Release(ext, sk)
	if len(r1.Written) == 0 {
		t.Fatal("首次释放应写入文件")
	}
	// db.ts 应存在
	if _, err := os.Stat(filepath.Join(ext, "db.ts")); err != nil {
		t.Fatalf("db.ts 未释放: %v", err)
	}
	// 子目录技能
	if _, err := os.Stat(filepath.Join(sk, "handoff", "SKILL.md")); err != nil {
		t.Fatalf("handoff/SKILL.md 未释放: %v", err)
	}
	// 改动一个文件，再次释放不应覆盖
	target := filepath.Join(ext, "db.ts")
	os.WriteFile(target, []byte("// USER EDIT"), 0o644)
	r2 := Release(ext, sk)
	if len(r2.Written) != 0 {
		t.Fatalf("重复释放不应写入（应全部跳过），实际写入 %v", r2.Written)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "// USER EDIT" {
		t.Fatal("用户改动被覆盖了")
	}
	// 删除一个文件，再次释放应补回
	os.Remove(filepath.Join(ext, "db.ts"))
	r3 := Release(ext, sk)
	if len(r3.Written) == 0 {
		t.Fatal("缺失文件应被补回")
	}
}
