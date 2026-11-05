// wasm_demo.ts —— 演示「插件调用 Zig 编译的 wasm 内核」（形态 A）。
//
// 这是插件侧使用 wasm_invoke 的最小模板：
//   - 宿主函数 wasm_invoke(kernel, fn, input)  → 文本进、文本出
//   - 宿主函数 wasm_invoke_b64(kernel, fn, b64) → 二进制进、二进制出
//   - 宿主函数 wasm_kernels()                   → 可用内核名 JSON 数组
// 内核契约见 internal/wasmkern/generic.go；参考内核见 internal/wasmkern/upper.zig。
//
// 为什么要这样写：插件跑在 goja（纯 Go 解释器，无 JIT），重计算比 Go 原生慢
// 三个数量级。把算法用 Zig 写、编成 wasm 内嵌进 Mizar 二进制，插件只留业务胶水，
// 既能拿到接近原生的性能，又不引入 cgo / 子进程 / 外部文件依赖。
//
// 降级策略（重要）：宿主函数缺失或内核不存在时**不能抛错**，
// 否则插件在旧版 Mizar 上会整体加载失败。下面用 typeof 探测 + try/catch 兜底。

declare const wasm_invoke: (kernel: string, fn: string, input: string) => string;
declare const wasm_invoke_b64: (kernel: string, fn: string, b64: string) => string;
declare const wasm_kernels: () => string;

/** 探测宿主端是否有某个 wasm 内核。何时用：插件启动/调用前判断能否走内核路径。 */
function hasKernel(name: string): boolean {
    if (typeof wasm_kernels !== "function") return false;
    try {
        const list = JSON.parse(wasm_kernels());
        return Array.isArray(list) && list.indexOf(name) >= 0;
    } catch (e) {
        return false;
    }
}

/** 列出当前二进制里内嵌的 Zig wasm 内核。参数: 无 */
export function tool_wasm_kernels(args: string): string {
    if (typeof wasm_kernels !== "function") {
        return "宿主未提供 wasm_kernels（当前 Mizar 版本不支持 Zig wasm 内核）";
    }
    return wasm_kernels();
}

/**
 * 用 Zig wasm 内核把英文转大写（示例：演示插件如何把重计算下沉到 Zig）。
 * 参数: 待转换的文本（不传则用示例串）
 */
export function tool_zig_upper(args: string): string {
    const input = args.trim() || "hello from zig wasm";
    if (!hasKernel("upper")) {
        return "内核 upper 不可用（先 make zig-kernels 编译并内嵌），回退: " + input.toUpperCase();
    }
    try {
        return wasm_invoke("upper", "upper", input);
    } catch (e) {
        return "wasm_invoke 失败: " + e + "，回退: " + input.toUpperCase();
    }
}
