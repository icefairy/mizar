# Zig → WebAssembly 内核评估：独立 wasm 与进程内内核的真实代价

> 结论先行：**把计算下沉到 wasm 不只是「启动慢一点」**。除了进程启动，
> 单次调用吞吐、延迟稳定性、内存占用、可调试性四项都受影响。
> 本页所有数字为 i7-10700 / Linux 6.8 实测，复现脚本见文末。

---

## 0. 前提不变量

引入 wasm 内核**不破坏**以下项目前提：

- `CGO_ENABLED=0`：wazero 是纯 Go 实现，无 cgo
- 纯静态单文件、跨发行版部署、Windows / arm64 交叉编译均正常
- `.wasm` 用 `go:embed` 内嵌，运行时无外部文件依赖

代价：二进制 **+2684 KB**（67.85 → 70.47 MB，+3.86%），`go.mod` 多一个直接依赖 `wazero`。

---

## 1. 单次调用：wasm 比原生慢 2–5×，且不是启动开销

同样是分词一段 30 字节中文（`中文分词记忆检索 hello world 性能测试`）：

| 路径 | 耗时 | 分配 |
|---|---|---|
| 纯 Zig 原生（零跨语言边界） | **1.42 µs** | 0 |
| Go 参考实现 `refTokenize` | **7.70 µs** | 42 allocs |
| wasm 内核完整调用 | **16.5 µs** | 9 allocs |

### 边界成本拆解（证明慢的不是「跨边界」）

| 动作 | 耗时 |
|---|---|
| 空调用 `bigram_tokenize(len=0)`（纯函数调用 + 返回） | **132 ns** |
| `mem.Write` 30 字节 | **9 ns** |
| 完整调用 30 字节 | **5183 ns** |

即 **16.5 µs 中只有约 0.14 µs 是跨边界成本**（0.8%）。剩下的是 Go 侧
`strings.ToLower`、指针换算、`ensure` 容量检查、把 `[]u32` 的
`(offset,len)` span 还原成 `[]string` 的工程量。

> 结论：**为了安全封装做的 Go 侧搬运，比 wasm 调用本身贵 100 倍。**
> 换句话说不论内核写得多快，这段「胶水」都吃掉收益。

### 不同输入长度的完整调用耗时（wasm，含 mem.Write）

| 输入长度 | 耗时 |
|---|---|
| 30 B | 5.2 µs |
| 100 B | 1.1 µs |
| 1000 B | 8.7 µs |
| 4000 B | 34.2 µs |

（30 B 那行偏高是首调抖动，稳态与 100 B 同量级。）

---

## 2. 批量场景：wasm 慢 1.7×，但分配少 18×

2000 条记忆批量分词（`-benchtime 3x`）：

| 路径 | 耗时 | 字节/op | 分配/op |
|---|---|---|---|
| `BenchmarkBigramWasm2000` | **96.6 ms** | 52.0 MB | **18,317** |
| `BenchmarkBigramGo2000` | **56.1 ms** | 36.1 MB | **338,044** |

这是一个**真实取舍**，不是单纯变慢：

- wasm 墙钟慢 **1.72×**
- 但 GC 对象数少 **18×**（3.3 万 vs 33.8 万），GC 停顿压力显著更低
- 代价是字节吞吐更大（52 MB vs 36 MB，因为 `[]u32` 输出区 + 线性内存拷贝）

> 因此 `MIZAR_WASM_BIGRAM` **默认关闭是正确的**。不要为了「用了黑科技」
> 把实测更慢的路径设成默认——只有当 GC 停顿成为瓶颈时才值得反过来切。

---

## 3. 内存：每实例固定 1.1 MB，池化会线性放大

- 单实例线性内存 **1152 KB**（`kernel_data_end()` = 1,114,541 字节，
  几乎全被两张哈希表占满）
- 5 个独立实例合计 **5760 KB**
- 进程 RSS：4 MB → **9 MB**（其中 Go heap `Alloc=3MB Sys=13MB`）
- 线性内存可 `Grow`，32 位地址空间是**硬顶 4 GB**（超了报错而非 OOM 杀进程）

### 并发安全性与池化放大

`sync.Pool` 池化在 par = 1 / 4 / 16 / 64 并发下**正确性全部通过**
（每 goroutine 200 次调用零失败）。但实例数**随并发 goroutine 数膨胀**：
64 并发峰值可同时持有 64 × 1.1 MB ≈ **70 MB** 线性内存。

> 高频并发场景要么限制池上限，要么就别用 wasm 内核。

---

## 4. GC 会造成毫秒级延迟尖刺（最容易踩的坑）

```
稳态            :  23 µs / 次
runtime.GC() ×2 :  sync.Pool 被清空，实例被回收
GC 后首次调用   :  2.06 ms   ← 需要重新实例化
再次稳态        :  15 µs / 次
```

- 全新 runtime + 实例化（无 CompilationCache）：**2.45 ms / 次**
  （20 次平均，`wazero.NewRuntime` + `Instantiate` + `Close`）
- `CompilationCache` 能省掉编译，但**实例化仍要重做**

> **低频调用场景下，每次调用都可能退化成 2.5 ms。** 如果内核调用频率
> 低于 GC 周期，wasm 的延迟会呈现「锯齿」——P99 直接被 GC 决定。

---

## 5. 可调试性下降（工程上最痛的一项）

越界输入实测：

