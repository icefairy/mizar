package server

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ============================================================================
// Web UI 工作空间：浏览器上传的文件按会话落到服务器磁盘的
// <workspace_root>/<convID>/，Agent 的文件工具以该目录为 per-call cwd。
// 根目录默认 ~/.mizar/workspaces/（开箱可用），可在设置里改成别的绝对路径。
// ============================================================================

const (
	// workspaceMaxUploadTotal 单次上传请求体上限（多文件合计）。
	workspaceMaxUploadTotal = 100 << 20
	// workspaceMaxFileBytes 单文件上限。
	workspaceMaxFileBytes = 50 << 20
)

// defaultWorkspaceRoot 默认工作空间根目录（跟随 HOME，测试可用临时 HOME 隔离）。
func defaultWorkspaceRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "mizar-workspaces")
	}
	return filepath.Join(home, ".mizar", "workspaces")
}

// workspaceRoot 返回当前生效的工作空间根目录（配置值或默认值）。
func (s *Server) workspaceRoot() string {
	if s != nil && s.web != nil && s.web.store != nil {
		if v := strings.TrimSpace(s.web.store.getSettings().WorkspaceRoot); v != "" {
			if abs, err := filepath.Abs(v); err == nil {
				return abs
			}
			return v
		}
	}
	return defaultWorkspaceRoot()
}

// validateWorkspaceRoot 校验并规范化用户设置的根目录。
// 只接受绝对路径，拒绝系统敏感目录，并尝试创建目录确认可写。
func validateWorkspaceRoot(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil // 空 = 用默认值
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("工作空间根目录必须是绝对路径")
	}
	abs := filepath.Clean(p)
	if abs == string(os.PathSeparator) || abs == "." {
		return "", errors.New("不能把文件系统根目录作为工作空间")
	}
	// 拒绝明显的系统目录，避免误把上传文件写进系统路径（只做前缀判断）。
	lower := strings.ToLower(abs)
	for _, bad := range []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "c:\\windows"} {
		if lower == bad || strings.HasPrefix(lower, bad+string(os.PathSeparator)) || strings.HasPrefix(lower, bad+"\\") {
			return "", fmt.Errorf("不能使用系统目录 %s 作为工作空间", bad)
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("无法创建目录：%w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errors.New("该路径不是目录")
	}
	return abs, nil
}

// validConvID 会话 id 白名单（生成的 id 是 c+数字+hex，天然符合）。
func validConvID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// convWorkspaceDir 返回会话工作空间目录（不做存在性检查）。
func (s *Server) convWorkspaceDir(convID string) (string, error) {
	if !validConvID(convID) {
		return "", errors.New("非法的会话 id")
	}
	root := filepath.Clean(s.workspaceRoot())
	dir := filepath.Join(root, convID)
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", errors.New("会话目录越界")
	}
	return dir, nil
}

// safeFileName 净化上传文件名：只取基名，拒绝空/./.. 与控制字符。
func safeFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	base := filepath.Base(name)
	if base == "" || base == "." || base == ".." || name == "/" {
		return "", errors.New("非法文件名")
	}
	if strings.ContainsRune(base, 0) || len(base) > 255 {
		return "", errors.New("非法文件名")
	}
	return base, nil
}

// removeConvWorkspace 删除会话时联动清理其工作空间目录。
// 只删 <root>/<convID> 单层目录，删前校验 convID 合法且路径在 root 内。
func (s *Server) removeConvWorkspace(convID string) {
	dir, err := s.convWorkspaceDir(convID)
	if err != nil {
		return
	}
	root := filepath.Clean(s.workspaceRoot())
	if dir == root || !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return
	}
	_ = os.RemoveAll(dir)
}

// handleWorkspaceRoot GET /web/api/workspace/root → 当前生效根目录。
func (s *Server) handleWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root := s.workspaceRoot()
	writeJSON(w, http.StatusOK, map[string]any{
		"root":       root,
		"default":    defaultWorkspaceRoot(),
		"is_default": root == defaultWorkspaceRoot(),
	})
}

// handleWorkspaceFiles GET /web/api/workspace/files?conversation_id= → 列文件。
func (s *Server) handleWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	convID := r.URL.Query().Get("conversation_id")
	if s.web.store.get(convID) == nil {
		errJSON(w, http.StatusNotFound, "会话不存在")
		return
	}
	dir, err := s.convWorkspaceDir(convID)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	files := []map[string]any{}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".upload-") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, map[string]any{
			"name": e.Name(), "size": info.Size(), "mod": info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "root": dir})
}

