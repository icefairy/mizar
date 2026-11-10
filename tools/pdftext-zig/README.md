# pdftext（Zig 单文件 PDF 文本提取器）

零依赖、单文件、约 260KB 的静态二进制，用于从 PDF 提取文本（尤其国内
WPS / iText 导出的中文简历与公文）。

## 为什么有它

Go 侧已有 `internal/pdftext`（`rsc.io/pdf` 修复分支），但仍依赖 Go 运行时
与整个 mizar 进程。这个 Zig 版本提供：

* **独立二进制**：可直接 `./pdftext x.pdf`，不需要 Go、不需要 mizar
* **可交叉编译**：`-Dtarget=aarch64-linux` / `x86_64-windows` 一行搞定
* **体积小**：260KB（Go 版 mizar 二进制是 285MB）
* **无 CGO / 无 libc**：纯标准库实现

## 构建

```bash
make                    # 产出 ./pdftext
make test               # 单元测试
zig build-exe pdftext.zig -OReleaseSmall --name pdftext   # 等价
```

交叉编译：

```bash
zig build-exe pdftext.zig -OReleaseSmall -target aarch64-linux --name pdftext-arm64
zig build-exe pdftext.zig -OReleaseSmall -target x86_64-windows --name pdftext.exe
```

## 用法

```bash
./pdftext file.pdf                     # 输出文本（页面间用 \f 分隔）
./pdftext file.pdf -p                  # 只看页数 / 页面结构
./pdftext file.pdf -d                  # 每段前打印坐标（调试排版）
./pdftext file.pdf --no-page-break     # 不插页分隔符
./pdftext file.pdf --line-tolerance 2  # 判定同行的 Y 容差（默认 1.5pt）
```

## 实现覆盖

| 特性 | 状态 |
|---|---|
| 传统 xref 表 | ✅ |
| xref 流（PDF 1.5+，含 /Prev 链、/XRefStm） | ✅ |
| 对象流 ObjStm（压缩对象） | ✅ |
| 页面树（含继承 /Resources、/MediaBox、缺 /Count） | ✅ |
| /Contents 单流与流数组 | ✅ |
| FlateDecode + PNG/TIFF predictor | ✅ |
| Form XObject 递归（含 /Matrix） | ✅ |
| Type0/CID + Identity-H + ToUnicode CMap | ✅ |
| ToUnicode bfchar / bfrange / 数组目标 | ✅ |
| 简单字体 WinAnsi/MacRoman/Differences | ✅ |
| CID 字宽（/W、/DW）用于坐标排序 | ✅ |
| 水印图层（OCG 名含 watermark/水印）跳过 | ✅ |
| 加密 PDF / LZW / JPX / 扫描件 OCR | ❌ |

## 已验证

| 文件 | 字符数 | 备注 |
|---|---|---|
| 4 份中文简历（WPS 导出，PDF 1.7，Form XObject + 水印） | 3.0k~4.9k | 无乱码、无水印残留 |
| `shared-mime-info-spec.pdf`（TeX 生成，xref 流 + ObjStm + bfrange） | 全部正文 | 与 pdftotext 结果一致 |

## 关键实现要点

1. **ToUnicode 是 Stream 而非 Dict**——很多解析器只认 Dict，导致中文全乱码。
2. **bfchar 与 bfrange 必须各自独立扫描**——CMap 里两者顺序不固定，用单一游标会漏。
3. **CID 字体一码两字节**——按 `Enc.bytesPerCode()` 切分，否则逐字节解码全错。
4. **Form XObject 要递归且继承 CTM**——Y 翻转页面（`d<0`）靠 CTM 传递才对得上坐标。
5. **TJ 的负位移是词间距**——位移超过 `0.12 * fontSize` 补一个空格，小于则是字距微调。

## 与 Go 版（internal/pdftext）的关系

`internal/utils.ExtractPDF` 采用**双引擎**策略：

```
MIZAR_PDF_ZIG=/path/to/pdftext   （或把 pdftext 放在 mizar 二进制同目录）
        ↓ 命中
   tools/pdftext-zig（本目录）  ← 快（0.04s/份），支持交叉编译
        ↓ 未配置 / 执行失败 / 输出为空
   internal/pdftext（Go 修复分支） ← 兜底，无外部依赖
```

测试对比（唐智旺简历，4 页）：

| 引擎 | 耗时 | 输出 |
|---|---|---|
| Zig | 0.04s | 9761 字符，保留 PDF 原始字间距 |
| Go | 1.27s | 9517 字符，字间空格被合并 |

两者内容一致，Zig 版更快且排版信息保留更完整。
