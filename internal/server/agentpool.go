package server

import (
	"sync"

	"mizar/internal/agent"
)

// agentPool 按会话提供独立的 Agent 实例，实现真正的多会话并发。
//
// 为什么需要：Agent 实例上挂着本轮专属的可变状态 ——
//   - Initial（会话历史，每次 Run 前注入）
//   - OnLLMStream / OnToolExchange / OnCompact（流式与落盘回调，闭包捕获会话 id）
//   - steer 槽位、aborted 标志、runActive、runCancel、currentTask
//
// 这些字段一旦被两个会话同时 Run 就会互相覆盖：A 的流式增量会写进 B 的窗口，
// A 的纠正消息会被 B 的循环消费。所以并发的前提是每个会话一个实例。
//
// 而真正昂贵/全局的资源按设计共享：LLM client、插件管理器（工具注册表）、
// Jobs 后台任务注册表、GoalService 等 —— 它们本来就是进程级单例，
// 复制反而会让两边的注册表不一致。
//
// 参照 Hermes gateway 的做法（gateway/run.py 的 _agent_cache：
// OrderedDict[session_key] -> AIAgent，按会话缓存 + LRU 上限）。
type agentPool struct {
	template *agent.Agent // 组装好的主实例，作为克隆模板兼「默认会话」实例
	mu       sync.Mutex
	byConv   map[string]*agent.Agent
}

func newAgentPool(template *agent.Agent) *agentPool {
	return &agentPool{template: template, byConv: map[string]*agent.Agent{}}
}

// forConv 取该会话专属的 Agent，没有就克隆一个。
// convID 为空时返回模板实例（非 Web 入口 /v1、/rpc 无会话概念，沿用原行为）。
func (p *agentPool) forConv(convID string) *agent.Agent {
	if p == nil || p.template == nil {
		return nil
	}
	if convID == "" {
		return p.template
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if a, ok := p.byConv[convID]; ok {
		return a
	}
	a := p.template.Clone()
	p.byConv[convID] = a
	return a
}

// peek 取已存在的实例，不创建（用于查询 Running/Aborted 等）。
func (p *agentPool) peek(convID string) *agent.Agent {
	if p == nil {
		return nil
	}
	if convID == "" {
		return p.template
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byConv[convID]
}

// drop 会话删除时释放其 Agent（避免长期累积）。
func (p *agentPool) drop(convID string) {
	if p == nil || convID == "" {
		return
	}
	p.mu.Lock()
	delete(p.byConv, convID)
	p.mu.Unlock()
}

// busyConvs 返回当前真有 loop 在跑的会话 id 列表（供状态查询）。
func (p *agentPool) busyConvs() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for id, a := range p.byConv {
		if a != nil && a.Running() {
			out = append(out, id)
		}
	}
	if p.template != nil && p.template.Running() {
		out = append(out, "")
	}
	return out
}

// count 已分配的实例数（含模板不算）。测试与运维观测用。
func (p *agentPool) count() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byConv)
}
