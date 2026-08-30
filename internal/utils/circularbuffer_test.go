package utils

import (
	"testing"
)

func TestCircularBuffer_BasicAddAndGet(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	if buf.Length() != 0 {
		t.Fatalf("expected length 0, got %d", buf.Length())
	}
	if buf.Capacity() != 5 {
		t.Fatalf("expected capacity 5, got %d", buf.Capacity())
	}

	buf.Add(10)
	buf.Add(20)
	buf.Add(30)
	if buf.Length() != 3 {
		t.Fatalf("expected length 3, got %d", buf.Length())
	}

	recent := buf.GetRecent(2)
	if len(recent) != 2 || recent[0] != 20 || recent[1] != 30 {
		t.Fatalf("expected [20 30], got %v", recent)
	}
}

func TestCircularBuffer_Overflow(t *testing.T) {
	buf := NewCircularBuffer[int](3)
	for i := 0; i < 10; i++ {
		buf.Add(i)
	}
	if buf.Length() != 3 {
		t.Fatalf("expected length 3, got %d", buf.Length())
	}
	// 最近三个是 7, 8, 9
	recent := buf.GetRecent(3)
	if len(recent) != 3 || recent[0] != 7 || recent[1] != 8 || recent[2] != 9 {
		t.Fatalf("expected [7 8 9], got %v", recent)
	}
}

func TestCircularBuffer_GetRecent_OverflowN(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	buf.Add(1)
	buf.Add(2)
	// 请求超过现有元素数量
	recent := buf.GetRecent(10)
	if len(recent) != 2 || recent[0] != 1 || recent[1] != 2 {
		t.Fatalf("expected [1 2], got %v", recent)
	}
}

func TestCircularBuffer_GetRecent_ZeroOrNegative(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	buf.Add(1)
	if r := buf.GetRecent(0); r != nil {
		t.Fatalf("expected nil for n=0, got %v", r)
	}
	if r := buf.GetRecent(-1); r != nil {
		t.Fatalf("expected nil for n=-1, got %v", r)
	}
}

func TestCircularBuffer_GetRecent_Empty(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	if r := buf.GetRecent(3); r != nil {
		t.Fatalf("expected nil for empty buffer, got %v", r)
	}
}

func TestCircularBuffer_ToArray(t *testing.T) {
	buf := NewCircularBuffer[int](3)
	buf.Add(10)
	buf.Add(20)
	buf.Add(30)
	buf.Add(40) // 淘汰 10
	arr := buf.ToArray()
	if len(arr) != 3 || arr[0] != 20 || arr[1] != 30 || arr[2] != 40 {
		t.Fatalf("expected [20 30 40], got %v", arr)
	}
}

func TestCircularBuffer_ToArray_Empty(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	if arr := buf.ToArray(); arr != nil {
		t.Fatalf("expected nil, got %v", arr)
	}
}

func TestCircularBuffer_Peek(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	if _, ok := buf.Peek(); ok {
		t.Fatal("expected false for empty buffer")
	}
	buf.Add(42)
	val, ok := buf.Peek()
	if !ok || val != 42 {
		t.Fatalf("expected (42, true), got (%v, %v)", val, ok)
	}
	// Peek 不改变长度
	if buf.Length() != 1 {
		t.Fatalf("expected length 1 after Peek, got %d", buf.Length())
	}
}

func TestCircularBuffer_Clear(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	buf.Add(1)
	buf.Add(2)
	buf.Add(3)
	buf.Clear()
	if buf.Length() != 0 {
		t.Fatalf("expected length 0 after Clear, got %d", buf.Length())
	}
	if arr := buf.ToArray(); arr != nil {
		t.Fatalf("expected nil after Clear, got %v", arr)
	}
}

func TestCircularBuffer_AddAll(t *testing.T) {
	buf := NewCircularBuffer[int](5)
	buf.AddAll([]int{1, 2, 3, 4, 5, 6, 7})
	if buf.Length() != 5 {
		t.Fatalf("expected length 5, got %d", buf.Length())
	}
	recent := buf.GetRecent(5)
	expected := []int{3, 4, 5, 6, 7}
	for i, v := range recent {
		if v != expected[i] {
			t.Fatalf("expected recent[%d]=%d, got %d", i, expected[i], v)
		}
	}
}

func TestCircularBuffer_SingleCapacity(t *testing.T) {
	buf := NewCircularBuffer[string](1)
	buf.Add("first")
	if val, _ := buf.Peek(); val != "first" {
		t.Fatalf("expected 'first', got %v", val)
	}
	buf.Add("second")
	if val, _ := buf.Peek(); val != "second" {
		t.Fatalf("expected 'second', got %v", val)
	}
	if buf.Length() != 1 {
		t.Fatalf("expected length 1, got %d", buf.Length())
	}
}

func TestCircularBuffer_WrapAround(t *testing.T) {
	buf := NewCircularBuffer[int](3)
	// 填满后继续添加，测试环形回绕
	for i := 0; i < 8; i++ {
		buf.Add(i)
	}
	arr := buf.ToArray()
	if len(arr) != 3 || arr[0] != 5 || arr[1] != 6 || arr[2] != 7 {
		t.Fatalf("expected [5 6 7], got %v", arr)
	}
}
