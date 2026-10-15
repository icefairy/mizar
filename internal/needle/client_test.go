package needle

import (
	"context"
	"testing"
	"time"
)

func TestClient_New(t *testing.T) {
	c := New("http://localhost:8765")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.baseURL != "http://localhost:8765" {
		t.Errorf("expected baseURL=http://localhost:8765, got %s", c.baseURL)
	}
}

func TestClient_Health(t *testing.T) {
	// 需要先启动 Needle 2 server
	// 这里只测试客户端创建
	c := New("http://localhost:8765")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 服务器未启动，应该返回 false
	healthy, err := c.Health(ctx)
	if err == nil {
		t.Error("expected error when server is not running")
	}
	if healthy {
		t.Error("expected unhealthy when server is not running")
	}
}