// handleWorkspaceUpload POST multipart: conversation_id（query 或首个字段）+ file 多份。
// 流式落盘，不整份读进内存；同名覆盖；临时文件 + 原子 rename。
func (s *Server) handleWorkspaceUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, workspaceMaxUploadTotal)
	mr, err := r.MultipartReader()
	if err != nil {
		errJSON(w, http.StatusBadRequest, "需要 multipart/form-data")
		return
	}
	convID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if convID != "" && s.web.store.get(convID) == nil {
		errJSON(w, http.StatusNotFound, "会话不存在")
		return
	}
	dir := ""
	if convID != "" {
		if dir, err = s.convWorkspaceDir(convID); err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		if err = os.MkdirAll(dir, 0o755); err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	var saved []map[string]any
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			errJSON(w, http.StatusBadRequest, "读取上传内容失败："+perr.Error())
			return
		}
		formName := part.FormName()
		if formName == "conversation_id" {
			b, _ := io.ReadAll(io.LimitReader(part, 256))
			part.Close()
			if convID == "" {
				convID = strings.TrimSpace(string(b))
				if s.web.store.get(convID) == nil {
					errJSON(w, http.StatusNotFound, "会话不存在")
					return
				}
				if dir, err = s.convWorkspaceDir(convID); err != nil {
					errJSON(w, http.StatusBadRequest, err.Error())
					return
				}
				if err = os.MkdirAll(dir, 0o755); err != nil {
					errJSON(w, http.StatusInternalServerError, err.Error())
					return
				}
			}
			continue
		}
		if formName != "file" {
			part.Close()
			continue
		}
		if dir == "" {
			part.Close()
			errJSON(w, http.StatusBadRequest, "缺少 conversation_id")
			return
		}
		base, ferr := safeFileName(part.FileName())
		if ferr != nil {
			part.Close()
			errJSON(w, http.StatusBadRequest, ferr.Error())
			return
		}
		dst := filepath.Join(dir, base)
		if filepath.Dir(dst) != filepath.Clean(dir) {
			part.Close()
			errJSON(w, http.StatusBadRequest, "非法文件名")
			return
		}
		// 拒绝覆盖软链：避免借已有软链把内容写到目录外。
		if fi, lerr := os.Lstat(dst); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			part.Close()
			errJSON(w, http.StatusBadRequest, "目标文件是软链，已拒绝覆盖")
			return
		}
		n, serr := writeUploadPart(dir, dst, part)
		part.Close()
		if serr != nil {
			errJSON(w, http.StatusBadRequest, serr.Error())
			return
		}
		saved = append(saved, map[string]any{"name": base, "saved_as": base, "size": n})
	}
	if len(saved) == 0 {
		errJSON(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	// 往会话里插一条普通文本，让模型下一轮知道工作空间里有这些文件。
	names := make([]string, 0, len(saved))
	for _, f := range saved {
		names = append(names, fmt.Sprintf("%v", f["name"]))
	}
	notice := "已上传 " + strings.Join(names, "、") + " 到本会话工作空间"
	if len(notice) > 300 {
		notice = notice[:300] + "…"
	}
	// touchTitle=false：上传提示不能抢走「新会话」的标题（首条真实提问才该成为标题）。
	s.web.store.appendMsgRaw(convID, convMessage{Role: "user", Content: notice}, false)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": saved, "notice": notice})
}

// writeUploadPart 把一份 multipart 文件写到 dst（同目录临时文件 + 原子 rename）。
// 返回写入字节数。超过单文件上限直接报错并清理临时文件。
func writeUploadPart(dir, dst string, part io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	n, err := io.Copy(tmp, io.LimitReader(part, workspaceMaxFileBytes+1))
	if err != nil {
		cleanup()
		return 0, err
	}
	if n > workspaceMaxFileBytes {
		cleanup()
		return 0, fmt.Errorf("单个文件超过 %dMB 上限", workspaceMaxFileBytes>>20)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	_ = os.Chmod(tmpName, 0o644)
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return 0, err
	}
	return n, nil
}

// handleWorkspaceDownload GET ?conversation_id=&name= → 下载单文件。
func (s *Server) handleWorkspaceDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dir, name, err := s.workspaceTarget(r)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	full := filepath.Join(dir, name)
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() {
		errJSON(w, http.StatusNotFound, "文件不存在")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlPathEscape(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// handleWorkspaceDelete DELETE ?conversation_id=&name= → 删单文件。
func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	dir, name, err := s.workspaceTarget(r)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	full := filepath.Join(dir, name)
	fi, err := os.Lstat(full)
	if err != nil {
		errJSON(w, http.StatusNotFound, "文件不存在")
		return
	}
	if !fi.Mode().IsRegular() {
		errJSON(w, http.StatusBadRequest, "只能删除普通文件")
		return
	}
	if err := os.Remove(full); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// workspaceTarget 解析并校验 conversation_id + name，返回会话目录与净化后的文件名。
func (s *Server) workspaceTarget(r *http.Request) (string, string, error) {
	convID := r.URL.Query().Get("conversation_id")
	if s.web.store.get(convID) == nil {
		return "", "", errors.New("会话不存在")
	}
	dir, err := s.convWorkspaceDir(convID)
	if err != nil {
		return "", "", err
	}
	name, err := safeFileName(r.URL.Query().Get("name"))
	if err != nil {
		return "", "", err
	}
	return dir, name, nil
}

// urlPathEscape 对下载文件名做 RFC 3986 编码（保留常见安全字符）。
func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
