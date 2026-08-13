// 技能使用统计与周报。
//
// 每个技能被调用一次计数 +1，持久化到 JSON 文件。每周（距上次周报 ≥7 天）
// 汇总使用情况：列出使用量最低的 N 个技能（默认 10），供用户决定是否禁用。
// 统计默认开启，可在全局配置关闭（skill_stats_enabled=false）。
package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Stats 技能使用统计。
type Stats struct {
	mu sync.Mutex

	// Uses 技能名 → 累计使用次数。
	Uses map[string]int `json:"uses"`

	// Disabled 被禁用的技能（周报询问用户后记录）。
	Disabled map[string]bool `json:"disabled"`

	// LastReport 上次周报时间（Unix 秒，0=从未）。
	LastReport int64 `json:"last_report"`

	path string // 持久化文件路径（不序列化）
}

// StatsConfig 周报相关配置。
type StatsConfig struct {
	Enabled bool // 统计总开关（默认开）
	TopN    int  // 周报列出最低活跃技能数（默认 10）
}

// DefaultStatsConfig 默认：开启 + Top10。
func DefaultStatsConfig() StatsConfig { return StatsConfig{Enabled: true, TopN: 10} }

// LoadStats 加载统计；文件不存在时返回空统计。
func LoadStats(path string) *Stats {
	s := &Stats{Uses: map[string]int{}, Disabled: map[string]bool{}, path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, s)
	if s.Uses == nil {
		s.Uses = map[string]int{}
	}
	if s.Disabled == nil {
		s.Disabled = map[string]bool{}
	}
	return s
}

// Save 持久化（原子写）。
func (s *Stats) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Ensure 注册技能（不存在则记 0 次，让从未使用的技能也进入统计）。
func (s *Stats) Ensure(name string) {
	s.mu.Lock()
	if _, ok := s.Uses[name]; !ok {
		s.Uses[name] = 0
	}
	s.mu.Unlock()
}

// Incr 技能使用计数 +1（保存失败忽略，不影响主流程）。
func (s *Stats) Incr(name string) {
	s.mu.Lock()
	s.Uses[name]++
	s.mu.Unlock()
	_ = s.Save()
}

// IsDisabled 技能是否被禁用。
func (s *Stats) IsDisabled(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Disabled[name]
}

// SetDisabled 设置/取消禁用。
func (s *Stats) SetDisabled(name string, disabled bool) {
	s.mu.Lock()
	if disabled {
		s.Disabled[name] = true
	} else {
		delete(s.Disabled, name)
	}
	s.mu.Unlock()
	_ = s.Save()
}

// DisabledNames 返回已禁用技能名列表。
func (s *Stats) DisabledNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.Disabled {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LeastUsed 返回使用量最低的 n 个技能（按使用次数升序，0 次优先）。
// 只统计有记录且未禁用的技能。
func (s *Stats) LeastUsed(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	type kv struct {
		name string
		uses int
	}
	var all []kv
	for name, uses := range s.Uses {
		if s.Disabled[name] {
			continue
		}
		all = append(all, kv{name, uses})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].uses != all[j].uses {
			return all[i].uses < all[j].uses
		}
		return all[i].name < all[j].name
	})
	if n > len(all) {
		n = len(all)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[i].name)
	}
	return out
}

// DueReport 是否该发周报了（距上次 ≥7 天）。
func (s *Stats) DueReport(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LastReport == 0 {
		return true
	}
	return now.Unix()-s.LastReport >= 7*24*3600
}

// MarkReported 记录周报已发。
func (s *Stats) MarkReported(now time.Time) {
	s.mu.Lock()
	s.LastReport = now.Unix()
	s.mu.Unlock()
	_ = s.Save()
}

// Report 生成周报文本：总览 + 最低活跃 TopN 建议禁用。
func (s *Stats) Report(topN int) string {
	s.mu.Lock()
	uses := make(map[string]int, len(s.Uses))
	for k, v := range s.Uses {
		uses[k] = v
	}
	disabled := make(map[string]bool, len(s.Disabled))
	for k, v := range s.Disabled {
		disabled[k] = v
	}
	s.mu.Unlock()

	var total int
	for _, v := range uses {
		total += v
	}

	var sb strings.Builder
	sb.WriteString("📊 技能使用周报\n")
	sb.WriteString(fmt.Sprintf("统计周期: 近 7 天  累计使用: %d 次  技能数: %d\n\n", total, len(uses)))

	// 使用量排名（降序）
	type kv struct {
		name string
		uses int
	}
	var ranked []kv
	for name, u := range uses {
		if disabled[name] {
			continue
		}
		ranked = append(ranked, kv{name, u})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].uses > ranked[j].uses })
	if len(ranked) == 0 {
		sb.WriteString("本周无技能使用记录。\n")
		return sb.String()
	}

	sb.WriteString("📈 使用排行 (Top 10):\n")
	for i := 0; i < len(ranked) && i < 10; i++ {
		sb.WriteString(fmt.Sprintf("  %2d. %s: %d 次\n", i+1, ranked[i].name, ranked[i].uses))
	}

	// 最低活跃
	least := s.LeastUsed(topN)
	if len(least) > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠️  使用量最低的 %d 个技能（考虑禁用）:\n", len(least)))
		for _, name := range least {
			sb.WriteString(fmt.Sprintf("  - %s (%d 次)\n", name, uses[name]))
		}
		sb.WriteString("回复“禁用 <技能名>”可将其禁用，回复“忽略”跳过。\n")
	}
	if len(disabled) > 0 {
		sb.WriteString("\n🔕 已禁用: " + strings.Join(s.DisabledNames(), ", ") + "\n")
	}
	return sb.String()
}
