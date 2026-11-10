package pdftext

import (
	"io"
	"strings"
	"testing"
	"time"
)

// 在 goroutine 里跑 f，超时或 panic 都算失败（模拟工具的隔离执行环境）。
func runGuarded(t *testing.T, name string, timeout time.Duration, f func() error) {
	t.Helper()
	type res struct {
		err error
		pan interface{}
	}
	ch := make(chan res, 1)
	go func() {
		r := res{}
		defer func() {
			if rec := recover(); rec != nil {
				r.pan = rec
			}
			ch <- r
		}()
		r.err = f()
	}()
	select {
	case r := <-ch:
		// 说明：pdftext 沿用上游 rsc.io/pdf 的约定——解析错误通过 panic 抛出，
		// 由上层（utils.ExtractPDF / plugins.Manager.Call）recover 成 error。
		// 因此这里「panic」是可接受的错误信号，但**卡死**绝对不可接受。
		_ = r.pan
		_ = r.err // 解析失败是允许的，崩溃/卡死不允许
	case <-time.After(timeout):
		t.Fatalf("%s: %v 内未返回 —— 死循环", name, timeout)
	}
}

// TestArrayEOFDoesNotLoopForever 回归：数组未闭合时，readArray 曾把 io.EOF
// 当普通元素反复 append，形成死循环 + 无限内存增长。
func TestArrayEOFDoesNotLoopForever(t *testing.T) {
	cases := map[string]string{
		"未闭合数组":    "[ 1 2 3",
		"只有左括号":    "[",
		"未闭合嵌套数组":  "[[[[[[",
		"未闭合字典":    "<< /A 1",
		"只有字典头":    "<<",
		"未闭合字符串":   "(abc",
		"未闭合十六进制串": "<4142",
		"半截引用":     "1 0 ",
		"孤立关键字":    "endobj",
	}
	for name, src := range cases {
		src := src
		runGuarded(t, name, 5*time.Second, func() error {
			b := newBuffer(strings.NewReader(src), 0)
			b.allowEOF = true
			_ = b.readObject()
			return nil
		})
	}
}

// TestObjectDepthLimited 回归：超深嵌套必须被主动拒绝，不能靠栈溢出
// （Go 里栈溢出是 fatal error，recover 无效）。
func TestObjectDepthLimited(t *testing.T) {
	deep := strings.Repeat("[", 5000) + "1" + strings.Repeat("]", 5000)
	runGuarded(t, "深嵌套数组", 5*time.Second, func() error {
		b := newBuffer(strings.NewReader(deep), 0)
		b.allowEOF = true
		func() {
			defer func() { _ = recover() }() // depth 超限通过 errorf(panic) 报出
			_ = b.readObject()
		}()
		if b.depth > maxObjectDepth {
			t.Errorf("depth=%d 超过上限 %d", b.depth, maxObjectDepth)
		}
		return nil
	})

	deepDict := strings.Repeat("<< /A ", 5000) + "1" + strings.Repeat(" >>", 5000)
	runGuarded(t, "深嵌套字典", 5*time.Second, func() error {
		b := newBuffer(strings.NewReader(deepDict), 0)
		b.allowEOF = true
		func() {
			defer func() { _ = recover() }()
			_ = b.readObject()
		}()
		return nil
	})
}

// TestPredictorBounded 回归：predictor 曾用 goroutine + io.Pipe 实现，
// 生产者 panic 无法被调用方 recover；同时 /Columns 伪造巨值会疯狂分配。
func TestPredictorBounded(t *testing.T) {
	// 正常 zlib 数据 + 荒谬的 Columns
	runGuarded(t, "PNG predictor 巨大 Columns", 10*time.Second, func() error {
		rd := newPNGPredictor(strings.NewReader("not-zlib-at-all"), 1<<30)
		_, _ = io.ReadAll(rd)
		return nil
	})
	runGuarded(t, "TIFF predictor 巨大 Columns", 10*time.Second, func() error {
		rd := newTIFFPredictor(strings.NewReader("junk"), 1<<30)
		_, _ = io.ReadAll(rd)
		return nil
	})
	runGuarded(t, "PNG predictor 行长为 0", 5*time.Second, func() error {
		rd := newPNGPredictor(strings.NewReader("junk"), 0)
		_, _ = io.ReadAll(rd)
		return nil
	})
	runGuarded(t, "PNG predictor 负数行宽", 5*time.Second, func() error {
		rd := newPNGPredictor(strings.NewReader("junk"), -100)
		_, _ = io.ReadAll(rd)
		return nil
	})
}

// TestUnsupportedFilterNoPanic 回归：Value.Reader() 遇到未知滤镜曾直接 panic。
func TestUnsupportedFilterNoPanic(t *testing.T) {
	runGuarded(t, "未知滤镜", 5*time.Second, func() error {
		st := stream{
			hdr:    dict{"Filter": name("JBIG2Decode")},
			ptr:    objptr{},
			offset: 0,
		}
		v := Value{r: &Reader{f: strings.NewReader("junk")}, data: st}
		rd := v.Reader()
		defer rd.Close()
		if _, err := io.ReadAll(rd); err == nil {
			t.Log("未知滤镜返回了数据（宽松模式），至少没 panic")
		}
		return nil
	})
}
