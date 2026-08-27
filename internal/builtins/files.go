package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mizar/internal/plugins"
	"mizar/internal/utils"
)

// 双限截断常量（对齐 pi：2000 行 / 50KB）
const (
	readMaxLines = 2000
	readMaxBytes = 50_000
	// bash 输出截断上限（对齐 pi）
	bashMaxBytes = 50_000
	// read 文件大小预检：超过 10MB 拒绝全量读（防内存不可预估消耗）
	readMaxFileBytes = 10 * 1024 * 1024
)

// bomUTF8 UTF-8 BOM 字节序列（编辑器常见，编辑时剥去，写回时还原）
var bomUTF8 = []byte{0xEF, 0xBB, 0xBF}

// detectLineEndings 检测文件行尾风格：\r\n=CRLF, \n=LF, 其他=LF（兜底）。
// 返回原始行尾标记（"\r\n" 或 "\n"），供写回时保真。
func detectLineEndings(content string) string {
	cr := strings.Count(content, "\r\n")
	nl := strings.Count(content, "\n")
	if cr > 0 && nl > cr {
		return "\r\n"
	}
	return "\n"
}

// dumpToTemp 将超限输出落盘到临时文件，返回路径
func dumpToTemp(content string) (string, error) {
	f, err := os.CreateTemp("", "mizar-bash-*.log")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// isDocumentFile 判断是否为文档格式文件（按扩展名）。
func isDocumentFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".docx", ".xlsx":
		return true
	}
	return false
}

// toolRead 读文件（对齐 pi 的 read：path + offset + limit，双限截断 + 续读提示）。
// 支持文本文件 + 文档格式（PDF/DOCX/XLSX）。
// 文件大小预检 >10MB 拒绝全量读，防止内存不可预估消耗。
func toolRead() plugins.Tool {
	return plugins.Tool{
		Name:        "read",
		Description: "Read a text file. Args: {path: string, offset?: number(1-indexed), limit?: number(lines)}. Returns content with line numbers, truncated at 2000 lines/50KB with a continue hint. Files over 10MB are rejected. Supports PDF, DOCX, XLSX.",
		Run: func(args string) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("read: args {path} required")
			}
			// 大小预检：>10MB 拒绝，防 OOM
			fi, err := os.Stat(p.Path)
			if err != nil {
				return "", err
			}
			if fi.Size() > readMaxFileBytes {
				return "", fmt.Errorf("read: %s is %.1fMB (limit %dMB); use grep/find to search, or bash: sed -n / head -c to read slices",
					p.Path, float64(fi.Size())/(1024*1024), readMaxFileBytes/(1024*1024))
			}

			// 文档格式走专用解析器
			if isDocumentFile(p.Path) {
				switch strings.ToLower(filepath.Ext(p.Path)) {
				case ".pdf":
					text, err := utils.ExtractPDF(p.Path)
					if err != nil {
						return "", fmt.Errorf("read PDF: %w", err)
					}
					return text, nil
				case ".docx":
					text, err := utils.ExtractDOCX(p.Path)
					if err != nil {
						return "", fmt.Errorf("read DOCX: %w", err)
					}
					return text, nil
				case ".xlsx":
					text, err := utils.ExtractXLSX(p.Path)
					if err != nil {
						return "", fmt.Errorf("read XLSX: %w", err)
					}
					return text, nil
				}
			}

			b, err := os.ReadFile(p.Path)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(b), "\n")
			total := len(lines)
			start, end := 0, total
			if p.Offset > 0 {
				start = p.Offset - 1
			}
			if p.Limit > 0 {
				end = start + p.Limit
				if end > total {
					end = total
				}
			}
			if start >= total {
				start = total - 1
			}
			if start < 0 {
				start = 0
			}
			// 行数限
			if end-start > readMaxLines {
				end = start + readMaxLines
			}
			// 拼接显示，同时按字节限（50KB）截断
			var sb strings.Builder
			byteUsed := 0
			shown := 0
			for i := start; i < end; i++ {
				line := fmt.Sprintf("%d|%s\n", i+1, lines[i])
				byteUsed += len(line)
				if byteUsed > readMaxBytes {
					break
				}
				sb.WriteString(line)
				shown++
			}
			truncated := end < total || byteUsed > readMaxBytes
			var head strings.Builder
			if truncated {
				fmt.Fprintf(&head, "%d lines (total %d, truncated). Use offset=%d to continue.\n", shown, total, start+shown+1)
			} else {
				fmt.Fprintf(&head, "%d lines (total %d)\n", shown, total)
			}
			return strings.TrimRight(head.String()+sb.String(), "\n"), nil
		},
	}
}

