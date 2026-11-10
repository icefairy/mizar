// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pdftext

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf16"
)

// 页面树遍历的资源上限（防畸形 /Kids 自引用导致无限递归或爆栈）。
const (
	maxPageTreeDepth   = 64
	maxPageTreeNodes   = 200000
	maxPageKidsPerNode = 10000
	// maxFormDepth 限制 Form XObject 嵌套层数（正常 PDF 极少超过 4 层）。
	maxFormDepth = 16
	// 单条内容流 / ToUnicode CMap 的读取上限（防 /Length 伪造导致巨量分配）。
	maxContentStreamBytes = 32 << 20 // 32MB
	maxCMapBytes          = 4 << 20  // 4MB
)

// A Page represent a single page in a PDF file.
// The methods interpret a Page dictionary stored in V.
type Page struct {
	V Value
}

// Page returns the page for the given page number.
// Page numbers are indexed starting at 1, not 0.
// If the page is not found, Page returns a Page with p.V.IsNull().
func (r *Reader) Page(num int) Page {
	num-- // now 0-indexed
	page := r.Trailer().Key("Root").Key("Pages")
	depth := 0
Search:
	for page.Key("Type").Name() == "Pages" || page.Key("Kids").Len() > 0 {
		depth++
		if depth > maxPageTreeDepth {
			return Page{}
		}
		if page.Key("Type").Name() != "Pages" {
			break
		}
		count := int(page.Key("Count").Int64())
		if count > 0 && num >= count {
			return Page{}
		}
		kids := page.Key("Kids")
		for i := 0; i < kids.Len(); i++ {
			kid := kids.Index(i)
			if kid.Key("Type").Name() == "Pages" || kid.Key("Kids").Len() > 0 {
				c := int(kid.Key("Count").Int64())
				if c <= 0 {
					c = countKids(kid)
				}
				if num < c {
					page = kid
					continue Search
				}
				num -= c
				continue
			}
			if num == 0 {
				return Page{kid}
			}
			num--
		}
		break
	}
	return Page{}
}

// countKids 递归统计页面树节点下的页面数。
//
// 深度与节点数都有上限：畸形 PDF 的 /Kids 可以自引用或极深嵌套，
// 无限递归在 Go 里是 fatal error（栈溢出，recover 无效），会杀进程。
func countKids(node Value) int {
	return countKidsDepth(node, 0, new(int))
}

func countKidsDepth(node Value, depth int, visited *int) int {
	if depth > maxPageTreeDepth || *visited > maxPageTreeNodes {
		return 0
	}
	*visited++
	if node.Key("Type").Name() == "Page" {
		return 1
	}
	kids := node.Key("Kids")
	if kids.Len() == 0 {
		return 0
	}
	n := 0
	limit := kids.Len()
	if limit > maxPageKidsPerNode {
		limit = maxPageKidsPerNode
	}
	for i := 0; i < limit; i++ {
		n += countKidsDepth(kids.Index(i), depth+1, visited)
	}
	return n
}

// NumPage returns the number of pages in the PDF file.
func (r *Reader) NumPage() int {
	pages := r.Trailer().Key("Root").Key("Pages")
	if n := int(pages.Key("Count").Int64()); n > 0 {
		return n
	}
	return countKids(pages)
}

func (p Page) findInherited(key string) Value {
	for v := p.V; !v.IsNull(); v = v.Key("Parent") {
		if r := v.Key(key); !r.IsNull() {
			return r
		}
	}
	return Value{}
}

/*
func (p Page) MediaBox() Value {
	return p.findInherited("MediaBox")
}

func (p Page) CropBox() Value {
	return p.findInherited("CropBox")
}
*/

// Resources returns the resources dictionary associated with the page.
func (p Page) Resources() Value {
	return p.findInherited("Resources")
}

// Fonts returns a list of the fonts associated with the page.
func (p Page) Fonts() []string {
	return p.Resources().Key("Font").Keys()
}

// Font returns the font with the given name associated with the page.
func (p Page) Font(name string) Font {
	return Font{p.Resources().Key("Font").Key(name)}
}

// A Font represent a font in a PDF file.
// The methods interpret a Font dictionary stored in V.
type Font struct {
	V Value
}

