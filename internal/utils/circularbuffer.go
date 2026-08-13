// CircularBuffer 固定容量环形缓冲，自动淘汰最旧的元素。
//
// 用途：
//   - 滚动日志窗口
//   - 性能指标保留最近 N 个
//   - Agent 工具调用历史（防内存无限增长）
//
// 用法：
//
//	buf := NewCircularBuffer[int](100)
//	for i := 0; i < 200; i++ {
//	    buf.Add(i)
//	}
//	fmt.Println(buf.Length())         // 100
//	fmt.Println(buf.GetRecent(3))    // [197, 198, 199]
package utils

// CircularBuffer 泛型环形缓冲。
type CircularBuffer[T any] struct {
	buf  []T
	head int
	size int
}

// NewCircularBuffer 创建固定容量缓冲。
func NewCircularBuffer[T any](capacity int) *CircularBuffer[T] {
	return &CircularBuffer[T]{
		buf: make([]T, capacity),
	}
}

// Add 添加一个元素。容量满时淘汰最旧元素。
func (b *CircularBuffer[T]) Add(item T) {
	b.buf[b.head] = item
	b.head = (b.head + 1) % len(b.buf)
	if b.size < len(b.buf) {
		b.size++
	}
}

// AddAll 批量添加。
func (b *CircularBuffer[T]) AddAll(items []T) {
	for _, item := range items {
		b.Add(item)
	}
}

// GetRecent 获取最近 N 个元素，按从旧到新的顺序返回。
func (b *CircularBuffer[T]) GetRecent(n int) []T {
	if b.size == 0 {
		return nil
	}
	if n <= 0 {
		return nil
	}
	available := n
	if available > b.size {
		available = b.size
	}
	start := b.head - available
	if start < 0 {
		start += len(b.buf)
	}
	result := make([]T, available)
	for i := 0; i < available; i++ {
		result[i] = b.buf[(start+i)%len(b.buf)]
	}
	return result
}

// ToArray 返回所有元素，按从旧到新的顺序。
func (b *CircularBuffer[T]) ToArray() []T {
	if b.size == 0 {
		return nil
	}
	result := make([]T, b.size)
	start := b.head
	if b.size < len(b.buf) {
		start = 0
	}
	for i := 0; i < b.size; i++ {
		result[i] = b.buf[(start+i)%len(b.buf)]
	}
	return result
}

// Length 返回当前元素数量。
func (b *CircularBuffer[T]) Length() int {
	return b.size
}

// Capacity 返回缓冲容量。
func (b *CircularBuffer[T]) Capacity() int {
	return len(b.buf)
}

// Clear 清空缓冲。
func (b *CircularBuffer[T]) Clear() {
	b.head = 0
	b.size = 0
}

// Peek 查看最新元素，不修改缓冲。
func (b *CircularBuffer[T]) Peek() (T, bool) {
	if b.size == 0 {
		var zero T
		return zero, false
	}
	idx := b.head - 1
	if idx < 0 {
		idx += len(b.buf)
	}
	return b.buf[idx], true
}