package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
)

// FTPBridge 给插件提供 FTP 客户端能力：
//   ftp_connect(host, port, user, pass) -> ftpID
//   ftp_list(ftpID, dir) -> JSON
//   ftp_download(ftpID, remotePath, localPath)
//   ftp_upload(ftpID, localPath, remotePath)
//   ftp_mkdir(ftpID, dir)
//   ftp_rmdir(ftpID, dir)
//   ftp_delete(ftpID, path)
//   ftp_rename(ftpID, from, to)
//   ftp_close(ftpID)
type FTPBridge struct {
	mu    sync.Mutex
	conns map[string]*ftpConn
	next  int
}

type ftpConn struct {
	conn   *ftp.ServerConn
	mu     sync.Mutex
	closed bool
}

func NewFTPBridge() *FTPBridge {
	return &FTPBridge{conns: make(map[string]*ftpConn)}
}

// Connect 建立 FTP 连接并登录。
func (b *FTPBridge) Connect(host string, port int, user, pass string) (string, error) {
	addr := host + ":" + strconv.Itoa(port)
	c, err := ftp.Dial(addr, ftp.DialWithTimeout(10*time.Second))
	if err != nil {
		return "", fmt.Errorf("ftp_connect %s: %w", addr, err)
	}
	if err := c.Login(user, pass); err != nil {
		c.Quit()
		return "", fmt.Errorf("ftp_login %s: %w", addr, err)
	}
	b.mu.Lock()
	b.next++
	id := fmt.Sprintf("ftp-%d", b.next)
	b.conns[id] = &ftpConn{conn: c}
	b.mu.Unlock()
	return id, nil
}

// List 列出目录。
func (b *FTPBridge) List(id, dir string) (string, error) {
	c, ok := b.get(id)
	if !ok {
		return "", fmt.Errorf("ftp_list: conn %s not found", id)
	}
	entries, err := c.conn.List(dir)
	if err != nil {
		return "", err
	}
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// Upload 上传文件（本地→远程）。
func (b *FTPBridge) Upload(id, localPath, remotePath string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_upload: conn %s not found", id)
	}
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()
	return c.conn.Stor(remotePath, src)
}

// Download 下载文件（远程→本地）。
func (b *FTPBridge) Download(id, remotePath, localPath string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_download: conn %s not found", id)
	}
	r, err := c.conn.Retr(remotePath)
	if err != nil {
		return err
	}
	defer r.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, r)
	return err
}

// Mkdir 创建目录。
func (b *FTPBridge) Mkdir(id, dir string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_mkdir: conn %s not found", id)
	}
	return c.conn.MakeDir(dir)
}

// Rmdir 删除目录。
func (b *FTPBridge) Rmdir(id, dir string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_rmdir: conn %s not found", id)
	}
	return c.conn.RemoveDir(dir)
}

// Delete 删除文件。
func (b *FTPBridge) Delete(id, path string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_delete: conn %s not found", id)
	}
	return c.conn.Delete(path)
}

// Rename 重命名文件/目录。
func (b *FTPBridge) Rename(id, from, to string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_rename: conn %s not found", id)
	}
	return c.conn.Rename(from, to)
}

// Close 关闭 FTP 连接。
func (b *FTPBridge) Close(id string) error {
	c, ok := b.get(id)
	if !ok {
		return fmt.Errorf("ftp_close: conn %s not found", id)
	}
	b.mu.Lock()
	delete(b.conns, id)
	b.mu.Unlock()
	return c.conn.Quit()
}

func (b *FTPBridge) get(id string) (*ftpConn, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.conns[id]
	return c, ok
}