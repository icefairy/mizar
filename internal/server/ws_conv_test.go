package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestSteerRejectsForeignConversation 运行归属另一个会话时，steer 不应注入
// （否则 A 会话的纠正会串进 B 会话的循环）——这是「多会话串台」的服务端防线。
func TestSteerRejectsForeignConversation(t *testing.T) {
	srv, ts, cookies := newProviderTestServer(t)

	// 模拟当前运行归属于会话 A
	srv.setRunConv("conv-A")
	if srv.RunConv() != "conv-A" {
		t.Fatalf("RunConv=%q want conv-A", srv.RunConv())
	}

	// 从会话 B 发纠正 → 应报告 running=false（前端会改作新任务补发）
	jw := postJSON(t, ts.URL+"/web/api/steer", map[string]any{
		"content": "改方向", "conversation_id": "conv-B",
	}, cookies)
	if jw.code != http.StatusOK {
		t.Fatalf("code=%d body=%s", jw.code, jw.body)
	}
	if !strings.Contains(jw.body, `"running":false`) {
		t.Fatalf("跨会话 steer 应被拒绝注入: %s", jw.body)
	}

	// 归属清空后恢复原行为
	srv.setRunConv("")
	if srv.RunConv() != "" {
		t.Fatal("RunConv 应可清空")
	}
}
