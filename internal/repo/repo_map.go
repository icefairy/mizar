// Package repo 提供仓库级别的代码索引工具。
//
// RepoMap 生成项目文件级别地图，供 Agent 快速了解代码结构。
package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RepoMap 文件级仓库地图。
type RepoMap struct {
	Root    string
	Files   []FileInfo
	Dirs    []string
	LangMap map[string][]string // 扩展名 → 文件列表
}

// FileInfo 单个文件的信息。
type FileInfo struct {
	Path     string
	RelPath  string
	Ext      string
	Size     int64
	Language string
}

// Build 从 root 目录递归构建 RepoMap。
func Build(root string) (*RepoMap, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	rm := &RepoMap{
		Root:    root,
		Files:   make([]FileInfo, 0),
		Dirs:    make([]string, 0),
		LangMap: make(map[string][]string),
	}

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的文件
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		if info.IsDir() {
			rm.Dirs = append(rm.Dirs, rel)
			return nil
		}

		// 跳过隐藏文件和构建产物
		name := info.Name()
		if strings.HasPrefix(name, ".") {
			return nil
		}

		finfo := FileInfo{
			Path:     path,
			RelPath:  rel,
			Ext:      strings.ToLower(filepath.Ext(path)),
			Size:     info.Size(),
			Language: langFromExt(filepath.Ext(path)),
		}
		rm.Files = append(rm.Files, finfo)
		rm.LangMap[finfo.Ext] = append(rm.LangMap[finfo.Ext], rel)
		return nil
	})

	sort.Strings(rm.Dirs)
	sort.Slice(rm.Files, func(i, j int) bool { return rm.Files[i].RelPath < rm.Files[j].RelPath })
	for ext := range rm.LangMap {
		sort.Strings(rm.LangMap[ext])
	}
	return rm, err
}

// Render 生成可读的仓库地图文本。
func (rm *RepoMap) Render(maxFiles int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Repo Map (%s)\n\n", rm.Root))
	sb.WriteString(fmt.Sprintf("目录: %d 个\n", len(rm.Dirs)))
	sb.WriteString(fmt.Sprintf("文件: %d 个\n\n", len(rm.Files)))

	// 按目录分组展示
	dirFiles := make(map[string][]FileInfo)
	for _, f := range rm.Files {
		dir := filepath.Dir(f.RelPath)
		dirFiles[dir] = append(dirFiles[dir], f)
	}

	dirs := make([]string, 0, len(dirFiles))
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	totalShown := 0
	for _, dir := range dirs {
		if totalShown >= maxFiles {
			break
		}
		files := dirFiles[dir]
		sb.WriteString(fmt.Sprintf("### %s/\n", dir))
		for i, f := range files {
			if totalShown >= maxFiles {
				remaining := len(files) - i
				if remaining > 0 {
					sb.WriteString(fmt.Sprintf("  ... (%d more files)\n", remaining))
				}
				break
			}
			size := fmt.Sprintf("%d bytes", f.Size)
			if f.Size > 1024*1024 {
				size = fmt.Sprintf("%.1f MB", float64(f.Size)/1024/1024)
			} else if f.Size > 1024 {
				size = fmt.Sprintf("%d KB", f.Size/1024)
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", f.RelPath, size))
			totalShown++
		}
		sb.WriteString("\n")
	}

	if totalShown >= maxFiles {
		sb.WriteString(fmt.Sprintf("... (%d total files shown %d/%d)\n", len(rm.Files), totalShown, len(rm.Files)))
	}

	return sb.String()
}

// langFromExt 从扩展名推断编程语言。
func langFromExt(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js", ".jsx":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".cpp", ".cc", ".cxx", ".c++", ".c":
		return "C/C++"
	case ".rb":
		return "Ruby"
	case ".php":
		return "PHP"
	case ".sh":
		return "Shell"
	case ".md":
		return "Markdown"
	case ".yaml", ".yml":
		return "YAML"
	case ".json":
		return "JSON"
	case ".toml":
		return "TOML"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".sql":
		return "SQL"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}
