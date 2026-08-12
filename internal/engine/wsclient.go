// WebSocket 客户端能力：供 JS 插件发起 WS 连接（如连飞书长连接）。
//
// 与 server 包的服务端 ws_emit（推送）互补：这里是插件作为**客户端**
// 主动 connect 外部 WS 服务，收事件、发消息。
//
// JS 侧 API（host 注入，每个引擎一个桥）：
//   ws_connect(url, headersJSON) -> connID (string)  // 建立连接（异步）
//   ws_send(connID, data)                            // 发送文本
//   ws_onmessage(connID, callbackJS)                 // 注册收消息回调
//   ws_close(connID)                                 // 关闭连接
//
// 回调签名：callback(messageText)  // 服务端推来的原始文本
package engine

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/gorilla/websocket"
)

// WSClientBridge 是一个引擎的 WS 客户端桥（gorilla/websocket 封装）。
type WSClientBridge struct {
	mu    sync.Mutex
	conns map[string]*wsConn
	next  int
}

type wsConn struct {
	conn   *websocket.Conn
	mu     sync.Mutex // 保护写
	closed bool
}

// NewWSClientBridge 创建桥。
func NewWSClientBridge() *WSClientBridge {
	return &WSClientBridge{conns: make(map[string]*wsConn)}
}

// Connect 建立 WS 连接（阻塞至握手完成）。
// headersJSON 形如 {"Authorization":"Bearer xxx"}。
func (b *WSClientBridge) Connect(url, headersJSON string) (string, error) {
	hdr := map[string]string{}
	if headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &hdr); err != nil {
			return "", fmt.Errorf("ws_connect headers: %w", err)
		}
	}
	httpHdr := make(map[string][]string, len(hdr))
	for k, v := range hdr {
		httpHdr[k] = []string{v}
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, httpHdr)
	if err != nil {
		return "", fmt.Errorf("ws_connect %s: %w", url, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := fmt.Sprintf("ws%d", b.next)
	b.conns[id] = &wsConn{conn: conn}
	return id, nil
}

// Send 发送文本消息。
func (b *WSClientBridge) Send(id, data string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ws_send: conn %s not found", id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("ws_send: conn %s closed", id)
	}
	return c.conn.WriteMessage(websocket.TextMessage, []byte(data))
}

// OnMessage 注册收消息回调（回调 JS 函数，接收原始文本）。
func (b *WSClientBridge) OnMessage(id string, cb goja.Callable, vm *goja.Runtime) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ws_onmessage: conn %s not found", id)
	}
	go func() {
		for {
			_, data, err := c.conn.ReadMessage()
			if err != nil {
				return // 连接关闭/出错，结束读循环
			}
			// 在 goja 运行时中安全调用回调（收到消息，传原始文本）
			_, _ = cb(goja.Undefined(), vm.ToValue(string(data)))
		}
	}()
	return nil
}

// Close 关闭连接。
func (b *WSClientBridge) Close(id string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ws_close: conn %s not found", id)
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	b.mu.Lock()
	delete(b.conns, id)
	b.mu.Unlock()
	return c.conn.Close()
}

func (b *WSClientBridge) get(id string) (*wsConn, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.conns[id]
	return c, ok
}

// tick 占位防误用（保留未来心跳）。
func (b *WSClientBridge) tick() time.Time { return time.Now() }
