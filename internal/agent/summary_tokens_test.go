package agent

import "testing"

// TestEffectiveSummaryTokens 摘要输出预算随模型 output limit 放大，但有上下界。
//
// 对齐 pi 1.4.0：摘要不再受固定小上限约束，改用模型 output limit，
// 否则推理型模型把预算耗在 reasoning 上会报 "generation hit the token cap"。
func TestEffectiveSummaryTokens(t *testing.T) {
	cases := []struct {
		name       string
		modelMax   int // 模型 output limit（0=未知）
		configured int // Compactor.MaxSummaryTokens
		want       int
	}{
		{"未知模型回退到默认 1500", 0, 0, 1500},
		{"配置已知时用配置", 0, 3000, 3000},
		{"模型上限小不加预算", 2048, 0, 1500},
		{"模型上限大时取一半", 16000, 0, 8000},
		{"模型上限极大时封顶 8000", 100000, 0, 8000},
		{"配置优先于模型推导", 16000, 3000, 3000},
		{"配置超上限被截断", 0, 99999, 8000},
		{"配置负值视为未设", 0, -1, 1500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EffectiveSummaryTokens(c.modelMax, c.configured); got != c.want {
				t.Errorf("EffectiveSummaryTokens(%d, %d) = %d, want %d",
					c.modelMax, c.configured, got, c.want)
			}
		})
	}
}

// TestCompactorUsesMaxSummaryTokens 确认 Compactor 的 MaxSummaryTokens 真正生效。
func TestCompactorUsesMaxSummaryTokens(t *testing.T) {
	c := DefaultCompactor(nil)
	if got := c.SummaryTokenBudget(); got != 2000 {
		t.Errorf("默认 SummaryTokenBudget = %d, want 2000", got)
	}
	c.MaxSummaryTokens = 4096
	if got := c.SummaryTokenBudget(); got != 4096 {
		t.Errorf("设置后 SummaryTokenBudget = %d, want 4096", got)
	}
}
