package engine

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ftpTestServer 内嵌最小 FTP server（仅测试用，支持 LIST/STOR/RETR/DELE/MKD/RMD/RENAME）。
type ftpTestServer struct {
	root string
	port int
	user string
	pass string
	ln   net.Listener
	wg   sync.WaitGroup
}

func startFTPTestServer(t *testing.T) *ftpTestServer {
	t.Helper()
	tmp := t.TempDir()
	s := &ftpTestServer{root: tmp, user: "testuser", pass: "testpass"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ftp test server listen: %v", err)
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.wg.Add(1)
	go s.serve()
	time.Sleep(50 * time.Millisecond)
	return s
}

func (s *ftpTestServer) Close() {
	s.ln.Close()
	s.wg.Wait()
}

func (s *ftpTestServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			s.handle(conn)
			s.wg.Done()
		}()
	}
}

func (s *ftpTestServer) handle(conn net.Conn) {
	defer conn.Close()

	_, _ = conn.Write([]byte("220 test FTP\r\n"))
	var loggedIn bool
	var curDir = "/"
	// PASV 模式：数据连接通过 channel 传递
	dataCh := make(chan net.Conn, 1)
	var pasvStopMu sync.Mutex
	var pasvListener net.Listener

	stopPASV := func() {
		pasvStopMu.Lock()
		if pasvListener != nil {
			pasvListener.Close()
			pasvListener = nil
		}
		pasvStopMu.Unlock()
	}

	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		cmd := strings.ToUpper(line)
		arg := ""
		if idx := strings.IndexAny(line, " \t"); idx >= 0 {
			cmd = strings.ToUpper(line[:idx])
			arg = strings.TrimSpace(line[idx+1:])
		}

		switch cmd {
		case "USER":
			_, _ = conn.Write([]byte("331 ok\r\n"))
		case "PASS":
			if arg == s.pass { // 只检查密码
				loggedIn = true
				_, _ = conn.Write([]byte("230 ok\r\n"))
			} else {
				_, _ = conn.Write([]byte("530 bad\r\n"))
			}
		case "PWD":
			_, _ = conn.Write([]byte("257 \"" + curDir + "\"\r\n"))
		case "CWD":
			if strings.HasPrefix(arg, "/") {
				curDir = arg
			} else {
				curDir = cleanFTPPath(curDir + "/" + arg)
			}
			_, _ = conn.Write([]byte("250 ok\r\n"))
		case "CDUP":
			curDir = cleanFTPPath(curDir + "/..")
			_, _ = conn.Write([]byte("250 ok\r\n"))
		case "TYPE":
			_, _ = conn.Write([]byte("200 ok\r\n"))
		case "PASV":
			stopPASV() // 关旧 listener
			dl, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				_, _ = conn.Write([]byte("425 pasv fail\r\n"))
				continue
			}
			pasvListener = dl
			dp := dl.Addr().(*net.TCPAddr).Port
			_, _ = conn.Write([]byte(fmt.Sprintf("227 Entering Passive Mode (127,0,0,1,%d,%d)\r\n", dp/256, dp%256)))
			// goroutine accept 数据连接
			go func() {
				dl.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
				dconn, err := dl.Accept()
				if err != nil {
					return
				}
				dataCh <- dconn
			}()
		case "LIST":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			_, _ = conn.Write([]byte("150 data\r\n"))
			select {
			case dconn := <-dataCh:
				target := s.root
				if arg != "" {
					target = filepath.Join(target, arg)
				}
				ents, _ := os.ReadDir(target)
				for _, e := range ents {
					info, _ := e.Info()
					_, _ = dconn.Write([]byte(fmt.Sprintf("-rw-r--r-- 1 user user %10d  Jan  1 00:00 %s\r\n", info.Size(), e.Name())))
				}
				dconn.Close()
			case <-time.After(5 * time.Second):
			}
			_, _ = conn.Write([]byte("226 done\r\n"))
		case "MKD":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			os.MkdirAll(s.absPath(arg, curDir), 0755)
			_, _ = conn.Write([]byte(fmt.Sprintf("257 \"%s\"\r\n", arg)))
		case "RMD":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			os.RemoveAll(s.absPath(arg, curDir))
			_, _ = conn.Write([]byte("250 ok\r\n"))
		case "DELE":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			os.Remove(s.absPath(arg, curDir))
			_, _ = conn.Write([]byte("250 ok\r\n"))
		case "RENAME":
			_, _ = conn.Write([]byte("250 ok\r\n"))
		case "STOR":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			_, _ = conn.Write([]byte("150 data\r\n"))
			select {
			case dconn := <-dataCh:
				p := s.absPath(arg, curDir)
				dst, err := os.Create(p)
				if err == nil {
					io.Copy(dst, dconn)
					dst.Close()
				}
				dconn.Close()
			case <-time.After(5 * time.Second):
			}
			_, _ = conn.Write([]byte("226 done\r\n"))
		case "RETR":
			if !loggedIn {
				_, _ = conn.Write([]byte("530 no\r\n"))
				continue
			}
			_, _ = conn.Write([]byte("150 data\r\n"))
			select {
			case dconn := <-dataCh:
				p := s.absPath(arg, curDir)
				src, err := os.Open(p)
				if err == nil {
					io.Copy(dconn, src)
					src.Close()
				}
				dconn.Close()
			case <-time.After(5 * time.Second):
			}
			_, _ = conn.Write([]byte("226 done\r\n"))
		case "QUIT":
			_, _ = conn.Write([]byte("221 bye\r\n"))
			return
		default:
			_, _ = conn.Write([]byte("502 no\r\n"))
		}
	}
}

func (s *ftpTestServer) absPath(rel, cur string) string {
	if strings.HasPrefix(rel, "/") {
		return filepath.Join(s.root, rel)
	}
	return filepath.Join(s.root, cur, rel)
}

func cleanFTPPath(p string) string {
	parts := strings.Split(p, "/")
	var out []string
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return "/" + strings.Join(out, "/")
}