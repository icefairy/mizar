package server

import (
	"testing"

	"mizar/internal/agent"
	"mizar/internal/plugins"
)

func TestAgentPoolDrop(t *testing.T) {
	pm := plugins.NewManager("", nil)
	base := agent.New(nil, pm)
	p := newAgentPool(base)

	a1 := p.forConv("conv-a")
	a2 := p.forConv("conv-b")
	if a1 == nil || a2 == nil {
		t.Fatal("forConv 应返回实例")
	}
	if a1 == a2 {
		t.Fatal("不同会话应拿到不同实例")
	}
	if got := p.count(); got != 2 {
		t.Errorf("count 应 2，got %d", got)
	}
	if p.peek("conv-a") != a1 {
		t.Error("peek 应返回同一实例")
	}

	// 审计修复回归：删除会话时必须释放实例（此前 drop 零调用 → 只增不减）
	p.drop("conv-a")
	if p.peek("conv-a") != nil {
		t.Error("drop 后不应再有该会话实例")
	}
	if got := p.count(); got != 1 {
		t.Errorf("drop 后 count 应 1，got %d", got)
	}
	// 再取会新建
	if p.forConv("conv-a") == a1 {
		t.Error("drop 后应新建实例，而非复用旧的")
	}
}

func TestAgentPoolEmptyConvUsesTemplate(t *testing.T) {
	pm := plugins.NewManager("", nil)
	base := agent.New(nil, pm)
	p := newAgentPool(base)
	if p.forConv("") != base {
		t.Error("空 convID 应返回模板实例")
	}
}
