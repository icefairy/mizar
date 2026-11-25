package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"mizar/internal/agent"
	"mizar/internal/session"
)

// newWorkspaceTestServer 构造一个工作空间根目录在临时目录下的 Server。
func newWorkspaceTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	a := agent.New(&mockLLM{}, testPluginManager(t))
	cfg := NewConfig("127.0.0.1:0", "tok")
	cfg.SetAll(true, true, true)
	srv := New(cfg, a, session.New(t.TempDir()))
	root := filepath.Join(t.TempDir(), "ws")
	srv.web.store.saveSettings(webSettings{WorkspaceRoot: root})
	return srv, root
}

func multipartBody(t *testing.T, convID string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if convID != "" {
		_ = w.WriteField("conversation_id", convID)
	}
	for name, body := range files {
		fw, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, _ = fw.Write([]byte(body))
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func uploadReq(t *testing.T, convID, target string, files map[string]string) *http.Request {
	t.Helper()
	body, ct := multipartBody(t, "", files)
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", ct)
	return req
}

func TestWorkspaceUploadListDownloadDelete(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	conv := srv.web.store.create("agent")
	dir := filepath.Join(root, conv.ID)

	// 上传
	rec := httptest.NewRecorder()
	srv.handleWorkspaceUpload(rec, uploadReq(t, conv.ID, "/web/api/workspace/upload?conversation_id="+conv.ID,
		map[string]string{"报告.txt": "hello 开阳"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload code=%d body=%s", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "报告.txt")); err != nil || string(b) != "hello 开阳" {
		t.Fatalf("file not saved correctly: %v %q", err, string(b))
	}
	// 落盘一条提示消息给模型
	c := srv.web.store.get(conv.ID)
	if len(c.Messages) != 1 || c.Messages[0].Role != "user" {
		t.Fatalf("expected upload notice message, got %+v", c.Messages)
	}

	// 列表
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/api/workspace/files?conversation_id="+conv.ID, nil)
	srv.handleWorkspaceFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list code=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if len(listed.Files) != 1 || listed.Files[0].Name != "报告.txt" || listed.Files[0].Size == 0 {
		t.Fatalf("unexpected list: %+v", listed.Files)
	}

	// 下载
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/web/api/workspace/download?conversation_id="+conv.ID+"&name="+urlPathEscape("报告.txt"), nil)
	srv.handleWorkspaceDownload(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello 开阳" {
		t.Fatalf("download code=%d body=%q", rec.Code, rec.Body.String())
	}

	// 删除单文件
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/web/api/workspace/file?conversation_id="+conv.ID+"&name="+urlPathEscape("报告.txt"), nil)
	srv.handleWorkspaceDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "报告.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: %v", err)
	}
}

func TestWorkspaceRejectsTraversalAndSymlink(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	conv := srv.web.store.create("agent")
	dir := filepath.Join(root, conv.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// ".." 文件名 → 400
	rec := httptest.NewRecorder()
	srv.handleWorkspaceUpload(rec, uploadReq(t, conv.ID, "/web/api/workspace/upload?conversation_id="+conv.ID,
		map[string]string{"..": "x"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("dotdot name: code=%d want 400 body=%s", rec.Code, rec.Body.String())
	}

	// 带路径的文件名被取基名，不能写到目录外
	outside := filepath.Join(root, "escaped.txt")
	rec = httptest.NewRecorder()
	srv.handleWorkspaceUpload(rec, uploadReq(t, conv.ID, "/web/api/workspace/upload?conversation_id="+conv.ID,
		map[string]string{"../../escaped.txt": "bad"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("path name upload: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("file escaped workspace: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "escaped.txt")); err != nil || string(b) != "bad" {
		t.Fatalf("sanitized file missing: %v %q", err, string(b))
	}

	// 软链目标：拒绝覆盖，且不跟随写到目录外
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.handleWorkspaceUpload(rec, uploadReq(t, conv.ID, "/web/api/workspace/upload?conversation_id="+conv.ID,
		map[string]string{"link.txt": "pwned"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("symlink upload: code=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("symlink target was overwritten: %q", string(b))
	}
}

func TestWorkspaceDeletedWithConversation(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	conv := srv.web.store.create("agent")
	dir := filepath.Join(root, conv.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/web/api/conversations?id="+conv.ID, nil)
	srv.handleConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete conv code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("workspace dir not removed: %v", err)
	}
}

func TestWorkspaceRootValidation(t *testing.T) {
	if _, err := validateWorkspaceRoot("relative/path"); err == nil {
		t.Fatal("relative path should be rejected")
	}
	if _, err := validateWorkspaceRoot("/"); err == nil {
		t.Fatal("fs root should be rejected")
	}
	if _, err := validateWorkspaceRoot("/etc/mizar-test"); err == nil {
		t.Fatal("/etc should be rejected")
	}
	dir := filepath.Join(t.TempDir(), "ok")
	got, err := validateWorkspaceRoot(dir)
	if err != nil || got != dir {
		t.Fatalf("valid path: got=%q err=%v", got, err)
	}
}

func TestAgentForSetsWorkspaceWorkDir(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	conv := srv.web.store.create("agent")
	a := srv.agentFor(conv.ID)
	want := filepath.Join(root, conv.ID)
	if a.WorkDir != want {
		t.Fatalf("WorkDir=%q want %q", a.WorkDir, want)
	}
	// 非 Web 入口（空 convID）不应被改成工作空间目录
	if srv.agentFor("").WorkDir == want {
		t.Fatal("non-web agent should not get workspace WorkDir")
	}
}

// TestRemoveConvWorkspaceRejectsInvalidID 清理只接受合法会话 id：
// 路径穿越/带分隔符的 id 必须被拒，且不能误删根目录或根目录外的内容。
func TestRemoveConvWorkspaceRejectsInvalidID(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "marker.txt")
	if err := os.WriteFile(marker, []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "keep-me")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", ".", "..", "../keep-me", "a/b", "c\\d", "a b"} {
		srv.removeConvWorkspace(bad)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("workspace root content was removed by invalid conv id: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside dir was removed: %v", err)
	}
}

// TestDeleteConversationOnlyRemovesOwnWorkspace 删除一个会话不能影响另一个会话的文件。
func TestDeleteConversationOnlyRemovesOwnWorkspace(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	a := srv.web.store.create("agent")
	b := srv.web.store.create("agent")
	for _, c := range []*conversation{a, b} {
		dir := filepath.Join(root, c.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(c.ID), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/web/api/conversations?id="+a.ID, nil)
	srv.handleConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete conv code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted conv workspace still exists: %v", err)
	}
	bFile := filepath.Join(root, b.ID, "f.txt")
	if got, err := os.ReadFile(bFile); err != nil || string(got) != b.ID {
		t.Fatalf("other conv workspace damaged: %v %q", err, string(got))
	}
}

// TestDeleteConversationWithoutWorkspaceIsNoop 从没上传过文件的会话删除时不能报错。
func TestDeleteConversationWithoutWorkspaceIsNoop(t *testing.T) {
	srv, _ := newWorkspaceTestServer(t)
	conv := srv.web.store.create("chat")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/web/api/conversations?id="+conv.ID, nil)
	srv.handleConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete conv without workspace: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestUploadLeavesNoTempFiles 成功上传后不应残留 .upload-* 临时文件。
func TestUploadLeavesNoTempFiles(t *testing.T) {
	srv, root := newWorkspaceTestServer(t)
	conv := srv.web.store.create("agent")
	rec := httptest.NewRecorder()
	srv.handleWorkspaceUpload(rec, uploadReq(t, conv.ID, "/web/api/workspace/upload?conversation_id="+conv.ID,
		map[string]string{"ok.txt": "data"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload code=%d body=%s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, conv.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "ok.txt" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

// TestWriteUploadPartCleansUpOnError 读文件流失败时不能留下半成品/临时文件。
func TestWriteUploadPartCleansUpOnError(t *testing.T) {
	dir := t.TempDir()
	_, err := writeUploadPart(dir, filepath.Join(dir, "x.txt"), iotest.ErrReader(errors.New("boom")))
	if err == nil {
		t.Fatal("expected error from broken reader")
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("temp/partial files left behind: %+v", entries)
	}
}
