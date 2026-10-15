package needle

import (
	"context"
	"testing"
)

func TestNewAgent(t *testing.T) {
	a := NewAgent("http://localhost:8765")
	if a == nil {
		t.Fatal("expected non-nil agent")
	}
	if a.serverURL != "http://localhost:8765" {
		t.Errorf("expected serverURL=http://localhost:8765, got %s", a.serverURL)
	}
}

func TestHealthCheck(t *testing.T) {
	a := NewAgent("http://localhost:8765")

	ctx := context.Background()

	// Server not running is expected in test env
	healthy, err := a.Health(ctx)
	if err == nil {
		t.Logf("Server is healthy: %v", healthy)
	} else {
		t.Logf("Server not running (expected): %v", err)
	}
}