// BaseFont returns the font's name (BaseFont property).
func (f Font) BaseFont() string {
	return f.V.Key("BaseFont").Name()
}

// FirstChar returns the code point of the first character in the font.
func (f Font) FirstChar() int {
	return int(f.V.Key("FirstChar").Int64())
}

// LastChar returns the code point of the last character in the font.
func (f Font) LastChar() int {
	return int(f.V.Key("LastChar").Int64())
}

// Widths returns the widths of the glyphs in the font.
// In a well-formed PDF, len(f.Widths()) == f.LastChar()+1 - f.FirstChar().
func (f Font) Widths() []float64 {
	x := f.V.Key("Widths")
	var out []float64
	for i := 0; i < x.Len(); i++ {
		out = append(out, x.Index(i).Float64())
	}
	return out
}

// Width returns the width of the given code point (1/1000 text space units).
// Type0（CID）字体的字宽来自 DescendantFonts 的 /W 与 /DW，不是 /Widths。
func (f Font) Width(code int) float64 {
	if f.isType0() {
		return f.cidWidth(code)
	}
	first := f.FirstChar()
	last := f.LastChar()
	if code < first || last < code {
		return 0
	}
	return f.V.Key("Widths").Index(code - first).Float64()
}

// cidWidth 从 CIDFont 的 /W 数组取字宽，缺省用 /DW（默认 1000）。
func (f Font) cidWidth(cid int) float64 {
	desc := f.V.Key("DescendantFonts")
	if desc.Kind() != Array || desc.Len() == 0 {
		return 1000
	}
	d := desc.Index(0)
	dw := 1000.0
	if v := d.Key("DW"); v.Kind() != Null {
		dw = v.Float64()
	}
	w := d.Key("W")
	if w.Kind() != Array {
		return dw
	}
	// /W 语法：[c [w1 w2 ...]] 或 [cFirst cLast w]
	for i := 0; i+1 < w.Len(); {
		first := int(w.Index(i).Int64())
		next := w.Index(i + 1)
		if next.Kind() == Array {
			if cid >= first && cid-first < next.Len() {
				return next.Index(cid - first).Float64()
			}
			i += 2
			continue
		}
		if i+2 >= w.Len() {
			break
		}
		last := int(next.Int64())
		width := w.Index(i + 2).Float64()
		if cid >= first && cid <= last {
			return width
		}
		i += 3
	}
	return dw
}

// codeByteLen 返回该字体每个字符码所占的字节数（CID 字体为 2，简单字体为 1）。
func (f Font) codeByteLen() int {
	return bytesPerCode(f.Encoder())
}

// Encoder returns the encoding between font code point sequences and UTF-8.
//
// 修复要点：
//  1. ToUnicode 在 PDF 中是 *流对象*（Kind()==Stream），原实现只判断 Dict，
//     导致所有带 ToUnicode 的 CID 字体（中文简历里全是 Identity-H + ToUnicode）
//     退化成 pdfDocEncoding，解出来全是乱码。
//  2. Identity-H/V（Type0/CID 字体）没有 ToUnicode 时按双字节大端（UCS-2）解码，
//     而不是原样返回原始字节。
func (f Font) Encoder() TextEncoding {
	// 优先使用 ToUnicode CMap（Dict 与 Stream 两种形态都要支持）
	toUnicode := f.V.Key("ToUnicode")
	if k := toUnicode.Kind(); k == Stream || k == Dict {
		if m := readCmap(toUnicode); m != nil && len(m.space) > 0 {
			return m
		}
	}

	enc := f.V.Key("Encoding")
	switch enc.Kind() {
	case Name:
		switch enc.Name() {
		case "WinAnsiEncoding":
			return &byteEncoder{&winAnsiEncoding}
		case "MacRomanEncoding":
			return &byteEncoder{&macRomanEncoding}
		case "Identity-H", "Identity-V":
			// 双字节 CID 编码，无 ToUnicode 时按 UCS-2 尽力解码
			return &ucs2Encoder{}
		default:
			// 未知命名编码：Type0 字体按双字节处理，其余按单字节
			if f.isType0() {
				return &ucs2Encoder{}
			}
			return &byteEncoder{&pdfDocEncoding}
		}
	case Dict:
		return &dictEncoder{enc.Key("Differences")}
	case Null:
		if f.isType0() {
			return &ucs2Encoder{}
		}
		return &byteEncoder{&pdfDocEncoding}
	default:
		if f.isType0() {
			return &ucs2Encoder{}
		}
		return &byteEncoder{&pdfDocEncoding}
	}
}

