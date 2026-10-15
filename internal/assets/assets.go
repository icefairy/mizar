// Package assets 内嵌随二进制发布的默认插件与技能，首次运行时释放到用户目录。
//
// 设计原则：
//   - **只补缺，不覆盖**：目标文件已存在（用户可能改过）就跳过，绝不覆盖用户内容。
//   - **可关闭**：调用方可通过 CLI 开关（--no-assets）跳过释放。
//   - **只放通用资产**：不含密钥、不绑定特定主机/平台的插件与技能才可内嵌。
package assets

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:extensions all:skills
var FS embed.FS

// ReleaseResult 释放结果统计。
type ReleaseResult struct {
	Written []string // 新释放的文件（相对路径）
	Skipped []string // 已存在而跳过
	Errors  []error
}

// Release 把内嵌资产释放到 extDir / skillDir。
// 已存在的文件一律跳过（保护用户修改）。返回统计。
func Release(extDir, skillDir string) ReleaseResult {
	var res ReleaseResult
	releaseTree(&res, "extensions", extDir)
	releaseTree(&res, "skills", skillDir)
	return res
}

// releaseTree 释放 embed 里 sub 子树到 dst 目录。
func releaseTree(res *ReleaseResult, sub, dst string) {
	err := fs.WalkDir(FS, sub, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		rel, _ := filepath.Rel(sub, path)
		target := filepath.Join(dst, rel)
		// 只补缺：目标已存在则跳过（用户可能改过）
		if _, statErr := os.Stat(target); statErr == nil {
			res.Skipped = append(res.Skipped, sub+"/"+rel)
			return nil
		}
		data, readErr := FS.ReadFile(path)
		if readErr != nil {
			res.Errors = append(res.Errors, readErr)
			return nil
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			res.Errors = append(res.Errors, mkErr)
			return nil
		}
		if wErr := os.WriteFile(target, data, 0o644); wErr != nil {
			res.Errors = append(res.Errors, wErr)
			return nil
		}
		res.Written = append(res.Written, sub+"/"+rel)
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
	}
}
