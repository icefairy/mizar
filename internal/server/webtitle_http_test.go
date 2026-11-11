package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// doJSON 发一个任意方法的 JSON 请求（postJSON 只发 POST，PATCH 测试需要）。
func doJSON(t *testing.T, method, url string, payload map[string]any, cookies []*http.Cookie) resp {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return resp{code: r.StatusCode, body: string(body), cookies: r.Cookies()}
}

// TestWebStatusRunningConv 状态接口应返回 running_conv 字段（前端据此区分「哪个会话在跑」）。
func TestWebStatusRunningConv(t *testing.T) {
	srv, ts, cookies := newProviderTestServer(t)
	srv.setRunConv("conv-X")
	defer srv.setRunConv("")

	req, _ := http.NewRequest("GET", ts.URL+"/web/api/status", nil)
	for _, x := range cookies {
		req.AddCookie(x)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, r)
	if !strings.Contains(body, `"running_conv":"conv-X"`) {
		t.Fatalf("status 应带 running_conv=conv-X, got %s", body)
	}
}

// TestWebPatchMarksTitleManual 手动改名后应写 title_manual=true，自动标题不再覆盖。
func TestWebPatchMarksTitleManual(t *testing.T) {
	srv, ts, cookies := newProviderTestServer(t)
	c := srv.web.store.create("chat")

	jw := doJSON(t, http.MethodPatch, ts.URL+"/web/api/conversation?id="+c.ID, map[string]any{"title": "我的手写标题"}, cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("patch: %d %s", jw.code, jw.body)
	}
	got := srv.web.store.get(c.ID)
	if got.Title != "我的手写标题" {
		t.Fatalf("Title=%q", got.Title)
	}
	if !got.TitleManual {
		t.Fatal("PATCH 后 TitleManual 应为 true")
	}
	// 自动标题不得覆盖
	if srv.web.store.setAutoTitle(c.ID, "自动标题", 8) {
		t.Fatal("手动标题后 setAutoTitle 应被拒绝")
	}
	if got := srv.web.store.get(c.ID); got.Title != "我的手写标题" {
		t.Fatalf("手动标题被覆盖为 %q", got.Title)
	}
}