// isType0 判断是否为 Type0（CID）复合字体，这类字体的字符串是双字节编码。
func (f Font) isType0() bool {
	return f.V.Key("Subtype").Name() == "Type0"
}

// ucs2Encoder 把双字节大端（UCS-2/BE，含 UTF-16 代理对）解码为 UTF-8。
type ucs2Encoder struct{}

func (e *ucs2Encoder) Decode(raw string) string {
	r := make([]rune, 0, len(raw)/2+1)
	for i := 0; i < len(raw); i += 2 {
		if i+1 >= len(raw) {
			break
		}
		u := rune(raw[i])<<8 | rune(raw[i+1])
		if utf16.IsSurrogate(u) && i+3 < len(raw) {
			lo := rune(raw[i+2])<<8 | rune(raw[i+3])
			r = append(r, utf16.DecodeRune(u, lo))
			i += 2
			continue
		}
		r = append(r, u)
	}
	return string(r)
}

type dictEncoder struct {
	v Value
}

func (e *dictEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		ch := rune(raw[i])
		n := -1
		for j := 0; j < e.v.Len(); j++ {
			x := e.v.Index(j)
			if x.Kind() == Integer {
				n = int(x.Int64())
				continue
			}
			if x.Kind() == Name {
				if int(raw[i]) == n {
					r := nameToRune[x.Name()]
					if r != 0 {
						ch = r
						break
					}
				}
				n++
			}
		}
		r = append(r, ch)
	}
	return string(r)
}

// A TextEncoding represents a mapping between
// font code points and UTF-8 text.
type TextEncoding interface {
	// Decode returns the UTF-8 text corresponding to
	// the sequence of code points in raw.
	Decode(raw string) (text string)
}

// bytesPerCode 返回该编码下每个字符码占用的字节数。
func bytesPerCode(enc TextEncoding) int {
	switch e := enc.(type) {
	case *ucs2Encoder:
		return 2
	case *cmap:
		if len(e.codeLens) > 0 {
			return e.codeLens[0]
		}
		return 2
	case *byteEncoder, *dictEncoder, *nopEncoder:
		return 1
	}
	return 1
}

type nopEncoder struct {
}

func (e *nopEncoder) Decode(raw string) (text string) {
	return raw
}

type byteEncoder struct {
	table *[256]rune
}

func (e *byteEncoder) Decode(raw string) (text string) {
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, e.table[raw[i]])
	}
	return string(r)
}

type cmap struct {
	// space 按字节长度分组的 codespace 区间
	space [4][][2]string
	// m 为 "原始字节码" -> UTF-8 文本
	m map[string]string
	// codeLens 为该 CMap 可能使用的编码字节长度（升序）
	codeLens []int
}

func (m *cmap) addCode(src, dstRunes string) {
	if m.m == nil {
		m.m = make(map[string]string, 1024)
	}
	// 目标为 U+0000（或空串）视为“无映射”，不写入
	if dstRunes == "" || isAllNUL(dstRunes) {
		return
	}
	// 后出现的非空映射优先覆盖（部分 PDF 会先用 <0000> 占位，再补充真实映射）
	if old, ok := m.m[src]; ok && !isAllNUL(old) && old != "" {
		return
	}
	m.m[src] = dstRunes
}

// isAllNUL 判断字符串是否全为 NUL 字符。
func isAllNUL(s string) bool {
	for _, r := range s {
		if r != 0 {
			return false
		}
	}
	return true
}

func (m *cmap) finalize() {
	m.codeLens = m.codeLens[:0]
	for n := 1; n <= 4; n++ {
		found := false
		for k := range m.m {
			if len(k) == n {
				found = true
				break
			}
		}
		if !found {
			for _, sp := range m.space[n-1] {
				if len(sp[0]) == n {
					found = true
					break
				}
			}
		}
		if found {
			m.codeLens = append(m.codeLens, n)
		}
	}
	if len(m.codeLens) == 0 {
		m.codeLens = []int{2}
	}
}

