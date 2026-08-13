package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillManage(t *testing.T) {
	dir := t.TempDir()

	call := func(args string) (string, error) {
		return skillManage(dir).Run(args)
	}

	// create（内容含重复片段用于歧义测试）
	out, err := call(`{"operation":"create","name":"disk-watch","description":"监控磁盘空间","content":"检查 df -h 检查"}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "创建") {
		t.Fatalf("create output: %s", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "disk-watch", "SKILL.md"))
	if !strings.Contains(string(b), "监控磁盘空间") {
		t.Fatalf("SKILL.md missing desc: %s", b)
	}

	// list
	out, err = call(`{"operation":"list"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "disk-watch") {
		t.Fatalf("list output: %s", out)
	}

	// patch 唯一匹配（"df -h" 只出现一次）
	out, err = call(`{"operation":"patch","name":"disk-watch","find":"df -h","replace":"df -h --total"}`)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	// patch 歧义拒绝（"检查" 出现两次）
	_, err = call(`{"operation":"patch","name":"disk-watch","find":"检查","replace":"X"}`)
	if err == nil || !strings.Contains(err.Error(), "歧义") {
		t.Fatalf("patch should reject ambiguous, got %v", err)
	}

	// inspect
	out, err = call(`{"operation":"inspect","name":"disk-watch"}`)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !strings.Contains(out, "df -h --total") {
		t.Fatalf("inspect output: %s", out)
	}

	// delete
	out, err = call(`{"operation":"delete","name":"disk-watch"}`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disk-watch")); !os.IsNotExist(err) {
		t.Fatalf("skill dir should be gone: %v", err)
	}

	// 非法名
	_, err = call(`{"operation":"create","name":"Bad_Name","description":"x"}`)
	if err == nil {
		t.Fatal("invalid name should fail")
	}
}

func TestSkillManageNotTouchPlugins(t *testing.T) {
	// skill_manage 只写 skills 目录，绝不写插件目录
	dir := t.TempDir()
	plugDir := filepath.Join(dir, "..", "extensions-"+t.Name())
	_ = plugDir // 仅验证 skillsDir 参数内操作
	out, err := skillManage(dir).Run(`{"operation":"create","name":"test-skill","description":"x","content":"y"}`)
	if err != nil || !strings.Contains(out, "创建") {
		t.Fatalf("create: %v %v", out, err)
	}
	// 没有写出 dir 外
	if _, err := os.Stat(filepath.Join(dir, "..", "test-skill")); !os.IsNotExist(err) {
		t.Fatal("skill leaked outside skills dir")
	}
}
