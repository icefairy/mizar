// Package utils 提供 PDF / DOCX / XLSX / 图片格式的内网文档解析能力。
//
// 设计原则：
//  1. 每种格式一个解析函数，返回文本内容供 Agent 消费
//  2. 文件大小预检防 OOM
//  3. 失败降级：解析失败返回原始错误，不 panic
package utils

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"mizar/internal/pdftext"
)

// 文件大小上限（对齐 mizar read 工具：10MB）
const maxFileSize = 10 * 1024 * 1024

// maxZipEntryBytes 单个 ZIP 条目解压后的大小上限（防 zip 炸弹）。
const maxZipEntryBytes = 64 << 20

// XLSX 输出上限：避免超大表格撑爆内存与模型上下文。
const (
	maxXLSXRows  = 5000
	maxXLSXBytes = 2 << 20 // 2MB
)

// ExtractPDF 从 PDF 文件提取文本内容（返回文本，页面间用 \f 分隔）。
//
// 解析策略（两条独立实现，互为后备）：
//
//  1. 外部 Zig 解析器 —— tools/pdftext-zig 编译出的独立二进制。
//     查找顺序：环境变量 MIZAR_PDF_ZIG → 可执行文件同目录的 pdftext →
//     源码树的 tools/pdftext-zig/pdftext。带 60s 超时。
//  2. 内置 Go 解析器 —— internal/pdftext（rsc.io/pdf 的修复分支）。
//
// 外部解析器缺失或失败时静默回退，行为可预测。
func ExtractPDF(path string) (string, error) {
	if bin := pdfZigBinary(); bin != "" {
		if out, err := runPDFZig(bin, path); err == nil {
			return out, nil
		}
		// 外部二进制失败：静默回退，保持行为可预测
	}
	return extractPDFGo(path)
}

// pdfZigBinary 返回可用的 Zig PDF 解析器路径；未找到时返回空串。
//
// 查找顺序：
//  1. 环境变量 MIZAR_PDF_ZIG
//  2. 可执行文件同目录下的 pdftext（发布包中随附）
//  3. 源码树中的 tools/pdftext-zig/pdftext（开发期）
func pdfZigBinary() string {
	if p := strings.TrimSpace(os.Getenv("MIZAR_PDF_ZIG")); p != "" {
		if isExecutable(p) {
			return p
		}
		return ""
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "pdftext"))
	}
	candidates = append(candidates,
		filepath.Join("tools", "pdftext-zig", "pdftext"),
	)
	for _, c := range candidates {
		if isExecutable(c) {
			return c
		}
	}
	return ""
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// runPDFZig 调用外部解析器，带超时保护。
func runPDFZig(bin, path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--no-page-break", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pdftext: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		// 空结果说明外部解析器没认出该文件，让调用方回退
		return "", fmt.Errorf("pdftext: empty output")
	}
	return text, nil
}

// extractPDFGo 使用仓库内置解析器（internal/pdftext）抽取文本。
//
// internal/pdftext 是 rsc.io/pdf v0.1.1 的内联修复分支，修复了上游对
// 现代/中文 PDF 的致命问题：
//  1. ToUnicode 只认 Dict 不认 Stream —— CID 中文字体全部解成乱码
//  2. Identity-H（双字节 CID）没有解码器 —— 输出原始字节
//  3. /Contents 为流数组时按单流读取 —— 报 "stream not present" 直接失败
//  4. 不递归 Form XObject —— 文字藏在表单对象里时整页为空
//
// 文本按 PDF 坐标系排序：Y 降序（从上到下）→ X 升序（从左到右）。
func extractPDFGo(path string) (text string, err error) {
	// rsc.io/pdf 系列解析器在遇到损坏 / 非常规结构（缺少 stream、非法 xref、
	// 加密等）时会在内部直接 panic。这里统一 recover，把 panic 转成普通错误，
	// 避免拖垮整个进程。
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("parse PDF: malformed or unsupported PDF (%v)", r)
		}
	}()

	if err := checkSize(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open PDF: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat PDF: %w", err)
	}

	r, err := pdftext.NewReader(f, fi.Size())
	if err != nil {
		return "", fmt.Errorf("parse PDF: %w", err)
	}

	numPages := r.NumPage()
	if numPages == 0 {
		return "(empty PDF or no extractable text)", nil
	}

	var pages []string
	for pageIdx := 1; pageIdx <= numPages; pageIdx++ {
		// 单页失败不应导致整份文档失败：跳过坏页继续
		if pageText, ok := extractPDFPage(r, pageIdx); ok && pageText != "" {
			pages = append(pages, pageText)
		}
	}
	result := strings.TrimRight(strings.Join(pages, "\f"), "\n")
	if result == "" {
		return "(empty PDF or no extractable text)", nil
	}
	return result, nil
}