// Decode 把字体原始字节串按 ToUnicode CMap 映射为 UTF-8 文本。
func (m *cmap) Decode(raw string) (text string) {
	if m == nil || len(m.m) == 0 {
		return (&ucs2Encoder{}).Decode(raw)
	}
	var sb strings.Builder
	fallbackLen := m.codeLens[0]
	i := 0
	for i < len(raw) {
		matched := false
		for _, n := range m.codeLens {
			if i+n > len(raw) {
				continue
			}
			if s, ok := m.m[raw[i:i+n]]; ok {
				sb.WriteString(s)
				i += n
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		// 没有 bf 映射：按编码长度兜底解码，避免整篇乱码
		if fallbackLen == 2 && i+1 < len(raw) {
			u := rune(raw[i])<<8 | rune(raw[i+1])
			if u != 0 {
				sb.WriteRune(u)
			}
			i += 2
		} else {
			i += fallbackLen
		}
	}
	return sb.String()
}

type bfrange struct {
	lo  string
	hi  string
	dst Value
}

// readCmap 解析 ToUnicode CMap 流，构建 code -> Unicode 映射。
//
// 相对原实现的修复：
//   - ToUnicode 通常是 Stream（原实现只认 Dict，导致中文 PDF 全乱码）
//   - 支持 beginbfchar/endbfchar（iText/WPS 导出的 PDF 大量只用 bfchar）
//   - 支持 bfrange 的数组目标 [<a> <b> ...]，并按码位增量展开
//   - 去掉向 stdout 打印的调试噪音
func readCmap(toUnicode Value) *cmap {
	if toUnicode.Kind() != Stream && toUnicode.Kind() != Dict {
		return nil
	}
	var data []byte
	func() {
		defer func() { recover() }()
		data, _ = io.ReadAll(io.LimitReader(toUnicode.Reader(), maxCMapBytes))
	}()
	if len(data) == 0 {
		return nil
	}
	return parseCMapData(data)
}

// parseCMapData 从 ToUnicode CMap 原始字节解析映射表。
func parseCMapData(data []byte) *cmap {
	m := &cmap{}
	forEachBlock(data, "begincodespacerange", "endcodespacerange", func(body []byte) {
		toks := hexTokens(body)
		for i := 0; i+1 < len(toks); i += 2 {
			lo, hi := toks[i], toks[i+1]
			if len(lo) == 0 || len(lo) != len(hi) || len(lo) > 4 {
				continue
			}
			m.space[len(lo)-1] = append(m.space[len(lo)-1], [2]string{lo, hi})
		}
	})

	// bfchar：src -> dst 一对一
	forEachBlock(data, "beginbfchar", "endbfchar", func(body []byte) {
		toks := hexTokens(body)
		for i := 0; i+1 < len(toks); i += 2 {
			src, dst := toks[i], toks[i+1]
			if len(src) == 0 || len(dst) == 0 {
				continue
			}
			m.addCode(src, hexToUTF8(dst))
		}
	})

	// bfrange：lo..hi -> dst 递增（dst 可能是单个或数组）
	forEachBlock(data, "beginbfrange", "endbfrange", func(body []byte) {
		for _, it := range parseBfRangeBody(body) {
			expandBfRange(m, it)
		}
	})

	if len(m.m) == 0 {
		return nil
	}
	m.finalize()
	return m
}

type bfRangeItem struct {
	lo, hi string
	dsts   []string
}

// parseBfRangeBody 解析 bfrange 段：<lo> <hi> <dst> 或 <lo> <hi> [<a> <b> ...]
func parseBfRangeBody(body []byte) []bfRangeItem {
	var out []bfRangeItem
	var cur bfRangeItem
	flush := func() {
		if cur.lo != "" && cur.hi != "" && len(cur.dsts) > 0 {
			out = append(out, cur)
		}
		cur = bfRangeItem{}
	}
	inArray := false
	i := 0
	for i < len(body) {
		c := body[i]
		switch {
		case c == '[':
			inArray = true
			i++
		case c == ']':
			inArray = false
			if len(cur.dsts) > 0 || cur.lo != "" {
				flush()
			}
			i++
		case c == '<':
			j := i + 1
			for j < len(body) && body[j] != '>' {
				j++
			}
			tok := decodeHex(body[i+1 : j])
			if j < len(body) {
				i = j + 1
			} else {
				i = j
			}
			if inArray {
				cur.dsts = append(cur.dsts, tok)
				continue
			}
			switch {
			case cur.lo == "":
				cur.lo = tok
			case cur.hi == "":
				cur.hi = tok
			default:
				cur.dsts = append(cur.dsts, tok)
				flush()
			}
		default:
			i++
		}
	}
	flush()
	return out
}

func expandBfRange(m *cmap, it bfRangeItem) {
	lo := codeValue(it.lo)
	hi := codeValue(it.hi)
	if hi < lo {
		return
	}
	n := int(hi - lo)
	if n < 0 || n > 65535 {
		// 区间过大视为异常数据，避免内存爆炸
		if len(it.dsts) > 0 {
			m.addCode(it.lo, hexToUTF8(it.dsts[0]))
		}
		return
	}
	width := len(it.lo)
	for k := 0; k <= n; k++ {
		code := encodeCode(lo+int64(k), width)
		var dst string
		if k < len(it.dsts) && len(it.dsts) > 1 {
			dst = it.dsts[k]
		} else if len(it.dsts) > 0 {
			dst = it.dsts[0]
			if k > 0 {
				dst = incrUTF16BE(dst, k)
			}
		} else {
			continue
		}
		if dst == "" {
			continue
		}
		m.addCode(code, hexToUTF8(dst))
	}
}

// hexToUTF8 把十六进制目标串（UTF-16BE 常见，也可能是单字节）转为 UTF-8。
func hexToUTF8(s string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s)%2 != 0 {
		s += "\x00"
	}
	return utf16Decode(s)
}

// incrUTF16BE 在 UTF-16BE 串上做码位增量，返回十六进制字节串。
func incrUTF16BE(dst string, delta int) string {
	if len(dst) < 2 {
		return dst
	}
	b := []byte(dst)
	n := len(b)
	// 取最后一个 16bit 单元
	u := int(b[n-2])<<8 | int(b[n-1])
	u += delta
	b[n-2] = byte(u >> 8)
	b[n-1] = byte(u)
	return string(b)
}

func codeValue(s string) int64 {
	var v int64
	for i := 0; i < len(s) && i < 8; i++ {
		v = v<<8 | int64(s[i])
	}
	return v
}

func encodeCode(v int64, width int) string {
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return string(b)
}

// forEachBlock 依次调用 fn 处理 begin/end 之间的内容。
func forEachBlock(data []byte, begin, end string, fn func([]byte)) {
	rest := data
	for {
		i := bytes.Index(rest, []byte(begin))
		if i < 0 {
			return
		}
		rest = rest[i+len(begin):]
		j := bytes.Index(rest, []byte(end))
		if j < 0 {
			return
		}
		fn(rest[:j])
		rest = rest[j+len(end):]
	}
}

// hexTokens 提取片段中所有 <..> 十六进制串（数组元素会被展平）。
func hexTokens(body []byte) []string {
	var out []string
	for i := 0; i < len(body); i++ {
		if body[i] != '<' {
			continue
		}
		j := i + 1
		for j < len(body) && body[j] != '>' {
			j++
		}
		if j >= len(body) {
			break
		}
		out = append(out, decodeHex(body[i+1:j]))
		i = j
	}
	return out
}

// decodeHex 把十六进制串解码为原始字节串。
func decodeHex(h []byte) string {
	var sb strings.Builder
	hi := -1
	for _, c := range h {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		default:
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			sb.WriteByte(byte(hi<<4 | v))
			hi = -1
		}
	}
	return sb.String()
}

