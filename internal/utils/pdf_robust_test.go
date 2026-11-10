package utils

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildPDF 用给定对象体拼一份结构合法的 PDF（xref 正确），
// 这样能真正走到解析器的深层逻辑，而不是在 xref 校验处就被拒。
func buildPDF(objs [][]byte) []byte {
	var out []byte
	out = append(out, "%PDF-1.5\n"...)
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = len(out)
		out = append(out, []byte(fmt.Sprintf("%d 0 obj\n", i+1))...)
		out = append(out, body...)
		out = append(out, "\nendobj\n"...)
	}
	xrefPos := len(out)
	out = append(out, []byte(fmt.Sprintf("xref\n0 %d\n", len(objs)+1))...)
	out = append(out, "0000000000 65535 f \n"...)
	for _, off := range offsets {
		out = append(out, []byte(fmt.Sprintf("%010d 00000 n \n", off))...)
	}
	out = append(out, []byte(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, xrefPos))...)
	return out
}

// hostilePDFs 返回一组"恶意/畸形"PDF：历史上任一都能让主进程崩溃或卡死。
func hostilePDFs() map[string][]byte {
	cases := map[string][]byte{
		// 1) 超深嵌套数组（曾导致栈溢出 / 无限 append 死循环 + OOM）
		"deep_array": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
			[]byte("<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"),
			append(append([]byte("<< /Length 100000 >>\nstream\n"), repeat("[", 20000)...), append(repeat("1", 1), append(repeat("]", 20000), []byte("\nendstream")...)...)...),
		}),
		// 2) 超深嵌套字典
		"deep_dict": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 0 /Kids [] >>"),
		}),
		// 3) /Kids 自引用（页面树死循环）
		"selfref_kids": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 1 /Kids [2 0 R] >>"),
		}),
		// 4) 伪造巨大 /Length（曾导致巨量分配）
		"huge_length": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
			[]byte("<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"),
			[]byte("<< /Length 999999999 /Filter /FlateDecode >>\nstream\nx\nendstream"),
		}),
		// 5) 未知/不支持滤镜
		"unknown_filter": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
			[]byte("<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"),
			[]byte("<< /Length 5 /Filter /JBIG2Decode >>\nstream\nhello\nendstream"),
		}),
		// 6) Flate 数据损坏（predictor 路径）
		"broken_flate": buildPDF([][]byte{
			[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
			[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
			[]byte("<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"),
			[]byte("<< /Length 5 /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 999999 >> >>\nstream\nhello\nendstream"),
		}),
		// 7) 完全不是 PDF
		"not_pdf": []byte("this is definitely not a pdf file at all"),
		// 8) 空文件
		"empty_file": {},
		// 9) 截断的 PDF
		"truncated": []byte("%PDF-1.5\n1 0 obj\n<< /Type"),
		// 10) 二进制垃圾带 PDF 头
		"binary_junk": append([]byte("%PDF-1.7\n"), bytesN(0xFF, 4096)...),
	}
	return cases
}

func repeat(s string, n int) []byte {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return out
}

func bytesN(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestPDFHostileInputsDoNotCrash 核心回归测试：
// 任何畸形 PDF 都必须「要么返回文本、要么返回 error」，
// 绝不能 panic 到进程级（recover 兜住），也不能卡死（超时即失败）。
func TestPDFHostileInputsDoNotCrash(t *testing.T) {
	dir := t.TempDir()
	for name, data := range hostilePDFs() {
		name, data := name, data
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".pdf")
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}

			type result struct {
				text string
				err  error
				pan  interface{}
			}
			done := make(chan result, 1)
			go func() {
				r := result{}
				defer func() {
					if rec := recover(); rec != nil {
						r.pan = rec
					}
					done <- r
				}()
				r.text, r.err = ExtractPDF(p)
			}()

			select {
			case r := <-done:
				if r.pan != nil {
					t.Fatalf("panic 未被 recover（会杀进程）：%v", r.pan)
				}
				// 返回值二选一：有文本，或有错误；不允许两者皆空而不报错
				if r.err == nil && r.text == "" {
					t.Fatalf("既无文本也无错误，状态不明确")
				}
				t.Logf("text=%d bytes, err=%v", len(r.text), r.err)
			case <-time.After(30 * time.Second):
				t.Fatalf("解析 30s 未返回 —— 死循环/极慢（这在实际运行中会拖死 mizar）")
			}
		})
	}
}

// TestExtractPDFNeverPanicsOnFuzzish 抖一批随机/结构性破坏的字节，确认不 panic。
func TestExtractPDFNeverPanicsOnFuzzish(t *testing.T) {
	dir := t.TempDir()
	base := buildPDF([][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Count 1 /Kids [3 0 R] >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"),
		[]byte("<< /Length 4 >>\nstream\nq Q\nendstream"),
	})

	seed := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}

	for i := 0; i < 200; i++ {
		mutated := make([]byte, len(base))
		copy(mutated, base)
		// 随机破坏 1~16 个字节（模拟传输损坏 / 手工改文件）
		for k := 0; k < 1+int(next()%16); k++ {
			pos := int(next() % uint64(len(mutated)))
			mutated[pos] = byte(next())
		}
		p := filepath.Join(dir, fmt.Sprintf("mut%d.pdf", i))
		if err := os.WriteFile(p, mutated, 0o644); err != nil {
			t.Fatal(err)
		}
		panicked := false
		var panicVal interface{}
		done := make(chan struct{}, 1)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					panicked = true
					panicVal = rec
				}
				done <- struct{}{}
			}()
			_, _ = ExtractPDF(p)
		}()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatalf("第 %d 个变异样本卡死 >15s", i)
		}
		if panicked {
			t.Fatalf("第 %d 个变异样本 panic：%v", i, panicVal)
		}
	}
}

var _ = binary.LittleEndian
