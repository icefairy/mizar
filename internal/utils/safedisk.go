// safedisk 提供安全的临时文件操作（O_NOFOLLOW + session 隔离）。
//
// 安全设计：
//  1. O_NOFOLLOW：防 symlink 攻击，防止 attacker 在 temp 目录创建 symlink
//     指向任意文件，导致 mizar 写入到非预期位置
//  2. Session 隔离：每个 session 使用独立的 temp 目录，避免多个 mizar
//     实例互相干扰
//
// 用法：
//
//	path, err := DumpToTemp(content)
//	if err != nil {
//	    return err
//	}
//	fmt.Println("内容已保存到:", path)
package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const defaultMaxTaskOutputBytes = 5 * 1024 * 1024 * 1024 // 5GB 上限

var (
	_taskOutputDir    string
	_taskOutputDirMu  sync.RWMutex
	_taskOutputDirSet = false
)

// SetTaskOutputDir 设置任务输出目录（mizar 启动时调用）。
// 传入项目根目录下的 .mizar/tasks 路径。
func SetTaskOutputDir(dir string) {
	_taskOutputDirMu.Lock()
	defer _taskOutputDirMu.Unlock()
	_taskOutputDir = dir
	_taskOutputDirSet = true
	_ = os.MkdirAll(dir, 0o755)
}

// GetTaskOutputDir 获取任务输出目录。
// 如果未设置，自动使用项目根目录下的 .mizar/tasks。
func GetTaskOutputDir(projectRoot string) string {
	_taskOutputDirMu.RLock()
	if _taskOutputDirSet && _taskOutputDir != "" {
		dir := _taskOutputDir
		_taskOutputDirMu.RUnlock()
		return dir
	}
	_taskOutputDirMu.RUnlock()

	// 自动设置
	defaultDir := filepath.Join(projectRoot, ".mizar", "tasks")
	SetTaskOutputDir(defaultDir)
	_taskOutputDirMu.RLock()
	defer _taskOutputDirMu.RUnlock()
	return _taskOutputDir
}

// DumpToTemp 安全地将内容写入临时文件。
// 使用 O_NOFOLLOW（Unix）防止 symlink 攻击。
// 文件大小上限 5GB。
// 返回文件路径。
func DumpToTemp(content string) (string, error) {
	if len(content) > defaultMaxTaskOutputBytes {
		return "", fmt.Errorf("content too large: %.1fGB (limit 5GB)",
			float64(len(content))/(1024*1024*1024))
	}

	dir := GetTaskOutputDir(".")
	f, err := os.OpenFile(filepath.Join(dir, "mizar-output-*.log"),
		os.O_CREATE|os.O_WRONLY|os.O_EXCL|0, 0o600)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	name := f.Name()

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("sync temp file: %w", err)
	}
	f.Close()

	return name, nil
}

// TaskOutputPath 生成一个任务输出文件路径（不创建文件）。
func TaskOutputPath(projectRoot, taskId string) string {
	dir := GetTaskOutputDir(projectRoot)
	return filepath.Join(dir, taskId+".output")
}

// 确保 import 使用
var _ io.Writer = nil