type matrix [3][3]float64

var ident = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

// A Text represents a single piece of text drawn on a page.
type Text struct {
	Font     string  // the font used
	FontSize float64 // the font size, in points (1/72 of an inch)
	X        float64 // the X coordinate, in points, increasing left to right
	Y        float64 // the Y coordinate, in points, increasing bottom to top
	W        float64 // the width of the text, in points
	S        string  // the actual UTF-8 text
}

// A Rect represents a rectangle.
type Rect struct {
	Min, Max Point
}

// A Point represents an X, Y pair.
type Point struct {
	X float64
	Y float64
}

// Content describes the basic content on a page: the text and any drawn rectangles.
type Content struct {
	Text []Text
	Rect []Rect
}

type gstate struct {
	Resources Value
	Tc        float64
	Tw        float64
	Th        float64
	Tl        float64
	Tf        Font
	Tfs       float64
	Tmode     int
	Trise     float64
	Tm        matrix
	Tlm       matrix
	Trm       matrix
	CTM       matrix
}

// contentReader 返回页面内容流的合并 Reader。
// PDF 规范允许 /Contents 是单个流，也可以是流数组（很多导出工具会拆成多段），
// 原实现直接 Key("Contents") 当单流读，数组形态会得到 "stream not present" 错误。
func (p Page) contentReader() io.Reader {
	c := p.V.Key("Contents")
	switch c.Kind() {
	case Stream:
		return c.Reader()
	case Array:
		var rs []io.Reader
		for i := 0; i < c.Len(); i++ {
			item := c.Index(i)
			if item.Kind() != Stream {
				continue
			}
			// 单个内容流最多读 32MB：/Length 是文件里写的数字，可能是伪造的巨值
			data, err := io.ReadAll(io.LimitReader(item.Reader(), maxContentStreamBytes))
			if err != nil || len(data) == 0 {
				continue
			}
			rs = append(rs, strings.NewReader(string(data)))
			// 数组元素之间必须插入空白分隔，避免 token 粘连
			rs = append(rs, strings.NewReader("\n"))
		}
		if len(rs) == 0 {
			return strings.NewReader("")
		}
		return io.MultiReader(rs...)
	}
	return strings.NewReader("")
}

