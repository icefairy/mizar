package sessionsearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mizar/internal/agent"
)

// writeSession 写一个模拟会话文件（与 session.Store 的 JSONL 格式一致）。
func writeSession(t *testing.T, dir, scope, id string, msgs []agent.Message) string {
	t.Helper()
	base := dir
	if scope != "" {
		base = filepath.Join(dir, scope)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, m := range msgs {
		b, _ := marshalMsg(m)
		f.Write(append(b, '\n'))
	}
	return path
}

func marshalMsg(m agent.Message) ([]byte, error) {
	return json.Marshal(m)
}

func TestIndexAndSearch(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "search.db")

	writeSession(t, root, "--proj-a--", "s1", []agent.Message{
		{Role: agent.RoleUser, Content: "帮我实现会话全文搜索功能", Timestamp: "2026-11-15T10:00:00+08:00"},
		{Role: agent.RoleAssistant, Content: "好的，我用 SQLite FTS5 建增量索引。", Timestamp: "2026-11-15T10:01:00+08:00"},
		{Role: agent.RoleAssistant, Kind: agent.KindToolCall, ToolName: "bash", ToolArgs: `{"command":"go build"}`, Timestamp: "2026-11-15T10:02:00+08:00"},
		{Role: agent.RoleUser, Kind: agent.KindToolResult, Content: `工具结果: {"tool_name":"bash","output":"巨大输出不入索引"}`, Timestamp: "2026-11-15T10:02:01+08:00"},
	})
	writeSession(t, root, "--proj-b--", "s2", []agent.Message{
		{Role: agent.RoleUser, Content: "另一个会话谈 todo 压缩重注入", Timestamp: "2026-11-14T09:00:00+08:00"},
	})

	ix, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()

	n, err := ix.Sync(root)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// s1: 3 条（user + assistant + toolcall，tool_result 跳过）；s2: 1 条
	if n != 4 {
		t.Errorf("首次索引应 4 条，got %d", n)
	}

	// 中文子串命中
	hits, err := ix.Search("全文搜索", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("应命中「全文搜索」")
	}
	if hits[0].SessionID != "s1" {
		t.Errorf("应命中 s1，got %s", hits[0].SessionID)
	}
	if hits[0].Scope != "--proj-a--" {
		t.Errorf("scope 丢失: %q", hits[0].Scope)
	}

	// 工具调用可搜到
	hits, _ = ix.Search("go build", 10)
	if len(hits) == 0 {
		t.Error("工具调用参数应可搜")
	}

	// 工具结果不入索引
	hits, _ = ix.Search("巨大输出", 10)
	if len(hits) != 0 {
		t.Errorf("工具结果不应入索引，got %d hits", len(hits))
	}

	// 第二个会话
	hits, _ = ix.Search("压缩重注入", 10)
	if len(hits) == 0 || hits[0].SessionID != "s2" {
		t.Errorf("应命中 s2: %+v", hits)
	}
}

func TestIncrementalSync(t *testing.T) {
	root := t.TempDir()
	ix, _ := Open(filepath.Join(t.TempDir(), "s.db"))
	defer ix.Close()

	writeSession(t, root, "", "s1", []agent.Message{
		{Role: agent.RoleUser, Content: "第一条消息 alpha"},
	})
	if n, _ := ix.Sync(root); n != 1 {
		t.Fatalf("首次应 1 条，got %d", n)
	}
	// 再次同步（无变化）应 0
	if n, _ := ix.Sync(root); n != 0 {
		t.Errorf("无变化应 0 条，got %d", n)
	}

	// 追加一条（模拟 session.Store.Appen d）
	path := filepath.Join(root, "s1.jsonl")
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	b, _ := marshalMsg(agent.Message{Role: agent.RoleUser, Content: "第二条消息 beta"})
	f.Write(append(b, '\n'))
	f.Close()

	if n, _ := ix.Sync(root); n != 1 {
		t.Errorf("增量应只 1 条，got %d", n)
	}
	hits, _ := ix.Search("alpha", 5)
	if len(hits) != 1 || hits[0].Seq != 1 {
		t.Errorf("alpha 应命中 seq=1: %+v", hits)
	}
	hits, _ = ix.Search("beta", 5)
	if len(hits) != 1 || hits[0].Seq != 2 {
		t.Errorf("beta 应命中 seq=2: %+v", hits)
	}
}

func TestDeletedSessionCleaned(t *testing.T) {
	root := t.TempDir()
	ix, _ := Open(filepath.Join(t.TempDir(), "s.db"))
	defer ix.Close()

	path := writeSession(t, root, "", "s1", []agent.Message{{Role: agent.RoleUser, Content: "将删除的会话 gamma"}})
	ix.Sync(root)
	if hits, _ := ix.Search("gamma", 5); len(hits) != 1 {
		t.Fatal("索引后应命中")
	}
	os.Remove(path)
	ix.Sync(root)
	if hits, _ := ix.Search("gamma", 5); len(hits) != 0 {
		t.Errorf("会话删除后应从索引清除，got %d", len(hits))
	}
}

func TestSessionsList(t *testing.T) {
	root := t.TempDir()
	ix, _ := Open(filepath.Join(t.TempDir(), "s.db"))
	defer ix.Close()
	writeSession(t, root, "--a--", "s1", []agent.Message{{Role: agent.RoleUser, Content: "x", Timestamp: "2026-11-15T10:00:00+08:00"}})
	writeSession(t, root, "--b--", "s2", []agent.Message{{Role: agent.RoleUser, Content: "y", Timestamp: "2026-11-14T10:00:00+08:00"}})
	ix.Sync(root)
	list, err := ix.Sessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应 2 个会话，got %d", len(list))
	}
	if list[0].SessionID != "s1" {
		t.Errorf("应按时间倒序（s1 更新）: %+v", list)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	ix, _ := Open(filepath.Join(t.TempDir(), "s.db"))
	defer ix.Close()
	if _, err := ix.Search("   ", 5); err == nil {
		t.Error("空查询应报错")
	}
}

func TestIndexableMessageFilters(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`{"role":"user","content":"hello"}`, true},
		{`{"type":"compaction","summary":"x"}`, false},
		{`{"type":"fork","parent":"p"}`, false},
		{`{"role":"user","kind":"tool_result","content":"工具结果: {}"}`, false},
		{`{"role":"user","content":"<!--subagent-state:{}-->"}`, false},
		{`{"role":"assistant","kind":"tool_call","toolName":"read","toolArgs":"{}"}`, true},
	}
	for _, c := range cases {
		if _, ok := indexableMessage(c.line); ok != c.want {
			t.Errorf("indexableMessage(%s) = %v, want %v", c.line, ok, c.want)
		}
	}
}

func TestBuildMatchQuery(t *testing.T) {
	if got := buildMatchQuery("foo bar"); got != `"foo" AND "bar"` {
		t.Errorf("got %q", got)
	}
	if got := buildMatchQuery(`a"b`); got != `"a""b"` {
		t.Errorf("引号应转义: %q", got)
	}
}

func TestSyncNoDir(t *testing.T) {
	ix, _ := Open(filepath.Join(t.TempDir(), "s.db"))
	defer ix.Close()
	if _, err := ix.Sync(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("不存在的目录应报错")
	}
	_ = time.Now
}
