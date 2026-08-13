// boundedasync 提供基于 context.Context 的异步工具函数。
//
// 用途：
//   - RaceAbort：在 ctx 取消/超时与协程完成之间做竞争，哪个先到返回哪个
//   - MapWithConcurrency：并发执行 mapper，可设并发度、可取消、可 fail-fast
//   - ThrowIfAborted：同步检查点，ctx 已取消则抛错误
package utils

import (
	"context"
	"sync"
)

// ErrAborted 操作被取消时的标准错误。
var ErrAborted = context.Canceled

// RaceAbort 在 ctx 取消/超时与协程完成之间做竞争。
//
// 用法：
//
//	result, err := RaceAbort(ctx, doLongWork())
//
// 如果 ctx 先取消，返回 (zero, ctx.Err())。
// 如果协程先完成，返回协程的结果。
func RaceAbort[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	ch := make(chan result[T], 1)
	go func() {
		val, err := fn()
		ch <- result[T]{val: val, err: err}
	}()

	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case res := <-ch:
		return res.val, res.err
	}
}

// MapWithConcurrency 并发执行 mapper。
// 参数：
//   - ctx：上下文（取消时停止新任务，等待已启动的任务完成）
//   - items：输入项
//   - concurrency：最大并发数（<=0 表示不限制）
//   - mapper：每项的映射函数
//
// 返回：结果切片（按输入顺序排列）+ 错误
func MapWithConcurrency[T any, R any](ctx context.Context, items []T, concurrency int, mapper func(ctx context.Context, item T) (R, error)) ([]R, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = len(items)
	}

	results := make([]R, len(items))
	sem := make(chan struct{}, concurrency)
	errCh := make(chan error, 1)
	type indexedResult struct {
		idx int
		val R
	}
	resultsCh := make(chan indexedResult, len(items))

	var wg sync.WaitGroup

	for i, item := range items {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, v T) {
			defer wg.Done()
			defer func() { <-sem }()
			val, err := mapper(ctx, v)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			resultsCh <- indexedResult{idx: idx, val: val}
		}(i, item)
	}

	wg.Wait()
	close(resultsCh)

	for r := range resultsCh {
		results[r.idx] = r.val
	}

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	return results, nil
}

// ThrowIfAborted 同步检查点：如果 ctx 已取消则返回错误。
// 用于长时间同步操作的中间检查点。
func ThrowIfAborted(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// result 内部通道类型。
type result[T any] struct {
	val T
	err error
}