// extractPDFPage 提取单页文本；解析异常时返回 ok=false。
func extractPDFPage(r *pdftext.Reader, pageIdx int) (string, bool) {
	ok := true
	defer func() {
		if rec := recover(); rec != nil {
			ok = false
		}
	}()

	page := r.Page(pageIdx)
	if page.V.IsNull() {
		return "", false
	}
	content := page.Content()
	if len(content.Text) == 0 {
		return "", ok
	}
	texts := content.Text
	// 按 Y 降序（PDF Y 轴从底部向上，所以大的 Y 在上）→ X 升序排序
	sort.SliceStable(texts, func(i, j int) bool {
		// 允许 1pt 容差，同一行的判定
		if abs(texts[i].Y-texts[j].Y) < 1 {
			return texts[i].X < texts[j].X
		}
		return texts[i].Y > texts[j].Y
	})

	var sb strings.Builder
	var lastY float64
	first := true
	for _, t := range texts {
		if t.S == "" {
			continue
		}
		if first {
			lastY = t.Y
			first = false
		} else if abs(t.Y-lastY) >= 1 {
			// 换行：同一行内的文字直接拼接，跨行加换行符，便于阅读
			sb.WriteString("\n")
			lastY = t.Y
		} else if abs(t.X) > 0 && sb.Len() > 0 {
			// 同一行内字符之间不加空格，避免中文被空格打散
		}
		sb.WriteString(t.S)
	}
	return sb.String(), ok
}

// ExtractDOCX 从 DOCX 文件提取文本内容。
// DOCX 本质是 ZIP，内含 word/document.xml，用 zip+xml 标准库解析，零外部依赖。
// 返回逐段落文本，段落间用 \n 分隔。
func ExtractDOCX(path string) (text string, err error) {
	// 与 PDF 同样的理由：文档解析是"处理不可信输入"，任何库都可能 panic。
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("parse DOCX: malformed document (%v)", r)
		}
	}()
	if err := checkSize(path); err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open DOCX zip: %w", err)
	}
	defer zr.Close()

	// 寻找 word/document.xml
	var documentXML io.ReadCloser
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			// zip 炸弹防护：document.xml 解压后不应超过 64MB
			if f.UncompressedSize64 > maxZipEntryBytes {
				return "", fmt.Errorf("parse DOCX: word/document.xml too large (%.1fMB)",
					float64(f.UncompressedSize64)/(1024*1024))
			}
			documentXML, err = f.Open()
			if err != nil {
				return "", fmt.Errorf("open document.xml: %w", err)
			}
			break
		}
	}
	if documentXML == nil {
		return "(empty document)", nil
	}
	defer documentXML.Close()

	var doc struct {
		XMLName xml.Name `xml:"document"`
		Body    struct {
			Paragraphs []struct {
				XMLName xml.Name `xml:"w:p"`
				Runs    []struct {
					XMLName xml.Name `xml:"w:r"`
					Texts   []struct {
						XMLName xml.Name `xml:"w:t"`
						Value   string   `xml:",chardata"`
					} `xml:"w:t"`
				} `xml:"w:r"`
			} `xml:"w:p"`
		} `xml:"w:body"`
	}
	if err := xml.NewDecoder(documentXML).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode DOCX: %w", err)
	}

	var sb strings.Builder
	for _, p := range doc.Body.Paragraphs {
		var line strings.Builder
		for _, run := range p.Runs {
			for _, t := range run.Texts {
				line.WriteString(t.Value)
			}
		}
		if line.Len() > 0 {
			sb.WriteString(line.String())
			sb.WriteString("\n")
		}
	}
	result := strings.TrimRight(sb.String(), "\n")
	if result == "" {
		return "(empty document)", nil
	}
	return result, nil
}

// ExtractXLSX 从 XLSX 文件提取文本内容。
// 返回各 sheet 名称 + 单元格数据（tab 分隔），sheet 间用 \f 分隔。
func ExtractXLSX(path string) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("parse XLSX: malformed spreadsheet (%v)", r)
		}
	}()
	if err := checkSize(path); err != nil {
		return "", err
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		return "", fmt.Errorf("open XLSX: %w", err)
	}
	defer f.Close()

	var sb strings.Builder
	first := true
	for _, sheet := range f.GetSheetList() {
		if !first {
			sb.WriteString("\f")
		}
		first = false
		sb.WriteString(fmt.Sprintf("Sheet: %s\n", sheet))
		rows, err := f.GetRows(sheet)
		if err != nil {
			sb.WriteString(fmt.Sprintf("(error reading sheet: %v)\n", err))
			continue
		}
		// 输出规模上限：超大表格（10 万行 × 百列）能直接撑爆内存与上下文，
		// 这里截断并显式告知，让模型知道还有剩余内容。
		emitted := 0
		truncated := false
		for _, row := range rows {
			if emitted >= maxXLSXRows {
				truncated = true
				break
			}
			line := strings.Join(row, "\t")
			if sb.Len()+len(line) > maxXLSXBytes {
				truncated = true
				break
			}
			sb.WriteString(line + "\n")
			emitted++
		}
		if truncated {
			sb.WriteString(fmt.Sprintf("(truncated: showing first %d rows of %d)\n", emitted, len(rows)))
		}
	}
	result := strings.TrimRight(sb.String(), "\n")
	if result == "" {
		return "(empty spreadsheet)", nil
	}
	return result, nil
}

// DescribeImage 识别图片文件并返回描述信息。
// 不提取图片内容（纯二进制），仅识别格式和尺寸。
func DescribeImage(path string) (desc string, err error) {
	defer func() {
		if r := recover(); r != nil {
			desc = ""
			err = fmt.Errorf("describe image: %v", r)
		}
	}()
	if err := checkSize(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	_, err = f.Read(make([]byte, 1))
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read image header: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat image: %w", err)
	}
	return fmt.Sprintf("Image: %s (%.1f MB, binary)", path, float64(info.Size())/1024/1024), nil
}

func checkSize(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() > maxFileSize {
		return fmt.Errorf("file too large: %.1fMB (limit %dMB)", float64(fi.Size())/(1024*1024), maxFileSize/(1024*1024))
	}
	return nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
