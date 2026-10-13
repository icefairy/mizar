package agent

import (
	"strings"
	"testing"
)

// 低于阈值的 prompt 太小，比率噪音大，不应产生提示。
func TestCacheMissSmallPromptNoNotice(t *testing.T) {
	d := NewCacheMissDetector()
	d.Observe(&Usage{PromptTokens: 3000, CachedTokens: 3000}) // 100% 命中，建立基线
	got := d.Observe(&Usage{PromptTokens: 100, CachedTokens: 0})
	if got != "" {
		t.Fatalf("小 prompt 不应提示，得到 %q", got)
	}
}

// 命中率从高位跌到低位 → 提示一次。
func TestCacheMissDetectsDrop(t *testing.T) {
	d := NewCacheMissDetector()
	d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 19000}) // 95%
	got := d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 1000})
	if got == "" {
		t.Fatal("命中率从 95% 跌到 5% 应产生提示")
	}
	if !strings.Contains(got, "缓存") {
		t.Fatalf("提示应说明是缓存问题: %q", got)
	}
}

// 同一轮下跌只提示一次，不刷屏。
func TestCacheMissNoticesOncePerDrop(t *testing.T) {
	d := NewCacheMissDetector()
	d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 19000})
	if d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 1000}) == "" {
		t.Fatal("首次下跌应提示")
	}
	for i := 0; i < 3; i++ {
		if got := d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 1000}); got != "" {
			t.Fatalf("重复下跌不应再提示，第 %d 次得到 %q", i, got)
		}
	}
}

// 命中率恢复后再次下跌，应能再次提示。
func TestCacheMissReFiresAfterRecovery(t *testing.T) {
	d := NewCacheMissDetector()
	d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 19000})
	d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 1000}) // 提示
	d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 19000}) // 恢复
	if got := d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 1000}); got == "" {
		t.Fatal("恢复后再次下跌应再次提示")
	}
}

// 命中率一直很高不应提示。
func TestCacheMissStableHighNoNotice(t *testing.T) {
	d := NewCacheMissDetector()
	for i := 0; i < 5; i++ {
		if got := d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 19000}); got != "" {
			t.Fatalf("稳定高命中不应提示，得到 %q", got)
		}
	}
}

// 首次请求（无基线）不应提示：没有对比就没有"下跌"。
func TestCacheMissFirstObservationNoNotice(t *testing.T) {
	d := NewCacheMissDetector()
	if got := d.Observe(&Usage{PromptTokens: 20000, CachedTokens: 0}); got != "" {
		t.Fatalf("首次观测无基线，不应提示，得到 %q", got)
	}
}

// 服务端不报缓存信息（CachedTokens=0 且无基线）时不应误报；
// 一旦基线建立后，普通下跌按阈值处理。
func TestCacheMissNilUsageSafe(t *testing.T) {
	d := NewCacheMissDetector()
	if got := d.Observe(nil); got != "" {
		t.Fatalf("nil usage 不应提示，得到 %q", got)
	}
}