// Content returns the page's content.
//
// 相对原实现的增强：
//   - 递归解析 Form XObject（/Subtype /Form）：很多导出工具（如 WPS、iText 的
//     "打印为 PDF"）把整页文字塞进表单对象，原实现只读页面内容流，导致 text 为空
//   - 识别标记内容（BDC/EMC）中的可选内容组（OCG），跳过名字含水印的图层
func (p Page) Content() Content {
	cp := &contentParser{
		page:  &p,
		g:     gstate{Th: 1, CTM: ident},
		enc:   &nopEncoder{},
		fonts: make(map[string]Font, 8),
		encs:  make(map[string]TextEncoding, 8),
		steps: make(map[string]int, 8),
	}
	cp.interpret(p.contentReader(), p.Resources())
	return Content{cp.text, cp.rect}
}

type contentParser struct {
	page  *Page
	g     gstate
	enc   TextEncoding
	step  int                     // 当前字体每个码位占用字节数（1 或 2）
	fonts map[string]Font         // 已解析字体缓存（按资源名）
	encs  map[string]TextEncoding // 已解析编码缓存（按资源名）
	steps map[string]int          // 每码位字节数缓存
	text  []Text
	rect  []Rect
	depth int
	bdc   []bool // 标记内容栈：true 表示该段是水印等应忽略的内容
}

func (cp *contentParser) suppressed() bool {
	for _, v := range cp.bdc {
		if v {
			return true
		}
	}
	return false
}

