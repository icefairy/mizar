package pdftext

import "testing"

// 真实 WPS/iText 导出的 ToUnicode CMap 片段（含 bfchar、bfrange 与占位映射）。
const sampleCMap = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo
<< /Registry (Adobe)
/Ordering (UCS)
/Supplement 0
>> def
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
4 beginbfchar
<0003> <0020>
<1F80> <4E0E>
<0275> <0000>
<046A> <FF0C>
endbfchar
2 beginbfrange
<0010> <0011> <002D>
<0013> <001C> <0030>
endbfrange
endcmap
CMapName currentdict /CMap defineresource pop
end
end
`

// TestParseCMapBfcharAndBfrange 验证 bfchar / bfrange 都能正确解析。
func TestParseCMapBfcharAndBfrange(t *testing.T) {
	m := parseCMapData([]byte(sampleCMap))
	if m == nil {
		t.Fatal("parseCMapData returned nil")
	}
	cases := map[string]string{
		"\x00\x03": " ",
		"\x1f\x80": "与",
		"\x04\x6a": "，",
		"\x00\x10": "-",
		"\x00\x11": ".",
		"\x00\x13": "0",
		"\x00\x1c": "9",
	}
	for src, want := range cases {
		if got := m.Decode(src); got != want {
			t.Errorf("Decode(%x) = %q, want %q", src, got, want)
		}
	}
	// 占位映射 <0000> 不应覆盖后面出现的真实映射
	if got := m.Decode("\x02\x75"); got == "\x00" || got == "" {
		t.Errorf("placeholder mapping leaked: %q", got)
	}
}

// TestCMapDecodeMultiByte 验证多字节序列按两个字节一个码位切分。
func TestCMapDecodeMultiByte(t *testing.T) {
	m := parseCMapData([]byte(sampleCMap))
	got := m.Decode("\x1f\x80\x04\x6a\x00\x03")
	if got != "与， " {
		t.Fatalf("Decode = %q, want %q", got, "与， ")
	}
}

// TestUCS2Encoder 验证 Identity-H 无 ToUnicode 时的双字节大端兜底解码。
func TestUCS2Encoder(t *testing.T) {
	e := &ucs2Encoder{}
	if got := e.Decode("\x4e\x2d\x65\x87"); got != "中文" {
		t.Fatalf("Decode = %q, want 中文", got)
	}
	// 代理对：U+1F600
	if got := e.Decode("\xd8\x3d\xde\x00"); got != "😀" {
		t.Fatalf("surrogate pair Decode = %q, want 😀", got)
	}
}

// TestIsWatermarkLabel 验证水印图层名识别。
func TestIsWatermarkLabel(t *testing.T) {
	for _, s := range []string{"zp_watermark", "Watermark", "我的水印"} {
		if !isWatermarkLabel(s) {
			t.Errorf("%q should be watermark", s)
		}
	}
	if isWatermarkLabel("Layer 1") {
		t.Error("Layer 1 should not be watermark")
	}
}
