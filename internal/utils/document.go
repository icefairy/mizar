// Package utils 提供 PDF / DOCX / XLSX / 图片格式的内网文档解析能力。
//
// 设计原则：
//  1. 每种格式一个解析函数，返回文本内容供 Agent 消费
//  2. 文件大小预检防 OOM
//  3. 失败降级：解析失败返回原始错误，不 panic
package utils

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
	"rsc.io/pdf"
)

// 文件大小上限（对齐 mizar read 工具：10MB）
const maxFileSize = 10 * 1024 * 1024

// ExtractPDF 从 PDF 文件提取文本内容。
// 返回逐页文本，页面间用 \f 分隔。
// 文本按 PDF 坐标系排序：Y 降序（从上到下）→ X 升序（从左到右）。
func ExtractPDF(path string) (string, error) {
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

	r, err := pdf.NewReader(f, fi.Size())
	if err != nil {
		return "", fmt.Errorf("parse PDF: %w", err)
	}

	numPages := r.NumPage()
	if numPages == 0 {
		return "(empty PDF or no extractable text)", nil
	}

	var sb strings.Builder
	for pageIdx := 1; pageIdx <= numPages; pageIdx++ {
		if pageIdx > 1 {
			sb.WriteString("\f")
		}
		page := r.Page(pageIdx)
		if page.V.IsNull() {
			continue
		}
		content := page.Content()
		if len(content.Text) == 0 {
			continue
		}
		// 按 Y 降序（PDF Y 轴从底部向上，所以大的 Y 在上）→ X 升序排序
		texts := content.Text
		sort.Slice(texts, func(i, j int) bool {
			// 允许 1pt 容差，同一行的判定
			if abs(texts[i].Y-texts[j].Y) < 1 {
				return texts[i].X < texts[j].X
			}
			return texts[i].Y > texts[j].Y
		})
		for _, t := range texts {
			if t.S != "" {
				sb.WriteString(t.S)
			}
		}
		sb.WriteString("\n")
	}

	result := strings.TrimRight(sb.String(), "\n")
	if result == "" {
		return "(empty PDF or no extractable text)", nil
	}
	return result, nil
}

// ExtractDOCX 从 DOCX 文件提取文本内容。
// DOCX 本质是 ZIP，内含 word/document.xml，用 zip+xml 标准库解析，零外部依赖。
// 返回逐段落文本，段落间用 \n 分隔。
func ExtractDOCX(path string) (string, error) {
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
func ExtractXLSX(path string) (string, error) {
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
		for _, row := range rows {
			var cells []string
			for _, cell := range row {
				cells = append(cells, cell)
			}
			sb.WriteString(strings.Join(cells, "\t") + "\n")
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
func DescribeImage(path string) (string, error) {
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
