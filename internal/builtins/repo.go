package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mizar/internal/plugins"
	"mizar/internal/repo"
)

// toolRepoMap 仓库地图工具：扫描项目文件结构，生成可读的 repo 地图。
func toolRepoMap() plugins.Tool {
	return plugins.Tool{
		Name:        "repo_map",
		Description: "扫描项目文件结构，生成仓库地图。Args: {path?: string, maxFiles?: number}。默认 path 为当前目录，maxFiles 默认 50。",
		Run: func(args string) (string, error) {
			var p struct {
				Path     string `json:"path"`
				MaxFiles int    `json:"maxFiles"`
			}
			if err := json.Unmarshal([]byte(args), &p); err != nil {
				// 允许纯字符串路径
				p.Path = args
			}
			if p.Path == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("repo_map: getwd: %w", err)
				}
				p.Path = cwd
			}
			abs, err := filepath.Abs(p.Path)
			if err != nil {
				return "", fmt.Errorf("repo_map: abs: %w", err)
			}
			stat, err := os.Stat(abs)
			if err != nil {
				return "", fmt.Errorf("repo_map: %s: %v", p.Path, err)
			}
			if !stat.IsDir() {
				return "", fmt.Errorf("repo_map: %s is not a directory", p.Path)
			}
			if p.MaxFiles <= 0 {
				p.MaxFiles = 50
			}
			rm, err := repo.Build(abs)
			if err != nil {
				return "", fmt.Errorf("repo_map: build: %w", err)
			}
			return rm.Render(p.MaxFiles), nil
		},
	}
}
