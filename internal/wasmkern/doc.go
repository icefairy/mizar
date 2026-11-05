// Package wasmkern 在 Go 进程内运行 Zig 编译出的 WebAssembly 计算内核。
//
// 为什么存在：插件跑在 goja（纯 Go 的 JS 解释器，无 JIT）。一个纯计算型插件
// （比如用 JS 循环估算 token 数）实测比 Go 原生慢 **696 倍**（95ms vs 137µs，见
// bench_test.go 的 BenchmarkGojaJS / BenchmarkGoNativeSame）。把这类重计算
// 下沉到 Zig→wasm 内核，能拿到接近原生的性能，同时**不引入 cgo**——
// wazero 是纯 Go 实现的 wasm 运行时，因此 CGO_ENABLED=0、纯静态单文件、
// 跨发行版部署、Windows 交叉编译这些项目前提全部保持不变。
//
// 设计要点：
//   - 内核产物（*.wasm）用 go:embed 内嵌，随二进制发布，运行时无需外部文件。
//   - 每个内核按需惰性编译；编译结果进 wazero.CompilationCache，多实例共享。
//   - 模块实例放在 sync.Pool 里：wazero 的模块实例不是并发安全的，
//     每次调用从池里取一个独占使用，用完归还。
//   - 内存布局：低地址 1024 字节留给栈/全局变量，之后是内核可用的数据传输区。
//     大输入采用「一次写入、多次计算」的两段式 API（Load + CallLoaded），
//     避免每次调用重复拷贝。
//   - 任何加载/调用失败都**不致命**：调用方拿 bool 判断，回退到纯 Go 实现。
//
// 关于 count_tokens 的实测结论（重要，避免后来者重走弯路）：
// 该算法的纯 Go 版本（for range string，零分配）在**所有尺寸上都比 wasm 快**：
//
//	输入          纯 Go       wasm 内核
//	9 字节        13.8 ns     5.2 µs
//	100 KB        137 µs      368 µs
//	1 MB          1.94 ms     2.94 ms
//
// 原因是跨边界调用 + 线性内存拷贝的固定成本，超过了「Go 循环 vs wasm 循环」的
// 差距。因此 count_tokens 默认**不走**内核（wantKernel 返回 false），
// 仅在 MIZAR_WASM_KERNEL=force 时启用，用于验证链路与压测。
//
// 真正的收益场景是「goja 里的重计算」——那才是 696 倍的差距所在。
// 后续候选内核（需要大词典的 seg_cut、严格 JSON 校验等）应先用
// bench_test.go 的方式量化，确认 Go 侧确实慢再下沉。
package wasmkern
