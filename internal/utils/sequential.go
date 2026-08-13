// sequential 将并发调用的异步函数包装为串行执行。
//
// 使用场景：文件写入、状态更新等可能有竞态的操作。
// 并发调用时按入队顺序依次执行，自动保留正确的结果/错误。
//
// 用法：
//
//	safeWrite := Sequential(os.WriteFile)
//	// 并发调用 safeWrite 时，实际写入是串行的，不会互相覆盖
package utils

// Sequential 将异步函数包装为顺序执行。
// 所有入队调用按 FIFO 顺序执行，即使并发调用也不会有竞态。
func Sequential[T any](fn func(T) (string, error)) func(T) (string, error) {
	type item struct {
		arg T
		ch  chan result[T]
	}
	type result[T any] struct {
		val string
		err error
	}
	type queueItem struct {
		arg T
		ch  chan result[T]
	}
	queue := make(chan queueItem, 1000)
	done := make(chan struct{})

	// 工作协程
	go func() {
		for {
			select {
			case <-done:
				return
			case item := <-queue:
				val, err := fn(item.arg)
				item.ch <- result[T]{val: val, err: err}
			}
		}
	}()

	return func(arg T) (string, error) {
		ch := make(chan result[T], 1)
		queue <- queueItem{arg: arg, ch: ch}
		res := <-ch
		return res.val, res.err
	}
}

// CloseSequential 关闭一个 Sequential 包装器。
// 注意：当前实现中关闭操作是 no-op（简化版），
// 生产环境可改为向 done 通道发送信号。
// 保留此函数供未来扩展。
func CloseSequential(_ interface{}) {
	// no-op
}
