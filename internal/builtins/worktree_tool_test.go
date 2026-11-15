package builtins

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupWTRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestWorktreeToolsE2E(t *testing.T) {
	repo := setupWTRepo(t)
	tools := WorktreeTools(repo)
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
	}
	var create, leave, list = tools[0], tools[1], tools[2]

	out, err := create.Run(`{"branch":"exp-tool"}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "exp-tool") {
		t.Errorf("create 输出: %s", out)
	}

	out, err = list.Run(`{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "exp-tool") {
		t.Errorf("list 输出: %s", out)
	}

	out, err = leave.Run(`{"path":"exp-tool","force":true}`)
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if !strings.Contains(out, "已删除") {
		t.Errorf("leave 输出: %s", out)
	}

	// list 应不再含 exp-tool
	out, _ = list.Run(`{}`)
	if strings.Contains(out, "exp-tool") {
		t.Errorf("leave 后仍存在: %s", out)
	}
}

func TestWorktreeToolsNilDir(t *testing.T) {
	if tools := WorktreeTools(""); tools != nil {
		t.Error("空 repoDir 应返回 nil")
	}
}

func TestWorktreeCreateBadArgs(t *testing.T) {
	repo := setupWTRepo(t)
	create := WorktreeTools(repo)[0]
	if _, err := create.Run(`{"branch":""}`); err == nil {
		t.Error("空 branch 应报错")
	}
	if _, err := create.Run(`not json`); err == nil {
		t.Error("非法 JSON 应报错")
	}
}

var _ = json.Marshal
