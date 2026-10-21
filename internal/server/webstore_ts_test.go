package server

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWebstoreAppendMsgTimestamp(t *testing.T) {
	st := newWebStore(filepath.Join(t.TempDir(), "webui.json"))
	c := st.create("chat")
	st.appendMsg(c.ID, convMessage{Role: "user", Content: "hi"})
	got := st.get(c.ID)
	if len(got.Messages) != 1 || got.Messages[0].Ts == "" {
		t.Fatalf("expected ts, got %+v", got.Messages)
	}
	if _, err := time.Parse(time.RFC3339, got.Messages[0].Ts); err != nil {
		t.Fatalf("bad ts %q: %v", got.Messages[0].Ts, err)
	}
}
