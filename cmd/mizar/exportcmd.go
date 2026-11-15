package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mizar/internal/exporter"
	"mizar/internal/session"
)

// runExport 处理 `-export`：把会话导出为自包含 HTML（P-6）。
// 返回进程退出码（0 成功，1 失败）。
func runExport(sessionID string, contLast bool, sessDir, workDir, out, model string) int {
	wd := workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	st := session.New(expandHome(sessDir))
	if pathKey := session.EncodePathKey(wd); pathKey != "" {
		st = st.Scoped(pathKey)
	}
	// -c：续接全局最后一次会话
	if sessionID == "" && contLast {
		if id, scope := st.Last(); id != "" {
			st = session.New(expandHome(sessDir))
			if scope != "" {
				st = st.Scoped(scope)
			}
			sessionID = id
		}
	}
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "用法: mizar -export -session <会话ID> 或 mizar -export -c [-export-out 文件.html]")
		return 1
	}
	// 支持短 ID 前缀匹配；当前作用域找不到时再扫全部作用域（-export 常跨目录导出）
	if !st.Exists(sessionID) {
		found := ""
		for _, id := range st.List() {
			if id == sessionID || strings.HasPrefix(id, sessionID) {
				found = id
				break
			}
		}
		if found == "" {
			// 跨作用域查找：找到后把 st 切到该作用域才能 Load
			for scope, ids := range st.ScopedList() {
				for _, id := range ids {
					if id == sessionID || strings.HasPrefix(id, sessionID) {
						found = id
						st = session.New(expandHome(sessDir)).Scoped(scope)
						break
					}
				}
				if found != "" {
					break
				}
			}
		}
		if found != "" {
			sessionID = found
		}
	}
	msgs, err := st.Load(sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取会话失败:", err)
		return 1
	}
	title := "Mizar 会话"
	if tc, terr := session.NewTitleCache(expandHome(sessDir)); terr == nil {
		if t := tc.Get(sessionID); t != "" {
			title = t
		}
	}
	html := exporter.ToHTML(msgs, exporter.Options{
		Title:     title,
		SessionID: sessionID,
		Model:     model,
	})
	if out == "" {
		fmt.Print(html)
		return 0
	}
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		return 1
	}
	fmt.Printf("✓ 已导出 %d 条消息到 %s\n", len(msgs), out)
	return 0
}

// expandHome 展开 ~/ 前缀。
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}
