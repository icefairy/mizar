package engine

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// NetBridge 给插件提供 TCP 网络能力：
//   tcp_listen(addr, onAccept) -> serverID
//   tcp_dial(addr) -> connID
//   tcp_send(connID, data)
//   tcp_onrecv(connID, callback)
//   tcp_close(connID)
//   tcp_stop(serverID)
//
// 服务器端用 accept 回调返回 connID+remoteAddr；接收端用 onRecv 回调返回数据。
type NetBridge struct {
	mu        sync.Mutex
	listeners map[string]*tcpListener
	conns     map[string]*netConn
	next      int
}

type tcpListener struct {
	ln     net.Listener
	mu     sync.Mutex
	closed bool
}
type netConn struct {
	conn          net.Conn
	mu            sync.Mutex
	closed        bool
	recvCallback  func(data string)
	recvDone      chan struct{}
}

func NewNetBridge() *NetBridge {
	return &NetBridge{listeners: make(map[string]*tcpListener), conns: make(map[string]*netConn)}
}

// Listen 启动 TCP server。onAccept 在每接入一个连接时调用（connID+remoteAddr）。
func (b *NetBridge) Listen(addr string, onAccept func(connID, remoteAddr string)) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("tcp_listen %s: %w", addr, err)
	}
	b.mu.Lock()
	b.next++
	id := fmt.Sprintf("tcp-srv-%d", b.next)
	b.listeners[id] = &tcpListener{ln: ln}
	b.mu.Unlock()
	go b.acceptLoop(id, ln, onAccept)
	return id, nil
}

func (b *NetBridge) acceptLoop(id string, ln net.Listener, onAccept func(connID, remoteAddr string)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			b.Stop(id)
			return
		}
		b.mu.Lock()
		b.next++
		connID := fmt.Sprintf("tcp-conn-%d", b.next)
		b.conns[connID] = &netConn{conn: conn, recvDone: make(chan struct{})}
		b.mu.Unlock()
		onAccept(connID, conn.RemoteAddr().String())
	}
}

// Dial 发起 TCP 连接。
func (b *NetBridge) Dial(addr string) (string, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("tcp_dial %s: %w", addr, err)
	}
	b.mu.Lock()
	b.next++
	id := fmt.Sprintf("tcp-conn-%d", b.next)
	b.conns[id] = &netConn{conn: conn, recvDone: make(chan struct{})}
	b.mu.Unlock()
	return id, nil
}

// Send 发送数据（文本）。
func (b *NetBridge) Send(id, data string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("tcp_send: conn %s not found", id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("tcp_send: conn %s closed", id)
	}
	_, err := c.conn.Write([]byte(data))
	return err
}

// OnRecv 注册接收数据回调。旧回调自动取消。
func (b *NetBridge) OnRecv(id string, cb func(data string)) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("tcp_onrecv: conn %s not found", id)
	}
	c.mu.Lock()
	if c.recvCallback != nil {
		close(c.recvDone)
	}
	c.recvCallback = cb
	c.recvDone = make(chan struct{})
	c.mu.Unlock()
	go b.recvLoop(id, c)
	return nil
}

func (b *NetBridge) recvLoop(id string, c *netConn) {
	var buf [4096]byte
	for {
		select {
		case <-c.recvDone:
			return
		default:
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			conn := c.conn
			c.mu.Unlock()
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, err := conn.Read(buf[:])
			if err != nil {
				if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
					return
				}
				continue
			}
			c.mu.Lock()
			cb := c.recvCallback
			c.mu.Unlock()
			if cb != nil {
				cb(string(buf[:n]))
			}
		}
	}
}

// Close 关闭连接。
func (b *NetBridge) Close(id string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("tcp_close: conn %s not found", id)
	}
	c.mu.Lock()
	if c.recvCallback != nil {
		close(c.recvDone)
		c.recvCallback = nil
	}
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	b.mu.Lock()
	delete(b.conns, id)
	b.mu.Unlock()
	return conn.Close()
}

// Stop 停止 TCP server，关闭所有已接入的连接。
func (b *NetBridge) Stop(id string) error {
	b.mu.Lock()
	l, ok := b.listeners[id]
	delete(b.listeners, id)
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("tcp_stop: server %s not found", id)
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	return l.ln.Close()
}

func (b *NetBridge) get(id string) (*netConn, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.conns[id]
	return c, ok
}