func (cp *contentParser) interpret(rd io.Reader, resources Value) {
	if cp.depth > maxFormDepth {
		return
	}
	g := cp.g
	if !resources.IsNull() {
		g.Resources = resources
	}
	cp.g = g
	var gstack []gstate

	showText := func(raw string) {
		if cp.suppressed() {
			return
		}
		// CID 字体一个码位占 2 字节，简单字体占 1 字节（在 Tf 时算好并缓存）
		step := cp.step
		if step <= 0 {
			step = 1
		}
		for i := 0; i < len(raw); i += step {
			end := i + step
			if end > len(raw) {
				end = len(raw)
			}
			code := raw[i:end]
			codeVal := 0
			for k := 0; k < len(code); k++ {
				codeVal = codeVal<<8 | int(code[k])
			}
			w0 := g.Tf.Width(codeVal)
			for _, ch := range cp.enc.Decode(code) {
				Trm := matrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(g.Tm).mul(g.CTM)
				if ch != ' ' {
					f := g.Tf.BaseFont()
					if k := strings.Index(f, "+"); k >= 0 {
						f = f[k+1:]
					}
					cp.text = append(cp.text, Text{f, Trm[0][0], Trm[2][0], Trm[2][1], w0 / 1000 * Trm[0][0], string(ch)})
				}
			}
			tx := w0/1000*g.Tfs + g.Tc
			if code == " " {
				tx += g.Tw
			}
			tx *= g.Th
			g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
		}
	}

	InterpretReader(rd, func(stk *Stack, op string) {
		n := stk.Len()
		args := make([]Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		switch op {
		default:
			return

		case "Do": // 绘制 XObject：递归处理表单对象
			if len(args) != 1 || cp.depth >= maxFormDepth {
				return
			}
			name := args[0].Name()
			xo := g.Resources.Key("XObject").Key(name)
			if xo.Kind() != Stream || xo.Key("Subtype").Name() != "Form" {
				return
			}
			formG := g
			// 表单矩阵
			if m := xo.Key("Matrix"); m.Kind() == Array && m.Len() == 6 {
				var mat matrix
				for i := 0; i < 6; i++ {
					mat[i/2][i%2] = m.Index(i).Float64()
				}
				mat[2][2] = 1
				formG.CTM = mat.mul(formG.CTM)
			}
			formRes := xo.Key("Resources")
			if formRes.IsNull() {
				formRes = g.Resources
			}
			data := readAllSafe(xo)
			if len(data) == 0 {
				return
			}
			savedG := cp.g
			cp.g = formG
			cp.depth++
			cp.interpret(strings.NewReader(string(data)), formRes)
			cp.depth--
			cp.g = savedG

		case "BMC": // 标记内容开始（无属性）
			cp.bdc = append(cp.bdc, false)

		case "BDC": // 标记内容开始（带属性）
			wm := false
			if len(args) == 2 && args[0].Name() == "OC" {
				if propName := args[1].Name(); propName != "" {
					ocg := g.Resources.Key("Properties").Key(propName)
					label := ocg.Key("Name").Text()
					if label == "" {
						label = ocg.Key("Name").RawString()
					}
					if isWatermarkLabel(label) {
						wm = true
					}
				}
			}
			cp.bdc = append(cp.bdc, wm)

		case "EMC": // 标记内容结束
			if len(cp.bdc) > 0 {
				cp.bdc = cp.bdc[:len(cp.bdc)-1]
			}

		case "cm": // update g.CTM
			if len(args) != 6 {
				return
			}
			var m matrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.CTM = m.mul(g.CTM)

		case "f", "g", "l", "m", "cs", "scn", "rg", "RG", "w", "W", "W*", "n", "S", "s", "h", "c", "v", "y", "B", "b", "f*", "B*", "b*":
			// 绘图指令，仅关心文本

		case "re": // append rectangle to path
			if len(args) != 4 {
				return
			}
			x, y, w, h := args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64()
			cp.rect = append(cp.rect, Rect{Point{x, y}, Point{x + w, y + h}})

		case "q": // save graphics state
			gstack = append(gstack, g)

		case "Q": // restore graphics state
			if n := len(gstack) - 1; n >= 0 {
				g = gstack[n]
				gstack = gstack[:n]
			}

		case "BT": // begin text
			g.Tm = ident
			g.Tlm = g.Tm

		case "ET": // end text

		case "T*": // move to start of next line
			x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm

		case "Tc": // set character spacing
			if len(args) != 1 {
				return
			}
			g.Tc = args[0].Float64()

		case "TD": // move text position and set leading
			if len(args) != 2 {
				return
			}
			g.Tl = -args[1].Float64()
			fallthrough
		case "Td": // move text position
			if len(args) != 2 {
				return
			}
			tx := args[0].Float64()
			ty := args[1].Float64()
			x := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm

		case "Tf": // set text font and size
			if len(args) != 2 {
				return
			}
			f := args[0].Name()
			if cached, ok := cp.fonts[f]; ok {
				g.Tf = cached
			} else {
				g.Tf = Font{g.Resources.Key("Font").Key(f)}
				cp.fonts[f] = g.Tf
			}
			if enc, ok := cp.encs[f]; ok {
				cp.enc = enc
				cp.step = cp.steps[f]
			} else {
				enc := g.Tf.Encoder()
				if enc == nil {
					enc = &nopEncoder{}
				}
				step := bytesPerCode(enc)
				if step <= 0 {
					step = 1
				}
				cp.encs[f] = enc
				cp.steps[f] = step
				cp.enc = enc
				cp.step = step
			}
			g.Tfs = args[1].Float64()

		case "\"": // set spacing, move to next line, and show text
			if len(args) != 3 {
				return
			}
			g.Tw = args[0].Float64()
			g.Tc = args[1].Float64()
			args = args[2:]
			fallthrough
		case "'": // move to next line and show text
			if len(args) != 1 {
				return
			}
			x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm
			fallthrough
		case "Tj": // show text
			if len(args) != 1 {
				return
			}
			showText(args[0].RawString())

		case "TJ": // show text, allowing individual glyph positioning
			if len(args) != 1 {
				return
			}
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == String {
					showText(x.RawString())
				} else {
					tx := -x.Float64() / 1000 * g.Tfs * g.Th
					g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
				}
			}

		case "TL": // set text leading
			if len(args) != 1 {
				return
			}
			g.Tl = args[0].Float64()

		case "Tm": // set text matrix and line matrix
			if len(args) != 6 {
				return
			}
			var m matrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.Tm = m
			g.Tlm = m

		case "Tr": // set text rendering mode
			if len(args) != 1 {
				return
			}
			g.Tmode = int(args[0].Int64())

		case "Ts": // set text rise
			if len(args) != 1 {
				return
			}
			g.Trise = args[0].Float64()

		case "Tw": // set word spacing
			if len(args) != 1 {
				return
			}
			g.Tw = args[0].Float64()

		case "Tz": // set horizontal text scaling
			if len(args) != 1 {
				return
			}
			g.Th = args[0].Float64() / 100
		}
	})
	cp.g = g
}

