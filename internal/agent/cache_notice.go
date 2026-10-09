package agent

import "fmt"

// 显著 prompt 缓存未命中提示（对齐 pi 0.80.4 的 showCacheMissNotices）。
//
// 背景：缓存命中率下跌时用户只看到"变慢了、变贵了"，却不知道原因
// （system prompt 变更、工具列表变化、切点变化都会让前缀失效）。
// 这里在单次请求命中率显著下跌时给一行提示，帮助定位。
//
// 设计要点：
//   - 需要基线：首次观测不提示（没有对比就没有"下跌"）
//   - 小 prompt 不提示：token 太少时比率噪音大（如 100 token 全 miss 无意义）
//   - 每次下跌只提示一次，恢复后再次下跌可再次提示（避免刷屏）
type CacheMissDetector struct {
	lastHitRate float64 // 上一次命中率（0~1），-1 表示无基线
	notified    bool    // 当前这轮下跌是否已提示
	haveBase    bool
}

const (
	// cacheMissMinPromptTokens 低于此 token 数的请求不参与判定（噪音大）。
	cacheMissMinPromptTokens = 5000
	// cacheMissDropThreshold 命中率下跌超过此幅度视为"显著"。
	cacheMissDropThreshold = 0.3
	// cacheMissLowRate 命中率低于此值才算问题（高位小幅波动不提示）。
	cacheMissLowRate = 0.5
)

// NewCacheMissDetector 创建检测器。
func NewCacheMissDetector() *CacheMissDetector {
	return &CacheMissDetector{}
}

// Observe 观测一次请求用量；命中率显著下跌时返回提示文本，否则返回空串。
func (d *CacheMissDetector) Observe(u *Usage) string {
	if d == nil || u == nil || u.PromptTokens < cacheMissMinPromptTokens {
		return ""
	}
	rate := float64(u.CachedTokens) / float64(u.PromptTokens)

	if !d.haveBase {
		d.haveBase = true
		d.lastHitRate = rate
		return ""
	}

	prev := d.lastHitRate
	d.lastHitRate = rate

	dropped := prev-rate >= cacheMissDropThreshold && rate < cacheMissLowRate
	if !dropped {
		// 命中率恢复：允许下次下跌再提示
		if rate >= cacheMissLowRate {
			d.notified = false
		}
		return ""
	}
	if d.notified {
		return ""
	}
	d.notified = true
	return fmt.Sprintf("⚠️ 缓存命中率从 %.0f%% 跌到 %.0f%%（本次 prompt %d tokens）——上下文前缀已失效，本轮更慢更贵",
		prev*100, rate*100, u.PromptTokens)
}
