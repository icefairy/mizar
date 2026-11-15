package userhooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// C-6 用户级 hooks：pre_tool_use 裁决（deny/rewrite/exit2/失败放行）与 post_tool_use 观察。

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func noopLogf(format string, args ...any) {}

func TestPreToolDenyByJSON(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "deny.sh", `echo '{"decision":"deny","reason":"禁止 rm"}'`)
	c := &Config{PreToolUse: []UserHook{{Command: script}}}
	r := c.PreToolDecision(noopLogf)("bash", `{"command":"rm -rf /"}`, "sess-1")
	if r.Decision != DecisionDeny || r.Reason != "禁止 rm" {
		t.Fatalf("deny 裁决错误: %+v", r)
	}
}

func TestPreToolDenyByExit2(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "exit2.sh", `echo " hook 拒绝了" >&2; exit 2`)
	c := &Config{PreToolUse: []UserHook{{Command: script}}}
	r := c.PreToolDecision(noopLogf)("bash", "{}", "s")
	if r.Decision != DecisionDeny || r.Reason != "hook 拒绝了" {
		t.Fatalf("exit2 裁决错误: %+v", r)
	}
	// stderr 为空时给默认原因
	script2 := writeScript(t, dir, "exit2b.sh", `exit 2`)
	c2 := &Config{PreToolUse: []UserHook{{Command: script2}}}
	r2 := c2.PreToolDecision(noopLogf)("bash", "{}", "s")
	if r2.Decision != DecisionDeny || !strings.Contains(r2.Reason, "exit 2") {
		t.Fatalf("exit2 默认原因错误: %+v", r2)
	}
}

func TestPreToolRewrite(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "rw.sh", `echo '{"decision":"rewrite","tool":"echo","args":"{\"safe\":true}"}'`)
	c := &Config{PreToolUse: []UserHook{{Command: script}}}
	r := c.PreToolDecision(noopLogf)("bash", `{"command":"danger"}`, "s")
	if r.Decision != DecisionRewrite || r.NewTool != "echo" || r.NewArgs != `{"safe":true}` {
		t.Fatalf("rewrite 裁决错误: %+v", r)
	}
}

func TestPreToolAllowAndFailOpen(t *testing.T) {
	dir := t.TempDir()
	// exit 0 无输出 = 放行
	allow := writeScript(t, dir, "allow.sh", `exit 0`)
	// 非零 exit（非 2）= 故障放行
	failing := writeScript(t, dir, "fail.sh", `echo oops >&2; exit 1`)
	// 超时 = 故障放行
	slow := writeScript(t, dir, "slow.sh", `sleep 5`)
	// stdin 上下文可被 hook 读到（验证喂参通路；写死路径，不依赖 $TMPDIR）
	ctxFile := filepath.Join(dir, "ctx.json")
	echoCtx := writeScript(t, dir, "ctx.sh", "cat > "+ctxFile+"; exit 0")

	c := &Config{PreToolUse: []UserHook{
		{Command: allow}, {Command: failing}, {Command: slow, TimeoutMs: 300}, {Command: echoCtx},
	}}
	r := c.PreToolDecision(noopLogf)("bash", `{"command":"ls"}`, "s1")
	if r.Decision != DecisionAllow {
		t.Fatalf("全部非阻断时最终应放行: %+v", r)
	}
	// 验证 stdin JSON 上下文（最后一条 hook 收到的应是原始参数）
	b, err := os.ReadFile(ctxFile)
	if err != nil {
		t.Fatal(err)
	}
	var ctx map[string]string
	if err := json.Unmarshal(b, &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx["tool"] != "bash" || ctx["session"] != "s1" || ctx["hook"] != "pre_tool_use" || ctx["cwd"] == "" {
		t.Fatalf("stdin 上下文缺失字段: %+v", ctx)
	}
	_ = os.Remove(ctxFile)
}

func TestPreToolRewriteChain(t *testing.T) {
	dir := t.TempDir()
	// 第 1 条把 tool 改成 echo；第 2 条看到改写后的 tool，再把 args 换掉
	s1 := writeScript(t, dir, "rw1.sh", `echo '{"decision":"rewrite","tool":"echo","args":"{}"}'`)
	s2 := writeScript(t, dir, "rw2.sh", `if grep -q '"tool":"echo"' >/dev/null; then echo '{"decision":"rewrite","args":"{\"v\":2}"}'; else exit 3; fi`)
	c := &Config{PreToolUse: []UserHook{{Command: s1}, {Command: s2}}}
	r := c.PreToolDecision(noopLogf)("bash", "{}", "s")
	if r.Decision != DecisionRewrite || r.NewTool != "echo" || r.NewArgs != `{"v":2}` {
		t.Fatalf("rewrite 链错误: %+v", r)
	}
}

func TestPostToolObserve(t *testing.T) {
	dir := t.TempDir()
	ctxFile := filepath.Join(dir, "post.json")
	script := writeScript(t, dir, "post.sh", "cat > "+ctxFile+"; exit 0")
	c := &Config{PostToolUse: []UserHook{{Command: script}}}
	fn := c.PostToolObserve(noopLogf)
	if fn == nil {
		t.Fatal("已配置 post_tool_use 应返回回调")
	}
	fn("bash", `{"command":"ls"}`, "工具输出内容", nil, "s1")
	b, err := os.ReadFile(ctxFile)
	if err != nil {
		t.Fatal(err)
	}
	var ctx map[string]string
	if err := json.Unmarshal(b, &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx["output"] != "工具输出内容" || ctx["tool"] != "bash" || ctx["error"] != "" {
		t.Fatalf("post 上下文错误: %+v", ctx)
	}
	// 未配置返回 nil
	if (&Config{}).PostToolObserve(noopLogf) != nil {
		t.Fatal("未配置应返回 nil")
	}
	// 失败的 post hook 不影响调用方（不 panic、不返回错误）
	c2 := &Config{PostToolUse: []UserHook{{Command: "nonexistent-cmd-xyz"}, {Command: "false"}}}
	c2.PostToolObserve(noopLogf)("bash", "{}", "out", nil, "s")
}

func TestEmpty(t *testing.T) {
	if !(*Config)(nil).Empty() || !(&Config{}).Empty() {
		t.Fatal("nil/空配置应 Empty")
	}
	c := &Config{PreToolUse: []UserHook{{Command: "x"}}}
	if c.Empty() {
		t.Fatal("有配置不应 Empty")
	}
}
