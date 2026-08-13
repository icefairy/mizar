// Package lsp 提供 LSP 客户端和工具注册。
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mizar/internal/plugins"
)

var (
	globalClient *Client
	globalInit   = false
)

// Init 初始化全局 LSP 客户端。
func Init(serverBinary string) {
	if globalInit {
		return
	}
	globalInit = true
	if serverBinary == "" {
		return
	}
	binary := serverBinary
	if !filepath.IsAbs(binary) {
		if p, err := exec.LookPath(binary); err == nil {
			binary = p
		}
	}
	cfg := ClientConfig{
		ServerBinary: binary,
		Workspace:    ".",
	}
	if client, err := NewClient(cfg); err == nil {
		globalClient = client
	}
}

func toolDiagnostics() plugins.Tool {
	return plugins.Tool{
		Name:        "lsp_diagnostics",
		Description: "查询文件的 LSP 诊断信息（错误/警告）。Args: {path: string}。返回诊断列表。",
		Run: func(args string) (string, error) {
			var p struct{ Path string }
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("lsp_diagnostics: args {path} required")
			}
			if globalClient == nil {
				return "LSP 客户端未初始化（未配置语言服务器，如 gopls）", nil
			}
			abs, err := filepath.Abs(p.Path)
			if err != nil {
				return "", err
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Sprintf("lsp_diagnostics: file not found: %s", p.Path), nil
			}
			ctx := context.Background()
			diags, err := globalClient.Diagnostics(ctx, abs)
			if err != nil {
				return fmt.Sprintf("诊断失败: %v", err), nil
			}
			return MarshalDiagnostics(diags), nil
		},
	}
}

func toolDefinition() plugins.Tool {
	return plugins.Tool{
		Name:        "lsp_definition",
		Description: "跳转到标识符的定义位置。Args: {path: string, line: number, column: number}。返回定义位置。",
		Run: func(args string) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("lsp_definition: args {path, line, column} required")
			}
			if globalClient == nil {
				return "LSP 客户端未初始化", nil
			}
			abs, err := filepath.Abs(p.Path)
			if err != nil {
				return "", err
			}
			ctx := context.Background()
			locs, err := globalClient.Definition(ctx, abs, p.Line-1, p.Column-1)
			if err != nil {
				return fmt.Sprintf("跳转失败: %v", err), nil
			}
			return MarshalLocations(locs), nil
		},
	}
}

func toolReferences() plugins.Tool {
	return plugins.Tool{
		Name:        "lsp_references",
		Description: "查找标识符的所有引用。Args: {path: string, line: number, column: number}。返回引用位置列表。",
		Run: func(args string) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("lsp_references: args {path, line, column} required")
			}
			if globalClient == nil {
				return "LSP 客户端未初始化", nil
			}
			abs, err := filepath.Abs(p.Path)
			if err != nil {
				return "", err
			}
			ctx := context.Background()
			locs, err := globalClient.References(ctx, abs, p.Line-1, p.Column-1)
			if err != nil {
				return fmt.Sprintf("查找失败: %v", err), nil
			}
			return MarshalLocations(locs), nil
		},
	}
}

func toolCompletion() plugins.Tool {
	return plugins.Tool{
		Name:        "lsp_completion",
		Description: "获取代码补全建议。Args: {path: string, line: number, column: number}。返回补全列表。",
		Run: func(args string) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
				return "", fmt.Errorf("lsp_completion: args {path, line, column} required")
			}
			if globalClient == nil {
				return "LSP 客户端未初始化", nil
			}
			abs, err := filepath.Abs(p.Path)
			if err != nil {
				return "", err
			}
			ctx := context.Background()
			items, err := globalClient.Completion(ctx, abs, p.Line-1, p.Column-1)
			if err != nil {
				return fmt.Sprintf("补全失败: %v", err), nil
			}
			if len(items) == 0 {
				return "（无补全建议）", nil
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("%d 个建议:\n", len(items)))
			for _, item := range items {
				sb.WriteString(fmt.Sprintf("- %s [%s] %s\n", item.Label, kindName(item.Kind), item.Detail))
			}
			return sb.String(), nil
		},
	}
}

// All 返回所有 LSP 工具。
func All() []plugins.Tool {
	return []plugins.Tool{
		toolDiagnostics(),
		toolDefinition(),
		toolReferences(),
		toolCompletion(),
	}
}

func kindName(k int) string {
	switch k {
	case 1:
		return "text"
	case 2:
		return "method"
	case 3:
		return "function"
	case 4:
		return "constructor"
	case 5:
		return "field"
	case 6:
		return "variable"
	case 7:
		return "class"
	case 8:
		return "interface"
	case 9:
		return "module"
	case 10:
		return "property"
	case 11:
		return "unit"
	case 12:
		return "value"
	case 13:
		return "enum"
	case 14:
		return "keyword"
	case 15:
		return "snippet"
	default:
		return fmt.Sprintf("kind%d", k)
	}
}
