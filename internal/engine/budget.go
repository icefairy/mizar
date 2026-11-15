// 宿主函数调用预算（P3，2026-11-15，源自 hermes ctx.fork_session 的护栏思想）。
//
// 背景：hermes 给插件的「回合后工作」加了硬护栏——工具白名单、总输入预算、
// 迭代上限。Mizar 的插件在 goja VM 里跑，已经有 30s 超时兜底死循环，
// 但**「一次 hook 回调里写一万个文件」**这类放大是超时挡不住的
// （每次调用都很快，只是次数多）。
//
// 所以这里给宿主函数加一层调用预算：
//   - 引擎级默认预算（大，防止异常插件失控）
//   - hook 回调开始时可以收紧（插件自己声明 max_calls）
//   - 超预算的调用直接返回错误，而不是静默失败
//
// 与 hermes 的差异：我们没有「工具白名单」（mizar 插件本来就不能调用其他工具，
// 只能调宿主函数），所以这里只管**次数**，不管「能调什么」。
package engine

import (
	"fmt"
	"sync/atomic"
)

// hostBudget 宿主函数调用计数器。
type hostBudget struct {
	limit int64
	used  atomic.Int64
}

// newHostBudget 创建预算（limit <= 0 表示不限）。
func newHostBudget(limit int) *hostBudget {
	return &hostBudget{limit: int64(limit)}
}

// check 记一次调用，超预算返回错误。
func (b *hostBudget) check(name string) error {
	if b == nil {
		return nil
	}
	n := b.used.Add(1)
	if b.limit > 0 && n > b.limit {
		return fmt.Errorf("宿主函数 %q 调用超预算：本次已调用 %d 次，上限 %d（插件 hook 里的批量操作请分多次、或用一次批量 API）", name, n-1, b.limit)
	}
	return nil
}

// reset 重置计数（每次 hook 回调开始）。
func (b *hostBudget) reset() {
	if b != nil {
		b.used.Store(0)
	}
}

// SetHostBudget 设置本引擎的宿主函数调用预算（limit<=0 = 不限）。
// main.go 可按 config 调整；插件 hook 亦可通过 hook_budget() 收紧当前回调。
func (e *Engine) SetHostBudget(limit int) {
	e.hostBudget = newHostBudget(limit)
}
