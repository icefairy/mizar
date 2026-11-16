// Package memory 实现结构化长期记忆（C-7，对齐 CodeBuddy 四分类记忆设计）。
//
// 与 memory 插件（HMEM/SQLite 语义检索）的分工：
//   - 插件 mem_* 工具 = 海量记忆的**语义检索**（向量/关键词），按需查询；
//   - 本包 = 小而精的**结构化事实清单**，四类固定分类，每次会话自动注入系统提示，
//     模型无需检索即可看到（对齐 CodeBuddy 的 MEMORY.md 索引）。
//
// 存储布局（纯文本，人类可读可手改）：
//
//	~/.mizar/memory/MEMORY.md
//	# MEMORY
//	## user
//	- 用户偏好结论先行
//	## feedback
//	## project
//	## reference
//
// 设计约束：
//   - 单文件 MEMORY.md 即索引即正文（一个事实一行），避免索引/内容双写不一致；
//   - 分类固定四类，未知分类归 reference（防提示注入/乱写导致文件结构崩坏）；
//   - 注入有上限（条目数 + 字节数），防止记忆无限膨胀吃掉上下文；
//   - 读写加锁，Web 模式多会话并发安全。
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 四类固定分类（对齐 CodeBuddy）。
const (
	CategoryUser      = "user"      // 用户身份/偏好/长期约定
	CategoryFeedback  = "feedback"  // 用户对工作方式的反馈与纠正
	CategoryProject   = "project"   // 项目级事实：架构约定、命令、坑
	CategoryReference = "reference" // 参考资料索引：文档、链接、外部系统
)

// Categories 返回固定分类顺序（渲染与文件章节按此顺序）。
func Categories() []string {
	return []string{CategoryUser, CategoryFeedback, CategoryProject, CategoryReference}
}

// NormalizeCategory 归一化分类：未知值一律归 reference。
func NormalizeCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	for _, k := range Categories() {
		if c == k {
			return c
		}
	}
	return CategoryReference
}

// 注入上限：防止记忆无限膨胀挤占上下文。
const (
	MaxInjectEntries = 200  // 最多注入条目数
	MaxInjectBytes   = 8000 // 最多注入字节数（截断时尾部提示）
)

const (
	fileHeader  = "# MEMORY\n"
	indexName   = "MEMORY.md"
	entryPrefix = "- "
)

// Store 结构化记忆存储。
type Store struct {
	mu   sync.Mutex
	dir  string
	path string
	// projectOnly 项目记忆模式：文件只承载 project 分类，注入/渲染只含 project 段。
	// 全局存储（false）的注入则**跳过** project 分类——project 条目按工作路径
	// 分文件（见 NewStorePath），跨项目注入会让模型误认当前项目
	// （2026-11-16 实测：WebUI 新会话因全局记忆里全是 hmem-go 条目而跑去探索 hmem-go）。
	projectOnly bool
	// entries 按分类保序（追加顺序）。
	entries map[string][]string
}

// NewStore 创建存储（dir 通常是 ~/.mizar/memory，不存在则懒创建）。
func NewStore(dir string) *Store {
	return &Store{
		dir:     dir,
		path:    filepath.Join(dir, indexName),
		entries: make(map[string][]string),
	}
}

// NewStorePath 用指定的 MEMORY.md 文件路径创建存储。
//
// 项目记忆用：project 分类按工作路径分文件，落在
// ~/.mizar/sessions/<路径key>/MEMORY.md（与该项目的会话同目录，复用
// session.EncodePathKey 的路径编码）。projectOnly=true 时该文件只承载
// project 分类，注入时只渲染 project 段。
func NewStorePath(path string, projectOnly bool) *Store {
	return &Store{
		dir:        filepath.Dir(path),
		path:       path,
		projectOnly: projectOnly,
		entries:    make(map[string][]string),
	}
}

// Path 返回 MEMORY.md 路径。
func (s *Store) Path() string { return s.path }

// Load 读取 MEMORY.md。文件不存在视为空记忆（不报错），
// 缺失的分类补空切片；未知章节内容归 reference。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = map[string][]string{}
			for _, c := range Categories() {
				s.entries[c] = nil
			}
			return nil
		}
		return err
	}
	s.entries = parse(string(b))
	return nil
}

// parse 解析 MEMORY.md：以 "## 分类" 分节，" - 条目" 为一行事实。
// 未识别的章节标题下的条目归 reference；节外内容忽略。
func parse(content string) map[string][]string {
	entries := map[string][]string{}
	for _, c := range Categories() {
		entries[c] = nil
	}
	cur := ""
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "" || strings.HasPrefix(t, "# ") && !strings.HasPrefix(t, "## "):
			continue // 空行与一级标题
		case strings.HasPrefix(t, "## "):
			cur = NormalizeCategory(strings.TrimSpace(strings.TrimPrefix(t, "## ")))
		case strings.HasPrefix(t, entryPrefix):
			if cur == "" {
				continue // 章节外的散条目忽略
			}
			item := strings.TrimSpace(strings.TrimPrefix(t, entryPrefix))
			if item != "" {
				entries[cur] = append(entries[cur], item)
			}
		}
	}
	return entries
}