// toolWrite 写文件（对齐 pi 的 write：path + content，自动建父目录）。
func toolWrite() plugins.Tool {
	return plugins.Tool{
		Name:        "write",
		Description: "Write content to a file (creates parent dirs, overwrites). Args: {path: string, content: string}.",
		Run: func(args string) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("write: args {path, content} required")
			}
			if dir := filepath.Dir(p.Path); dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "", err
				}
			}
			if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	}
}

// toolEdit 精准替换（对齐 pi 的 edit：path + oldText + newText，可多组 edits）。
func toolEdit() plugins.Tool {
	return plugins.Tool{
		Name:        "edit",
		Description: "Edit a file with targeted replacements. Args: {path: string, oldText: string, newText: string} or {path: string, edits: [{oldText, newText}]}. oldText must be unique. Returns diff summary.",
		Run: func(args string) (string, error) {
			var p struct {
				Path    string `json:"path"`
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
				Edits   []struct {
					OldText string `json:"oldText"`
					NewText string `json:"newText"`
				} `json:"edits"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("edit: args {path} required")
			}
			b, err := os.ReadFile(p.Path)
			if err != nil {
				return "", err
			}
			content := string(b)
			// BOM 剥离 + 行尾检测（保真写回）
			lineEnding := detectLineEndings(content)
			if strings.HasPrefix(content, "\xef\xbb\xbf") || string(b[:3]) == string(bomUTF8) {
				content = content[1:] // 剥去 BOM
				b = b[3:]
			}
			edits := p.Edits
			if len(edits) == 0 && p.OldText != "" {
				edits = []struct {
					OldText string `json:"oldText"`
					NewText string `json:"newText"`
				}{{OldText: p.OldText, NewText: p.NewText}}
			}
			if len(edits) == 0 {
				return "", fmt.Errorf("edit: oldText or edits required")
			}
			// 从后往前应用，防前面 edit 改变 offset 导致后面错配（对齐 pi 的多编辑倒序）
			type replacement struct {
				start   int
				end     int
				newText string
			}
			var reps []replacement
			for _, e := range edits {
				if e.OldText == "" {
					continue
				}
				idx := strings.Index(content, e.OldText)
				if idx < 0 {
					return "", fmt.Errorf("edit: oldText %q not found", truncate(e.OldText, 50))
				}
				count := strings.Count(content, e.OldText)
				if count != 1 {
					return "", fmt.Errorf("edit: oldText %q appears %d times (must be unique)", truncate(e.OldText, 50), count)
				}
				reps = append(reps, replacement{start: idx, end: idx + len(e.OldText), newText: e.NewText})
			}
			if len(reps) == 0 {
				return "", fmt.Errorf("edit: no edits applied")
			}
			// 按位置降序替换，避免偏移错乱
			sort.Slice(reps, func(i, j int) bool { return reps[i].start > reps[j].start })
			for _, r := range reps {
				content = content[:r.start] + r.newText + content[r.end:]
			}
			if err := os.WriteFile(p.Path, []byte(content), 0o644); err != nil {
				return "", err
			}
			// 若原始文件有 BOM 或 CRLF，写回时补上（保真）
			if strings.HasPrefix(string(b), string(bomUTF8)) || lineEnding == "\r\n" {
				// 重建带 BOM/CRLF 的内容
				final := content
				if lineEnding == "\r\n" && !strings.Contains(content, "\r\n") {
					final = strings.ReplaceAll(content, "\n", "\r\n")
				}
				if strings.HasPrefix(string(b), string(bomUTF8)) {
					final = string(bomUTF8) + final
				}
				if err := os.WriteFile(p.Path, []byte(final), 0o644); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("applied %d edit(s) to %s", len(reps), p.Path), nil
		},
	}
}

// toolLS 列目录（对齐 pi 的 ls：path + limit）。
func toolLS() plugins.Tool {
	return plugins.Tool{
		Name:        "ls",
		Description: "List directory entries. Args: {path?: string, limit?: number(default 500)}. Directories suffixed with /.",
		Run: func(args string) (string, error) {
			var p struct {
				Path  string `json:"path"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				return "", err
			}
			if p.Path == "" {
				p.Path = "."
			}
			if p.Limit <= 0 {
				p.Limit = 500
			}
			entries, err := os.ReadDir(p.Path)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > p.Limit {
				names = names[:p.Limit]
			}
			return fmt.Sprintf("%d entries:\n%s", len(entries), strings.Join(names, "\n")), nil
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
