package skills

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatsIncrAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stats.json")
	s := LoadStats(p)
	s.Incr("grep")
	s.Incr("grep")
	s.Incr("bash")
	if s.Uses["grep"] != 2 || s.Uses["bash"] != 1 {
		t.Fatalf("uses wrong: %v", s.Uses)
	}
	// 重新加载验证持久化
	s2 := LoadStats(p)
	if s2.Uses["grep"] != 2 || s2.Uses["bash"] != 1 {
		t.Fatalf("persist failed: %v", s2.Uses)
	}
}

func TestStatsLeastUsed(t *testing.T) {
	s := LoadStats(filepath.Join(t.TempDir(), "stats.json"))
	s.Incr("hot")  // 2
	s.Incr("hot")  // 2
	s.Incr("cold") // 1
	s.Ensure("icier") // 从未使用，但已注册
	least := s.LeastUsed(2)
	if len(least) != 2 || least[0] != "icier" || least[1] != "cold" {
		t.Fatalf("least used wrong: %v", least)
	}
	// 禁用后不计入
	s.SetDisabled("cold", true)
	least = s.LeastUsed(2)
	if len(least) != 2 || least[0] != "icier" || least[1] != "hot" {
		t.Fatalf("disabled should be excluded: %v", least)
	}
	if !s.IsDisabled("cold") {
		t.Fatal("cold should be disabled")
	}
}

func TestStatsReport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stats.json")
	s := LoadStats(p)
	s.Incr("hot")
	s.Incr("hot")
	s.Incr("hot")
	s.Incr("lukewarm")
	s.Incr("lukewarm")
	s.Incr("cold")
	rep := s.Report(2)
	if !strings.Contains(rep, "技能使用周报") {
		t.Fatalf("report missing title: %s", rep)
	}
	if !strings.Contains(rep, "hot") || !strings.Contains(rep, "cold") {
		t.Fatalf("report missing skills: %s", rep)
	}
	if !strings.Contains(rep, "使用量最低的 2 个") {
		t.Fatalf("report missing least-used section: %s", rep)
	}
	// cold 是最低，应在建议列表
	if !strings.Contains(rep, "- cold") {
		t.Fatalf("report should suggest cold: %s", rep)
	}
}

func TestStatsDueReport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stats.json")
	s := LoadStats(p)
	now := time.Now()
	if !s.DueReport(now) {
		t.Fatal("first report should be due")
	}
	s.MarkReported(now)
	if s.DueReport(now) {
		t.Fatal("not due right after report")
	}
	// 6 天后仍不 due
	if s.DueReport(now.Add(6 * 24 * time.Hour)) {
		t.Fatal("should not be due after 6 days")
	}
	// 7 天后 due
	if !s.DueReport(now.Add(7*24*time.Hour + time.Second)) {
		t.Fatal("should be due after 7 days")
	}
}

func TestDefaultStatsConfig(t *testing.T) {
	c := DefaultStatsConfig()
	if !c.Enabled || c.TopN != 10 {
		t.Fatalf("default config wrong: %+v", c)
	}
}
