package builtins

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
)

// uploadMaxBytes upload_file 单次写入上限（解码后的字节数）。
// 防止客户端把超大 base64 塞进一次工具调用把内存撑爆。
const uploadMaxBytes = 32 << 20

// toolUploadFile 从文本或 base64 内容创建文件（供 MCP 客户端等外部调用方上传）。
// 与 write 的区别：支持 base64，能把二进制文件原样落盘。
func toolUploadFile() plugins.Tool {
	const desc = "Create a file from text or base64 content (auto-creates parent dirs). " +
		"Use encoding=base64 for binary files. Args: {path: string, content: string, encoding?: \"text\"|\"base64\"(default text)}. " +
		"Max decoded size 32MB. Path is relative to the workspace/workdir and cannot escape it."
	run, runInDir := dualRun(func(cwd, args string) (string, error) {
		var p struct {
			Path     string `json:"path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal([]byte(args), &p); err != nil || strings.TrimSpace(p.Path) == "" {
			return "", fmt.Errorf("upload_file: args {path, content} required")
		}
		path, err := resolvePath(cwd, p.Path)
		if err != nil {
			return "", fmt.Errorf("upload_file: %w", err)
		}
		var data []byte
		switch strings.ToLower(strings.TrimSpace(p.Encoding)) {
		case "", "text":
			data = []byte(p.Content)
		case "base64":
			// 容忍 PEM/MIME 常见的换行与空白，避免调用方必须先压缩成一整行。
			// 用 ASCII 码判断，避免源码里的转义字符在不同编辑器/传输层被改写。
			clean := strings.Map(func(r rune) rune {
				switch r {
				case 32, 9, 10, 13: // space, tab, LF, CR
					return -1
				}
				return r
			}, p.Content)
			data, err = base64.StdEncoding.DecodeString(clean)
			if err != nil {
				return "", fmt.Errorf("upload_file: invalid base64: %w", err)
			}
		default:
			return "", fmt.Errorf("upload_file: unsupported encoding %q (use text or base64)", p.Encoding)
		}
		if len(data) > uploadMaxBytes {
			return "", fmt.Errorf("upload_file: %d bytes exceeds %dMB limit", len(data), uploadMaxBytes>>20)
		}
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("uploaded %d bytes to %s", len(data), path), nil
	})
	return plugins.Tool{Name: "upload_file", Description: desc, Run: run, RunInDir: runInDir}
}