// readAllSafe 读取流的全部内容，出错或 panic 时返回 nil。
func readAllSafe(v Value) []byte {
	var data []byte
	func() {
		defer func() { recover() }()
		data, _ = io.ReadAll(v.Reader())
	}()
	return data
}

// isWatermarkLabel 判断可选内容组名是否为水印图层。
func isWatermarkLabel(s string) bool {
	s = strings.ToLower(s)
	for _, k := range []string{"watermark", "water mark", "水印", "zp_watermark"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// TextVertical implements sort.Interface for sorting
// a slice of Text values in vertical order, top to bottom,
// and then left to right within a line.
type TextVertical []Text

func (x TextVertical) Len() int      { return len(x) }
func (x TextVertical) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextVertical) Less(i, j int) bool {
	if x[i].Y != x[j].Y {
		return x[i].Y > x[j].Y
	}
	return x[i].X < x[j].X
}

// TextVertical implements sort.Interface for sorting
// a slice of Text values in horizontal order, left to right,
// and then top to bottom within a column.
type TextHorizontal []Text

func (x TextHorizontal) Len() int      { return len(x) }
func (x TextHorizontal) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextHorizontal) Less(i, j int) bool {
	if x[i].X != x[j].X {
		return x[i].X < x[j].X
	}
	return x[i].Y > x[j].Y
}

// An Outline is a tree describing the outline (also known as the table of contents)
// of a document.
type Outline struct {
	Title string    // title for this element
	Child []Outline // child elements
}

// Outline returns the document outline.
// The Outline returned is the root of the outline tree and typically has no Title itself.
// That is, the children of the returned root are the top-level entries in the outline.
func (r *Reader) Outline() Outline {
	return buildOutline(r.Trailer().Key("Root").Key("Outlines"))
}

func buildOutline(entry Value) Outline {
	var x Outline
	x.Title = entry.Key("Title").Text()
	for child := entry.Key("First"); child.Kind() == Dict; child = child.Key("Next") {
		x.Child = append(x.Child, buildOutline(child))
	}
	return x
}