// render 生成 MEMORY.md 全文。projectOnly 模式只写 project 段。
func (s *Store) renderLocked() string {
	var sb strings.Builder
	sb.WriteString(fileHeader)
	for _, c := range Categories() {
		if s.projectOnly && c != CategoryProject {
			continue
		}
		sb.WriteString("\n## " + c + "\n")
		for _, item := range s.entries[c] {
			sb.WriteString(entryPrefix + item + "\n")
		}
	}
	return sb.String()
}

// Add 追加一条事实到分类，并立即落盘。重复条目（同分类同文本）忽略。
func (s *Store) Add(category, text string) error {
	cat := NormalizeCategory(category)
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("memory: 记忆内容为空")
	}
	// 单行化：换行折叠为空格，保证「一行一个事实」的文件结构不被破坏
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }), " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	for _, item := range s.entries[cat] {
		if item == text {
			return nil // 幂等
		}
	}
	s.entries[cat] = append(s.entries[cat], text)
	return s.saveLocked()
}

// Remove 删除分类下第 idx 条（1-based，与 list 输出对齐）。返回是否删除成功。
func (s *Store) Remove(category string, idx int) (bool, error) {
	cat := NormalizeCategory(category)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return false, err
	}
	list := s.entries[cat]
	if idx < 1 || idx > len(list) {
		return false, fmt.Errorf("memory: %s 分类无第 %d 条（共 %d 条）", cat, idx, len(list))
	}
	s.entries[cat] = append(list[:idx-1], list[idx:]...)
	return true, s.saveLocked()
}

// Clear 清空某分类（category 为空则清空全部分类）。
func (s *Store) Clear(category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	if strings.TrimSpace(category) == "" {
		for _, c := range Categories() {
			s.entries[c] = nil
		}
	} else {
		s.entries[NormalizeCategory(category)] = nil
	}
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("memory: mkdir %s: %w", s.dir, err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.renderLocked()), 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", tmp, err)
	}
	return os.Rename(tmp, s.path)
}

// List 返回某分类的条目（category 为空则返回全部分类）。
func (s *Store) List(category string) (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	if strings.TrimSpace(category) == "" {
		for _, c := range Categories() {
			out[c] = append([]string(nil), s.entries[c]...)
		}
		return out, nil
	}
	cat := NormalizeCategory(category)
	out[cat] = append([]string(nil), s.entries[cat]...)
	return out, nil
}

// Count 返回总条目数。
func (s *Store) Count() (int, error) {
	m, err := s.List("")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n, nil
}

// Render 生成注入系统提示的文本段；无可注入条目时返回空字符串。
//
// 注入有上限：最多 MaxInjectEntries 条、MaxInjectBytes 字节，超限截断并给提示。
//
// project 分类的注入规则（2026-11-16）：
//   - projectOnly 存储（项目记忆，按路径分文件）：只渲染 project 段；
//   - 全局存储：**跳过** project 段——project 条目属于某个具体项目，
//     全量注入会让模型误认当前项目。旧数据仍在文件里，可用 memory 工具查看。
func (s *Store) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return ""
	}
	total := 0
	for _, c := range Categories() {
		if s.projectOnly != (c == CategoryProject) {
			continue // 全局存储跳过 project；项目存储跳过其余三类
		}
		total += len(s.entries[c])
	}
	if total == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 结构化记忆（MEMORY.md）\n")
	if s.projectOnly {
		sb.WriteString("以下为当前项目的跨会话记忆（按项目路径分文件），请据此调整行为，不必再次询问用户；" +
			"可用 memory 工具补充/修正（文件: " + s.path + "）。\n")
	} else {
		sb.WriteString("以下为跨会话保留的长期事实，按类别组织。请据此调整行为，不必再次询问用户；" +
			"可用 memory 工具补充/修正（文件: " + s.path + "）。\n")
	}
	used := 0
	emitted := 0
	truncated := false
	for _, c := range Categories() {
		if s.projectOnly != (c == CategoryProject) {
			continue
		}
		list := s.entries[c]
		if len(list) == 0 {
			continue
		}
		sb.WriteString("### " + c + "\n")
		for _, item := range list {
			if emitted >= MaxInjectEntries || used+len(item) > MaxInjectBytes {
				truncated = true
				break
			}
			sb.WriteString("- " + item + "\n")
			used += len(item)
			emitted++
		}
		if truncated {
			break
		}
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("（记忆过多已截断，共 %d 条；完整内容见 %s）\n", total, s.path))
	}
	return sb.String()
}
