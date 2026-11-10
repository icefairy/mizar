# internal/pdftext

`rsc.io/pdf`（版本 v0.1.1，BSD 许可，见 LICENSE）的**内联修复分支**。

## 为什么内联

上游 `rsc.io/pdf` v0.1.1 已基本停止维护，对现代 PDF（尤其是国内简历/公文类
PDF——普遍使用 WPS / iText 5 导出、中文 CID 字体、表单 XObject、水印图层）存在
多处致命缺陷，导致 `read` 工具读取 PDF 时要么报错要么输出乱码。这些修复需要改动
库内部逻辑，无法通过包装绕过，因此把源码内联进仓库维护。

## 相对上游的修复

| # | 问题 | 影响 | 修复位置 |
|---|------|------|----------|
| 1 | `/Contents` 是**流数组**时按单流读取 | 报 `stream not present`，整个文件读不了 | `Page.contentReader` |
| 2 | `ToUnicode` 只判断 `Dict`，实际是 **`Stream`** | 所有 CID 中文字体解出乱码 | `Font.Encoder` |
| 3 | `Identity-H` 没有解码器（`TODO` 未实现） | 输出原始字节 | `ucs2Encoder` |
| 4 | CMap 只解析 `bfrange`，不解析 **`bfchar`** | 绝大多数 WPS/iText 导出文件无映射 | `parseCMapData` |
| 5 | `bfrange` 不处理数组目标 `<lo> <hi> [<a> <b>]` | 部分映射缺失 | `expandBfRange` |
| 6 | 不递归解析 **Form XObject**（`Do`） | 整页文字放在表单里时 text 为空 | `contentParser` `case "Do"` |
| 7 | 无 `/W`、`/DW` 支持，CID 字体字宽恒为 0 | 无法按坐标排序（全部挤在一起） | `Font.cidWidth` |
| 8 | `predictor` 只支持 PNG Up(12) 且硬编码断言 | 带 TIFF/PNG 其它预测的流解析失败 | `newPNGPredictor` / `newTIFFPredictor` |
| 9 | 向 stdout 打印调试噪音、`println` 干扰 CLI 输出 | 破坏 Agent 输出 | 全局清理 |
| 10 | 页面树缺 `/Count` 时取不到页 | 页数为 0 | `Reader.Page` / `countKids` |

## 额外增强

- **跳过水印图层**：识别 `BDC /OC /Xi0` 等标记内容，若可选内容组（OCG）名称含
  `watermark` / `水印` 则整段丢弃，避免正文被水印 URL 淹没。
- **字体/编码缓存**：按资源名缓存 `Font`、`TextEncoding` 与码位宽度，避免对
  每个文字串重复解析 CMap（实测 4 页中文简历从 8.3s 降到 1.2s）。
- `InterpretReader(io.Reader, ...)`：支持拼接多个内容流后再做 PostScript 解释。

## 使用

```go
import "mizar/internal/pdftext"

r, err := pdftext.NewReader(f, size)
content := r.Page(1).Content() // content.Text 已按 UTF-8 正确解码
```

上层封装见 `internal/utils.ExtractPDF`（按 Y 降序 → X 升序还原阅读顺序）。

## 维护

源码为 BSD-3-Clause（见 LICENSE），修改之处均以中文注释标注「修复要点」，
升级上游时请对照上面的表格重新核对。
