# 用 Zig 扩展 Mizar：形态 A（wasm 内核）与形态 B（MCP server）

Mizar 的插件是 TypeScript（运行在 goja 解释器里，纯 Go，无 JIT）。业务胶水放插件没问题，
但重计算（编解码/压缩/加密/图像/协议解析）在 goja 里慢 2~3 个数量级。
把重计算下沉到 Zig，是**在保持 CGO_ENABLED=0、纯静态单文件、跨发行版部署**前提下的正解。

两种形态：

| | 形态 A：wasm 内核 | 形态 B：MCP server |
|---|---|---|
| 产物 | `.wasm`，内嵌进 Mizar 主二进制 | 独立静态二进制（子进程） |
| 调用方式 | 插件内宿主函数 `wasm_invoke(kernel, fn, input)` | 插件内 `mcp_call(server, tool, args)` |
| 隔离性 | 沙箱（wazero），无系统调用 | 完全隔离进程，可用全部系统能力 |
| 性能 | 接近原生（无 JIT 也在 1/5 左右） | 原生 |
| 热加载 | 需重新构建 Mizar 二进制 | **重编译 + `/mcp reload`**（杀旧子进程重连） |
| 适用 | 纯计算、短小内核 | 需要 IO/网络/多线程/大依赖 |

---

## 形态 A：Zig → wasm 内核

### 1. 生成骨架

```
/zig new-wasm ./mykernel
```

生成 `kernel.zig`，通用 ABI 契约：

```zig
export fn kernel_data_end() u32;              // 可选：静态数据区结束地址
export fn <name>(in_ptr: [*]const u8, in_len: usize) u32;  // 返回响应头地址
```

响应头是内存里两个小端 `u32`：`[out_ptr, out_len]`。Go 侧（`internal/wasmkern/generic.go`）
按此契约读结果，因此**任何遵守契约的 Zig 内核都能被直接调用，无需改 Go 代码**。

### 2. 编译

```
zig build-exe kernel.zig -target wasm32-freestanding -OReleaseFast \
  --name mykernel -fno-entry -rdynamic -femit-bin=mykernel.wasm
```

（bash 工具会自动把内嵌 Zig 前置到 PATH，直接敲 `zig` 即可；未释放时先 `/zig init`。）

### 3. 内嵌

把 `mykernel.wasm` 放进 `internal/wasmkern/`，`make zig-kernels` 会遍历编译所有 `.zig`
并生成 embed 清单；然后正常 `go build` 即可——产物仍是**单个静态二进制**。

### 4. 插件调用

```ts
if (typeof wasm_kernels === "function" && JSON.parse(wasm_kernels()).indexOf("mykernel") >= 0) {
    return wasm_invoke("mykernel", "mykernel", input);   // 文本进、文本出
}
return fallbackInJS(input);   // 旧版 Mizar / 内核缺失时降级，勿抛错
```

二进制数据用 `wasm_invoke_b64(kernel, fn, base64)`。

宿主函数清单（`internal/engine/wasmhost.go`）：
`wasm_invoke` / `wasm_invoke_b64` / `wasm_kernels`。

参考实现：`internal/wasmkern/upper.zig`；示例插件：`extensions/wasm_demo.ts`。

---

## 形态 B：Zig → 独立 MCP server

### 1. 生成骨架

```
/zig new-mcp ./myserver
```

生成 `server.zig`：一个零依赖、按行分隔 JSON-RPC 2.0 的 MCP server，已实现
`initialize` / `tools/list` / `tools/call`，含 `echo`（回显）与 `hostname`（系统调用）两个示例工具。
你只需改 `callTool()` 与 `tools_json`。

### 2. 编译（可交叉编译）

```
zig build-exe server.zig -OReleaseFast -femit-bin=myserver
zig build-exe server.zig -OReleaseFast -target aarch64-linux -femit-bin=myserver-arm64
```

产物是**单文件静态二进制、零运行时依赖**，可直接丢到 CentOS 7（glibc 2.17）跑。

### 3. 注册进 Mizar

```
/mcp add myserver /abs/path/myserver
```

等价于在 `~/.mizar/config.json` 写：

```json
"mcp_servers": [{"name": "myserver", "command": "/abs/path/myserver"}]
```

### 4. 插件里调用

```ts
mcp_call("myserver", "echo", '{"text":"hi"}')
```

---

## 为什么形态 B 不能「像插件那样」热加载

这是本轮明确要回答的问题，分三层：

1. **插件热加载的本质是「重载解释器里的脚本」**。goja 是进程内的解释器，插件是它加载的
   JS 源码——文件一变，discard 旧 VM 上下文、重新 Compile+Run 即可，进程和二进制都没动。

2. **MCP server 是独立的 OS 进程，且已 mmap 进内存运行**。Linux 上「运行中的可执行文件被覆盖」
   不会让已在跑的进程换代码（inode 已被旧文件占住，新文件是新的 inode）。要让新代码生效，
   唯一途径是**结束旧进程、启动新进程**。所以不存在「在原进程内热替换 Zig 代码」这回事；
   Zig 无嵌入解释器（不像 wasm 有 wazero 这个纯 Go 运行时能在进程内实例化）。

3. **Mizar 已经把它做成了「近似热加载」**：`/mcp reload` 会关闭全部已建立的 stdio 连接
   （`StdioTransport.Close()` 关闭 stdin 并 Kill 子进程），清空 client 缓存，下次 `mcp_call`
   按新配置重新握手，拉起的是磁盘上的新二进制。也就是说：

   > 改 Zig 源码 → `zig build-exe` 覆盖同名文件 → `/mcp reload` → 立即生效，**无需重启 Mizar**。

   代价只是丢掉一次连接（毫秒级），状态（进程内变量）不保留——这与插件热加载保留宿主状态的
   行为不同，是形态 B 固有限制。

另外，形态 A 的 wasm 内核是**编译期 embed 进 Mizar 二进制**的，连子进程都不是，
所以它既不能运行时热加载，也不需要——它随 Mizar 一起发版。
