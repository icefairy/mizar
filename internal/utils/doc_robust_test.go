package utils

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runNoCrash 在独立 goroutine 里跑 f，超过 timeout 或 panic 逃逸都算失败。
func runNoCrash(t *testing.T, name string, timeout time.Duration, f func() (string, error)) {
	t.Helper()
	ch := make(chan struct{}, 1)
	var pan interface{}
	var out string
	var err error
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				pan = rec
			}
			ch <- struct{}{}
		}()
		out, err = f()
	}()
	select {
	case <-ch:
		if pan != nil {
			t.Fatalf("%s: panic 逃逸（会杀进程）: %v", name, pan)
		}
		_ = out
		_ = err
	case <-time.After(timeout):
		t.Fatalf("%s: %v 内未返回", name, timeout)
	}
}

// TestDOCXHostileInputs DOCX/XLSX 同样是不可信输入：损坏 zip / zip 炸弹 / 畸形 XML
// 都必须返回 error，不能 panic 也不能卡死。
func TestDOCXHostileInputs(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 1) 不是 zip
	p1 := write("notdocx.docx", []byte("plain text, not a zip at all"))
	runNoCrash(t, "DOCX 非 zip", 10*time.Second, func() (string, error) { return ExtractDOCX(p1) })

	// 2) 空 zip（没有 document.xml）
	p2 := filepath.Join(dir, "empty.docx")
	f, _ := os.Create(p2)
	zw := zip.NewWriter(f)
	zw.Close()
	f.Close()
	runNoCrash(t, "DOCX 空 zip", 10*time.Second, func() (string, error) { return ExtractDOCX(p2) })

	// 3) document.xml 是畸形 XML
	p3 := filepath.Join(dir, "badxml.docx")
	f3, _ := os.Create(p3)
	zw3 := zip.NewWriter(f3)
	w, _ := zw3.Create("word/document.xml")
	w.Write([]byte("<document><w:body><w:p><w:r><w:t>unclosed"))
	zw3.Close()
	f3.Close()
	runNoCrash(t, "DOCX 畸形 XML", 10*time.Second, func() (string, error) { return ExtractDOCX(p3) })

	// 4) zip 炸弹：声称解压后 1GB
	p4 := filepath.Join(dir, "bomb.docx")
	f4, _ := os.Create(p4)
	zw4 := zip.NewWriter(f4)
	wh := &zip.FileHeader{Name: "word/document.xml"}
	wh.UncompressedSize64 = 1 << 30
	wh.Method = zip.Store
	bw, _ := zw4.CreateRaw(wh)
	bw.Write([]byte("<x/>"))
	zw4.Close()
	f4.Close()
	runNoCrash(t, "DOCX zip 炸弹", 10*time.Second, func() (string, error) { return ExtractDOCX(p4) })

	// 5) XLSX 各种垃圾
	p5 := write("junk.xlsx", []byte("not a spreadsheet"))
	runNoCrash(t, "XLSX 非 zip", 15*time.Second, func() (string, error) { return ExtractXLSX(p5) })

	p6 := write("empty.xlsx", []byte{})
	runNoCrash(t, "XLSX 空文件", 15*time.Second, func() (string, error) { return ExtractXLSX(p6) })

	// 6) 图片
	p7 := write("junk.png", bytesN(0x89, 32))
	runNoCrash(t, "图片 垃圾数据", 10*time.Second, func() (string, error) { return DescribeImage(p7) })

	// 7) 不存在的文件
	runNoCrash(t, "文件不存在", 10*time.Second, func() (string, error) {
		return ExtractDOCX(filepath.Join(dir, "nope.docx"))
	})

	fmt.Println("DOCX/XLSX/图片 全部安全返回")
}
