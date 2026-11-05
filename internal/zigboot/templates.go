// templates.go —— 内嵌 Zig 项目脚手架模板，配合 /zig new-mcp、/zig new-wasm 生成骨架。
//
// 为什么要内嵌模板而不是让用户去仓库里翻文件：内网离线环境下用户手上只有
// 一个 Mizar 二进制，「一条命令生成能跑的骨架」比「去 GitHub 复制示例」可靠得多。
package zigboot

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed all:templates
var templatesFS embed.FS

// NewMCPServer 在 dir 下生成一个可直接编译的 Zig MCP server 骨架（形态 B）。
// dir 已存在同名 server.zig 时不覆盖，返回 needsForce=true。
func NewMCPServer(dir string) (path string, needsForce bool, err error) {
	return releaseTemplate("templates/zig-mcp/server.zig", filepath.Join(dir, "server.zig"))
}

// NewWasmKernel 在 dir 下生成一个通用 ABI wasm 内核骨架（形态 A）。
func NewWasmKernel(dir string) (path string, needsForce bool, err error) {
	return releaseTemplate("templates/zig-wasm/kernel.zig", filepath.Join(dir, "kernel.zig"))
}

func releaseTemplate(embedPath, dst string) (string, bool, error) {
	if _, statErr := os.Stat(dst); statErr == nil {
		return dst, true, nil
	}
	data, err := templatesFS.ReadFile(embedPath)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", false, err
	}
	return dst, false, nil
}