```
wasm error: out of bounds memory access
wasm stack trace:
  bigram.wasm.bigram.bigram_tokenize(i32,i32,i32,i32) i32
    0xfb: bigram.zig:78:16 (inlined)
          bigram.zig:180:25
```

**收益**：内核写坏指针会 **trap**，而不是像原生那样静默踩内存。
沙箱边界是真实的安全价值——这也是 wasm 唯一实打实的好处。

**代价**：

| 场景 | 影响 |
|---|---|
| 报错信息 | 只有 wasm 栈帧，**没有 Go 栈**，要手工对 `.zig` 行号 |
| `go tool pprof` | CPU 全归到 `wasmcall`，**无法归因到 Zig 行** |
| `dlv` | 无法单步进内核，只能整体黑盒 |
| 改逻辑 | 必须重编 `.wasm`（`make zig-kernels` 很快，但仍是跨工具链） |
| panic 语义 | Zig 的 panic/断言在 wasm 里变 trap，与原生不完全一致 |

---

## 6. wasm32-freestanding 的硬能力边界

当前内核用 `-target wasm32-freestanding -fno-entry -rdynamic`：
**无任何 syscall**。

- **无 IO**：文件/网络都需要换 `wasm32-wasi` 并让宿主提供 WASI 实现
- **无线程**：内核内不能起线程/TLS，并发只能靠宿主开多实例
- **栈深度受限**：深递归直接 trap
- **无 SIMD/AVX 与针对性指令选择**：wazero JIT 产物比 zig 原生慢，
  这是 §1 中 1.42 µs → 16.5 µs 的主因之一

---

## 7. 收益侧：唯一真正划算的场景

| 场景 | 相对加速 |
|---|---|
| goja 里 JS 循环 vs wasm 内核（100KB 混合文本） | **259×**（95.4 ms → 368 µs） |
| goja 里 JS 循环 vs 纯 Go 原生 | **696×**（95.4 ms → 137 µs） |
| wasm vs 纯 Go 原生 | **0.37×（更慢）** |

- 真正的痛点是「**goja 里的重计算**」，不是「Go 里的重计算」。
- 增益来自「**把循环搬出解释器**」，而不是「用上了 wasm」。
- 同样的收益用纯 Go 实现能拿到更多（再快 2.7×），且没有 §1–§6 的任何代价。

### 记忆检索批量打分（真实收益案例）

| 路径 | 耗时 | 分配 |
|---|---|---|
| goja 里逐条 `similarity()` | **102 ms** | 372k allocs |
| 宿主函数 `mem_score` 批量打分 | **10.8 ms** | 4.2k allocs |

**9.4×，且其中没有 wasm 的功劳**——收益全部来自把循环从 goja 搬到 Go。

---

## 8. 决策矩阵

| 影响面 | 严重度 | 说明 |
|---|---|---|
| 单次调用慢 2–5× | 🔴 高 | 任何热路径都不划算 |
| 可调试性下降 | 🔴 高 | 线上排障成本显著上升 |
| GC 后 2 ms 尖刺 | 🟡 中 | 低频场景致命，P99 被 GC 决定 |
| 内存 +1.1 MB/实例 | 🟡 中 | 高并发时线性放大 |
| 二进制 +2.7 MB | 🟡 中 | 单文件分发仍可接受 |
| 进程启动慢 48× | 🟢 低 | CLI 场景才有感，Mizar 是进程内，已摊销 |
| 沙箱安全 + 全平台同一份字节码 | ✅ 收益 | 唯一实打实的好处 |

### 适用 / 不适用

**适合 wasm 内核**：
- 不频繁调用（单次 > 10 ms 的量级，能盖过 2 ms 抖动）
- 需要沙箱（用户可上传/不可信计算逻辑）
- 需要跨平台同一份字节码（省去多架构重编）

**不适合**：
- 每次记忆检索都要跑的热路径（当前 bigram / count_tokens）
- 逐条小输入调用（固定开销完全吃掉差距）
- 需要 IO、线程、深递归的场景

> 现状：`bigram` 与 `count_tokens` 内核**默认走纯 Go**，
> `MIZAR_WASM_*=force` 才启用，仅用于对照与压测。这个默认是诚实的。

---

## 9. 复现方式

```bash
# 1) 内核单元与 Go 参考实现交叉校验
GOPROXY=off MIZAR_WASM_VERIFY=1 go test ./internal/wasmkern/ -count=1

# 2) 批量分词两条路径对比
GOPROXY=off go test ./internal/wasmkern/ \
  -run XXX -bench 'Bigram.*2000' -benchtime 3x -count=1

# 3) 短文本单次调用对比
GOPROXY=off go test ./internal/wasmkern/ \
  -run XXX -bench 'Short' -benchtime 20000x -count=1

# 4) 纯 Zig 原生基线（零边界）
#    把 bigram.zig 的 export fn 改为 pub fn 后由本地 harness import，
#    用 std.Io.Clock.Timestamp.now(io, .awake) 计时
#    （注意 Zig 0.17.0 已无 std.time.Timer）

# 5) 重编内核产物（改了 .zig 才需要）
make zig-kernels
```

---

## 10. 一句话总结

> wasm 给 Mizar 带来的不是性能，而是**沙箱边界与跨平台字节码**。
> 性能上它是净损失，调试上是净损失，只在「不可信计算 + 低频 + 重计算」
> 这一狭窄交集里才划算。**别把它当性能优化手段用。**
