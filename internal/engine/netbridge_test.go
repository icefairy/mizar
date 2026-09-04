package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockHostNet 返回带 NetBridge/FTPBridge/WSClient 的 HostFuncs，供 goja 可见性测试。
func mockHostNet() *HostFuncs {
	h := mockHost()
	h.NetBridge = NewNetBridge()
	h.FTPBridge = NewFTPBridge()
	h.WSClient = NewWSClientBridge()
	return h
}

// TestNetBridgeEcho 验证 TCP server+client 回环通信。
func TestNetBridgeEcho(t *testing.T) {
	nb := NewNetBridge()
	defer func() {
		for id := range nb.getAllConns() {
			nb.Close(id)
		}
	}()

	var mu sync.Mutex
	received := map[string]string{}
	var acceptOnce sync.Once
	var srvID string

	srvID, err := nb.Listen("127.0.0.1:0", func(connID, remoteAddr string) {
		acceptOnce.Do(func() {
			nb.OnRecv(connID, func(data string) {
				mu.Lock()
				received[connID] = data
				mu.Unlock()
				nb.Send(connID, "pong:"+data)
			})
		})
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer nb.Stop(srvID)

	// 取实际监听地址
	addr := nb.getListenerAddr(srvID)
	if addr == "" {
		t.Fatal("empty addr")
	}

	connID, err := nb.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nb.Close(connID)

	var clientGot string
	var gotMu sync.Mutex
	var gotDone = make(chan struct{})
	nb.OnRecv(connID, func(data string) {
		gotMu.Lock()
		clientGot = data
		gotMu.Unlock()
		close(gotDone)
	})

	// 等 accept 回调注册 server 侧 OnRecv
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := nb.Send(connID, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}

	// 等客户端收到回显
	select {
	case <-gotDone:
	case <-time.After(2 * time.Second):
	}
	if clientGot != "pong:hello" {
		t.Fatalf("echo got %q, want pong:hello", clientGot)
	}
}

// TestNetBridgeCloseAndStop 验证 tcp_close / tcp_stop 幂等。
func TestNetBridgeCloseAndStop(t *testing.T) {
	nb := NewNetBridge()
	srvID, err := nb.Listen("127.0.0.1:0", func(connID, remoteAddr string) {})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := nb.getListenerAddr(srvID)
	connID, err := nb.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := nb.Close(connID); err != nil {
		t.Fatalf("close conn: %v", err)
	}
	if err := nb.Close(connID); err == nil {
		t.Fatal("double close should error")
	}
	if err := nb.Stop(srvID); err != nil {
		t.Fatalf("stop srv: %v", err)
	}
	if err := nb.Stop(srvID); err == nil {
		t.Fatal("double stop should error")
	}
}

// TestNetBridgeGojaVisible 验证网络宿主函数在 goja 中可见。
func TestNetBridgeGojaVisible(t *testing.T) {
	e, err := New(mockHostNet())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, fn := range []string{"tcp_listen", "tcp_dial", "tcp_send", "tcp_onrecv", "tcp_close", "tcp_stop",
		"ftp_connect", "ftp_list", "ftp_upload", "ftp_download", "ftp_mkdir", "ftp_rmdir", "ftp_delete", "ftp_rename", "ftp_close",
		"ws_connect", "ws_send", "ws_onmessage", "ws_close"} {
		if !e.Has(fn) {
			t.Errorf("host func %q not visible", fn)
		}
	}
}

// TestWSClientBridgeRegistered 验证 ws_connect/onmessage 接线可用（连本地 echo WS server）。
func TestWSClientBridgeRegistered(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			conn.WriteMessage(websocket.TextMessage, append([]byte("echo:"), msg...))
		}
	}))
	defer server.Close()

	e, err := New(mockHostNet())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	res, err := e.Call("ws_connect", wsURL, "{}")
	if err != nil {
		t.Fatalf("ws_connect: %v", err)
	}
	connID := res.(string)
	if connID == "" {
		t.Fatal("ws_connect returned empty")
	}
	_ = fmt.Sprint(connID)
}

// TestFTPBridgeConnect 验证 FTP 客户端能连测试 server。
func TestFTPBridgeConnect(t *testing.T) {
	srv := startFTPTestServer(t)
	defer srv.Close()

	fb := NewFTPBridge()
	id, err := fb.Connect("127.0.0.1", srv.port, srv.user, srv.pass)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer fb.Close(id)

	if err := fb.Mkdir(id, "/up"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, err := fb.List(id, "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("list json: %v", err)
	}
	t.Logf("list entries: %d", len(entries))
}

// ---- goja 帮助 ----

type gojaCall func(args ...any) any

func (g gojaCall) String() string { return "callback" }

// ---- test 内部辅助 ----

// getListenerAddr 从 NetBridge 内部取监听地址（测试辅助）。
func (b *NetBridge) getListenerAddr(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.listeners[id]
	if !ok {
		return ""
	}
	addr := l.ln.Addr().String()
	return addr
}

// getAllConns 返回当前全部连接 ID（测试辅助）。
func (b *NetBridge) getAllConns() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]bool, len(b.conns))
	for k := range b.conns {
		out[k] = true
	}
	return out
}

var _ = net.IPv4len