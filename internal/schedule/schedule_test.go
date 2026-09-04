package schedule

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateAfter(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	id, err := reg.Create("after", "提醒我开会", 60, time.Time{}, 0)
	if err != nil {
		t.Fatalf("create after: %v", err)
	}
	if id == "" {
		t.Fatal("id should not be empty")
	}
	rs := reg.List()
	if len(rs) != 1 {
		t.Fatalf("want 1 reminder, got %d", len(rs))
	}
	if rs[0].Kind != "after" {
		t.Fatalf("kind want after, got %s", rs[0].Kind)
	}
	if !strings.Contains(rs[0].Prompt, "开会") {
		t.Fatalf("prompt mismatch: %s", rs[0].Prompt)
	}
}

func TestCreateAt(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	future := time.Now().Add(2 * time.Hour)
	id, err := reg.Create("at", "到期检查", 0, future, 0)
	if err != nil {
		t.Fatalf("create at: %v", err)
	}
	if id == "" {
		t.Fatal("id should not be empty")
	}
}

func TestCreateEveryTooShort(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	_, err := reg.Create("every", "每小时", 0, time.Time{}, 60)
	if err == nil {
		t.Fatal("every < 300s should fail")
	}
}

func TestDelete(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	id, _ := reg.Create("after", "测试提醒", 300, time.Time{}, 0)
	if err := reg.Delete(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatal("should be empty after delete")
	}
}

func TestDeleteNotFound(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	if err := reg.Delete("nonexistent"); err == nil {
		t.Fatal("delete unknown should fail")
	}
}

func TestRenderListEmpty(t *testing.T) {
	reg := NewRegistry(nil)
	defer reg.Stop()
	out := RenderList([]Reminder{})
	if !strings.Contains(out, "无") {
		t.Fatalf("empty list should show placeholder: %q", out)
	}
}

func TestDelivery(t *testing.T) {
	var delivered []string
	var mu sync.Mutex
	reg := NewRegistry(func(id, prompt string) {
		mu.Lock()
		delivered = append(delivered, id)
		mu.Unlock()
	})
	defer reg.Stop()
	// 创建一条立即到期的提醒（用过去时间）
	future := time.Now().Add(1 * time.Millisecond)
	_, err := reg.Create("at", "立即提醒", 0, future, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 等待 ticker 触发（最多 15 秒）
	deadline := time.After(15 * time.Second)
outer:
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for delivery")
		case <-time.After(500 * time.Millisecond):
			mu.Lock()
			n := len(delivered)
			mu.Unlock()
			if n > 0 {
				break outer
			}
		}
	}
	mu.Lock()
	n := len(delivered)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}
}
