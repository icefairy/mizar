package agent

import (
	"errors"
)

// ErrAborted 用户中止循环时返回。
var ErrAborted = errors.New("aborted by user")

// ErrTimeout 操作超时。
var ErrTimeout = errors.New("operation timed out")

// ErrMaxSteps 循环步数耗尽。
var ErrMaxSteps = errors.New("max steps exceeded")

// ============================================================================
// 快速插入机制（steer / abort，参照 pi 的 steer 命令）
//
// 场景：用户看到 Agent 循环的中间输出与自己预期有差距，可以立即发送
// 一条新的用户消息来纠正当前循环，而不用等它跑完。
//
// 实现：Agent 持有一个线程安全的"待插入消息"槽位 + abort 标志。
//   - Steer()：外部随时调用（HTTP/WS/JSON-RPC/同进程），消息存入槽位；
//     循环在【下一次 LLM 调用前】把槽位消息注入为 user 消息。
//   - 最新覆盖：同一时刻多条纠正只保留最新一条（用户最新意图优先）。
//   - Abort()：设置 abort 标志，循环在下一次检查点停止（不调用 LLM）。
//
// 缓存影响：steer 注入发生在消息序列末尾（插入点之后缓存失效，但 system
// 前缀与早期历史仍可命中）——这是用户主动纠正，可接受；文档已标注。
// ============================================================================

// steerMsg 一条待插入的纠正消息。
type steerMsg struct {
	content string
	seq     uint64
}

// Agent 新增字段（定义见 loop.go）：
//   steerMu    sync.Mutex      // 保护 steer 槽位
//   steer      *steerMsg       // 待插入消息（nil = 无）
//   steerSeq   uint64          // 序号，最新覆盖用
//   aborted    atomic.Bool     // abort 标志

// Steer 快速插入纠正消息（线程安全，非阻塞）。
// 若已有未消费的纠正消息，新消息覆盖旧消息（用户最新意图优先）。
func (a *Agent) Steer(content string) {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.steerSeq++
	a.steer = &steerMsg{content: content, seq: a.steerSeq}
}

// drainSteer 取出待插入的纠正消息（循环内部调用，LLM 调用前）。
// 返回 nil 表示无待插入消息。一次取出一条（最新的）。
func (a *Agent) drainSteer() *steerMsg {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	if a.steer == nil {
		return nil
	}
	sm := a.steer
	a.steer = nil
	return sm
}

// Abort 请求停止当前循环（线程安全）。
// 循环会在下一个检查点停止并返回当前进度，不再调用 LLM。
func (a *Agent) Abort() {
	a.aborted.Store(true)
}

// Aborted 查询是否已被请求中止。
func (a *Agent) Aborted() bool {
	return a.aborted.Load()
}

// Reset 重置会话上下文（参照 pi 的 /new 命令）：
// 清空 Initial 历史、pending steer 槽位、abort 标志；
// 保留 System / 插件 / Compactor 配置。线程安全。
func (a *Agent) Reset() {
	a.steerMu.Lock()
	a.steer = nil
	a.steerSeq++
	a.steerMu.Unlock()
	a.aborted.Store(false)
	a.Initial = nil
}

// resetAbort 清空 abort 标志（每次 Run 开始时调用）。
func (a *Agent) resetAbort() {
	a.aborted.Store(false)
}
