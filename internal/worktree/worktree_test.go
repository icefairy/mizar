package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupRepo 建一个临时 git 仓库（跳过无 git 环境）。
func setupRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用，跳过 worktree 测试")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// 隔离全局配置，避免用户 gitconfig 干扰（如默认分支名、签名）
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestRoot(t *testing.T) {
	dir := setupRepo(t)
	root, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 解析软链接后比较（macOS /tmp 是符号链接）
	a, _ := filepath.EvalSymlinks(dir)
	b, _ := filepath.EvalSymlinks(root)
	if a != b {
		t.Errorf("Root=%q want %q", b, a)
	}
}

func TestRootNotGit(t *testing.T) {
	dir := t.TempDir()
	if _, err := Root(dir); err == nil {
		t.Error("非 git 目录应报错")
	}
}

func TestCreateListRemove(t *testing.T) {
	repo := setupRepo(t)
	target, err := Create(repo, CreateOptions{Branch: "exp1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("worktree 目录未创建: %s: %v", target, err)
	}

	list, err := List(repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, w := range list {
		if filepath.Clean(w.Path) == filepath.Clean(target) && w.Branch == "exp1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("List 未找到新建 worktree: %+v", list)
	}

	// 重复创建应失败
	if _, err := Create(repo, CreateOptions{Branch: "exp1"}); err == nil {
		t.Error("重复 branch 应报错")
	}

	// 删除（force 丢弃）
	if err := Remove(repo, target, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("worktree 目录应已删除: %v", err)
	}
}

func TestCreateCustomPath(t *testing.T) {
	repo := setupRepo(t)
	rel := "wt-custom"
	target, err := Create(repo, CreateOptions{Branch: "exp2", Path: rel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.Clean(target), rel) {
		t.Errorf("自定义 path 未生效: %s", target)
	}
	defer Remove(repo, target, true)
}

func TestCreateBadBranch(t *testing.T) {
	repo := setupRepo(t)
	if _, err := Create(repo, CreateOptions{Branch: "a b"}); err == nil {
		t.Error("branch 含空格应报错")
	}
	if _, err := Create(repo, CreateOptions{Branch: ""}); err == nil {
		t.Error("空 branch 应报错")
	}
}

func TestFind(t *testing.T) {
	repo := setupRepo(t)
	target, err := Create(repo, CreateOptions{Branch: "expfind"})
	if err != nil {
		t.Fatal(err)
	}
	defer Remove(repo, target, true)

	if _, ok := Find(repo, target); !ok {
		t.Error("精确路径 Find 失败")
	}
	if _, ok := Find(repo, "expfind"); !ok {
		t.Error("按目录名 Find 失败")
	}
	if _, ok := Find(repo, "no-such"); ok {
		t.Error("不存在的路径不应找到")
	}
}

func TestParsePorcelain(t *testing.T) {
	out := `worktree /a/repo
HEAD abc123
branch refs/heads/main

worktree /a/wt
HEAD def456
branch refs/heads/exp

worktree /a/locked
HEAD 000
detached
locked reason

`
	got := parsePorcelain(out)
	if len(got) != 3 {
		t.Fatalf("应解析 3 个 worktree，got %d: %+v", len(got), got)
	}
	if got[0].Branch != "main" || got[1].Branch != "exp" {
		t.Errorf("branch 解析错误: %+v", got)
	}
	if !got[2].Locked {
		t.Errorf("locked 未识别: %+v", got[2])
	}
}

func TestRemoveEmptyPath(t *testing.T) {
	repo := setupRepo(t)
	if err := Remove(repo, "", true); err == nil {
		t.Error("空 path 应报错")
	}
}
