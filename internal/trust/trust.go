// Package trust 实现项目级资源信任（P-5 简化版，对齐 Claude Code Project Trust）。
//
// 问题：工作目录里的 AGENTS.md / .cursorrules / USER.md / .mizar/prompts/*.md
// 会被自动注入系统提示或注册为斜杠命令。克隆来的仓库可以借此插入任意指令
// （提示注入）。首次进入某个项目应先让用户确认一次。
//
// 简化版策略：
//   - 只对「非全局」的项目级资源做信任判定（~/.mizar/ 下的全局文件始终可信）；
//   - 判定结果记在 ~/.mizar/project_trust.json（path → trusted/denied + 时间）；
//   - 未记录的路径视为「待确认」：交互环境问一次，非交互环境按拒绝处理
//     （跳过项目资源并记日志，不阻塞启动）。
//   - 项目资源内容变化不重新询问（记录的是路径级信任，与 Claude Code 一致）。
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Decision 信任判定。
type Decision string

const (
	Trusted Decision = "trusted"
	Denied  Decision = "denied"
)

// Entry 一条记录。
type Entry struct {
	Decision  Decision `json:"decision"`
	UpdatedAt string   `json:"updated_at"`
}

// Store 信任记录存储。
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]Entry
}

// NewStore 打开 ~/.mizar/project_trust.json（不存在则空）。
func NewStore(configDir string) *Store {
	s := &Store{path: filepath.Join(configDir, "project_trust.json"), data: map[string]Entry{}}
	_ = s.load()
	return s
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.data)
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path 返回记录文件路径。
func (s *Store) Path() string { return s.path }

// Get 返回已记录的判定；未记录时 ok=false。
func (s *Store) Get(projectPath string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[normalize(projectPath)]
	return e, ok
}

// Set 记录判定并落盘。
func (s *Store) Set(projectPath string, d Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[normalize(projectPath)] = Entry{Decision: d, UpdatedAt: time.Now().Format(time.RFC3339)}
	return s.save()
}

// Reset 删除某项目记录（下次重新询问）。
func (s *Store) Reset(projectPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, normalize(projectPath))
	return s.save()
}

// List 返回全部记录（按路径排序），供 /trust 命令展示。
func (s *Store) List() []struct {
	Path string
	Entry
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]struct {
		Path string
		Entry
	}, 0, len(keys))
	for _, k := range keys {
		out = append(out, struct {
			Path string
			Entry
		}{k, s.data[k]})
	}
	return out
}

func normalize(p string) string {
	if p == "" {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

// ProjectResources 返回该工作目录下的项目级资源文件（不含全局 ~/.mizar）。
// 用于「是否有需要信任的东西」的判定与提示文案。
func ProjectResources(workDir string) []string {
	var out []string
	dir := workDir
	for {
		for _, name := range []string{"AGENTS.md", ".cursorrules", "USER.md"} {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				out = append(out, p)
			}
		}
		prompts := filepath.Join(dir, ".mizar", "prompts")
		if fi, err := os.Stat(prompts); err == nil && fi.IsDir() {
			out = append(out, prompts+string(filepath.Separator)+"*.md")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

// 决定函数签名：返回是否信任（交互环境由调用方问用户；nil = 非交互，按拒绝）。
// 调用方在得到 True 时应调用 store.Set(Trusted)。

// Ensure 判定某项目是否可信：
//   - 有记录 → 直接返回记录值；
//   - 无项目资源 → true（无需信任）；
//   - 待确认且 ask=nil（非交互）→ false（跳过项目资源）；
//   - 待确认且 ask!=nil → 调用 ask 询问并记录。
func Ensure(store *Store, workDir string, ask func(resources []string) bool) (bool, bool) {
	res := ProjectResources(workDir)
	if len(res) == 0 {
		return true, false // 无需项目资源
	}
	if e, ok := store.Get(workDir); ok {
		return e.Decision == Trusted, true
	}
	if ask == nil {
		return false, true // 非交互：拒绝（跳过项目资源）
	}
	trusted := ask(res)
	d := Denied
	if trusted {
		d = Trusted
	}
	_ = store.Set(workDir, d)
	return trusted, true
}

// Describe 生成 /trust 命令的展示文本。
func Describe(store *Store) string {
	list := store.List()
	if len(list) == 0 {
		return fmt.Sprintf("（无项目信任记录，文件: %s）\n首次进入含 AGENTS.md/.mizar/prompts 的项目会询问一次；/trust reset 可清除当前项目记录。", store.Path())
	}
	var sb []string
	for _, e := range list {
		sb = append(sb, fmt.Sprintf("  %s → %s（%s）", e.Path, e.Decision, e.UpdatedAt))
	}
	return fmt.Sprintf("项目信任记录（%s）：\n%s", store.Path(), joinLines(sb))
}

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
