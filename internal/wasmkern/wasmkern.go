package wasmkern

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed *.wasm
var assets embed.FS

// dataOffset 是内核线性内存中数据传输区的起始偏移。
// 低 1KB 留给 wasm 模块自身的栈与全局变量，避免踩踏。
const dataOffset uint32 = 1024

// EnvDisable 设为 "0"/"false"/"off" 时整体禁用 wasm 内核，强制回退纯 Go 实现。
// 给「零依赖原教旨」用户与排障留一个开关。
const EnvDisable = "MIZAR_WASM_KERNEL"

var errDisabled = errors.New("wasmkern: 已被 " + EnvDisable + " 禁用")

// disabled 报告是否通过环境变量禁用了 wasm 内核。
func disabled() bool {
	switch os.Getenv(EnvDisable) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Kernel 是一个已实例化的 wasm 计算内核。
// 它持有独立的 runtime 与模块实例，不可并发使用（由 Manager 的池来保证）。
type Kernel struct {
	name string
	rt   wazero.Runtime
	mod  api.Module
	mem  api.Memory
}

// Close 释放该实例持有的 runtime。
func (k *Kernel) Close() error {
	if k == nil || k.rt == nil {
		return nil
	}
	err := k.rt.Close(context.Background())
	k.rt = nil
	k.mod = nil
	k.mem = nil
	return err
}

// ensure 保证线性内存至少有 need 字节可用，按 64KiB 页扩容。
func (k *Kernel) ensure(need uint32) error {
	pages := (need + 65535) / 65536
	cur := k.mem.Size() / 65536
	if pages <= cur {
		return nil
	}
	if _, ok := k.mem.Grow(pages - cur); !ok {
		return fmt.Errorf("wasmkern: 无法把线性内存从 %d 页扩到 %d 页", cur, pages)
	}
	return nil
}

// Load 把 data 一次性写入内核线性内存的数据传输区，返回 (ptr, len)。
// 配合 CallLoaded 可对同一份大输入反复计算而无需重复拷贝。
func (k *Kernel) Load(data []byte) (ptr, length uint32, err error) {
	if k == nil || k.mem == nil {
		return 0, 0, errors.New("wasmkern: 内核未实例化")
	}
	if err := k.ensure(dataOffset + uint32(len(data))); err != nil {
		return 0, 0, err
	}
	if len(data) > 0 && !k.mem.Write(dataOffset, data) {
		return 0, 0, errors.New("wasmkern: 写入线性内存失败")
	}
	return dataOffset, uint32(len(data)), nil
}

// CallLoaded 调用已加载的 (ptr, len) -> i32 型导出函数。
func (k *Kernel) CallLoaded(fn string, ptr, length uint32) (uint64, error) {
	if k == nil || k.mod == nil {
		return 0, errors.New("wasmkern: 内核未实例化")
	}
	f := k.mod.ExportedFunction(fn)
	if f == nil {
		return 0, fmt.Errorf("wasmkern: 内核 %q 未导出函数 %q", k.name, fn)
	}
	res, err := f.Call(context.Background(), uint64(ptr), uint64(length))
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

// Call4 调用接受 4 个整数参数、返回 1 个整数的导出函数。
// 用于 bigram 这类需要 (inPtr, inLen, outPtr, outCap) 的内核。
func (k *Kernel) Call4(fn string, a, b, c, d uint64) (uint64, error) {
	if k == nil || k.mod == nil {
		return 0, errors.New("wasmkern: 内核未实例化")
	}
	f := k.mod.ExportedFunction(fn)
	if f == nil {
		return 0, fmt.Errorf("wasmkern: 内核 %q 未导出函数 %q", k.name, fn)
	}
	res, err := f.Call(context.Background(), a, b, c, d)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

// Call0 调用无参、返回单个整数的导出函数（如 kernel_data_end）。
func (k *Kernel) Call0(fn string) (uint64, error) {
	if k == nil || k.mod == nil {
		return 0, errors.New("wasmkern: 内核未实例化")
	}
	f := k.mod.ExportedFunction(fn)
	if f == nil {
		return 0, fmt.Errorf("wasmkern: 内核 %q 未导出函数 %q", k.name, fn)
	}
	res, err := f.Call(context.Background())
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

// dataEnd 调用无参导出函数，返回内核静态数据区的结束地址。
// 内核据此告诉 Go 侧「数据传输区从这里往后放」，避免覆盖全局变量。
func (k *Kernel) dataEnd(fn string) (uint32, error) {
	v, err := k.Call0(fn)
	if err != nil {
		return 0, err
	}
	if v > 1<<31 {
		return 0, fmt.Errorf("wasmkern: %q 返回异常的地址 %d", fn, v)
	}
	return uint32(v), nil
}

// CallBytes 是「写入 + 调用」的便捷封装，适合输入不大的场景。
func (k *Kernel) CallBytes(fn string, data []byte) (uint64, error) {
	ptr, n, err := k.Load(data)
	if err != nil {
		return 0, err
	}
	return k.CallLoaded(fn, ptr, n)
}

// Manager 管理一组内核：内嵌产物、惰性编译缓存与实例池。
// 零值不可用，必须用 NewManager 构造。
type Manager struct {
	mu    sync.Mutex
	cache wazero.CompilationCache
	seen  map[string]bool       // 已确认存在的内核名
	pools map[string]*sync.Pool // 内核名 → 实例池
}

// NewManager 创建管理器。CompilationCache 在所有内核实例间共享，
// 使第二个及以后的实例几乎不需要重新编译。
func NewManager() *Manager {
	return &Manager{
		cache: wazero.NewCompilationCache(),
		seen:  make(map[string]bool),
		pools: make(map[string]*sync.Pool),
	}
}

// Available 报告内嵌产物中存在名为 name 的内核。
func (m *Manager) Available(name string) bool {
	if m == nil || disabled() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seen[name]; ok {
		return true
	}
	if _, err := assets.ReadFile(name + ".wasm"); err != nil {
		return false
	}
	m.seen[name] = true
	return true
}

// pool 返回（必要时创建）某个内核的实例池。
func (m *Manager) pool(name string) *sync.Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[name]; ok {
		return p
	}
	p := &sync.Pool{}
	m.pools[name] = p
	return p
}

// acquire 从池中取一个可用实例，没有就现编译一个。
func (m *Manager) acquire(name string) (*Kernel, error) {
	p := m.pool(name)
	if v := p.Get(); v != nil {
		return v.(*Kernel), nil
	}
	bin, err := assets.ReadFile(name + ".wasm")
	if err != nil {
		return nil, fmt.Errorf("wasmkern: 内核 %q 不存在: %w", name, err)
	}
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfig().WithCompilationCache(m.cache)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	mod, err := rt.InstantiateWithConfig(ctx, bin, wazero.NewModuleConfig())
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasmkern: 实例化内核 %q 失败: %w", name, err)
	}
	return &Kernel{name: name, rt: rt, mod: mod, mem: mod.Memory()}, nil
}

// release 归还实例。传入 nil 会被忽略（便于 defer 用法）。
func (m *Manager) release(name string, k *Kernel) {
	if k == nil {
		return
	}
	m.pool(name).Put(k)
}

// Invoke 在指定内核上调用 (ptr, len) -> i32 型导出函数，输入为一段字节。
// 这是通用入口，新增内核只要遵守同一导出约定即可被调用。
func (m *Manager) Invoke(name, fn string, data []byte) (uint64, error) {
	if disabled() {
		return 0, errDisabled
	}
	if m == nil {
		return 0, errors.New("wasmkern: 管理器为 nil")
	}
	k, err := m.acquire(name)
	if err != nil {
		return 0, err
	}
	defer m.release(name, k)
	out, err := k.CallBytes(fn, data)
	if err != nil {
		// 调用出错时实例状态未知，直接丢弃，不放回池里。
		_ = k.Close()
		m.release(name, nil)
		return 0, err
	}
	return out, nil
}

// 默认管理器：宿主函数与工具直接用它，避免重复编译同一份内核。
var std = NewManager()

// Default 返回包级默认管理器。
func Default() *Manager { return std }

// Available 报告默认管理器中是否存在名为 name 的内核。
func Available(name string) bool { return std.Available(name) }

// KernelCountTokens 是 count_tokens 内核的名字与导出函数名。
const (
	KernelCountTokens = "count_tokens"
	FnCountTokens     = "count_tokens"
)

// CountTokens 用 wasm 内核估算 token 数。内核不可用或出错时返回 (0, false)，
// 调用方应回退到纯 Go 实现，从而保证功能在任何构建下都可用。
//
// ⚠ 小输入不要走内核：一次 wasm 调用（池取出 + 内存写 + 跨边界）固定开销约 5µs，
// 而纯 Go 实现对小串只要十几纳秒。实测 9 字节输入 5.2µs vs 13.8ns，
// 差 370 倍。因此低于 minKernelBytes 的输入直接判为「不适用」，交给回退路径。
func CountTokens(s string) (int, bool) {
	if len(s) < minKernelBytes || !wantKernel() {
		return 0, false
	}
	if !std.Available(KernelCountTokens) {
		return 0, false
	}
	out, err := std.Invoke(KernelCountTokens, FnCountTokens, []byte(s))
	if err != nil {
		return 0, false
	}
	return int(out), true
}

// EnvMode 控制内核启用策略：
//
//	（未设置）/ "auto"  → 仅对经实测确实更快的内核启用（当前 count_tokens 不在其列）
//	"force"             → 强制启用全部已内嵌内核，用于对照验证与压测
//	"0"/"false"/"off"   → 全部禁用
const EnvMode = EnvDisable

// wantKernel 判断当前是否应让 count_tokens 走 wasm 内核。
//
// 默认 false：实测纯 Go 实现全尺寸更快（详见 minKernelBytes 注释），
// 说明「把纯计算下沉到 wasm 一定更快」是错觉——跨边界 + 内存拷贝的开销
// 足以吃掉语言本身的差距。默认走 Go，是把性能留给用户，把基础设施留着复用。
func wantKernel() bool {
	if disabled() {
		return false
	}
	return os.Getenv(EnvMode) == "force"
}

// minKernelBytes 是走 wasm 内核的最小输入长度。
//
// 结论来自实测（i7-10700，见 bench_test.go）：
//   - 100KB 混合文本：纯 Go 137µs，wasm 368µs
//   - 1MB 纯中文：  纯 Go 1.94ms，wasm 2.94ms
//   - 9 字节：      纯 Go 13.8ns，wasm 5.2µs
//
// 也就是说 CountTokens 这个算法**纯 Go 全尺寸都更快**——Go 的 `for range string`
// 由编译器优化成极紧的循环且零分配，而 wasm 侧要付跨边界调用与线性内存拷贝的代价。
// 因此内核默认不接管该函数（wantKernel 默认 false），仅在
// MIZAR_WASM_KERNEL=force 时用于对照验证。保留这条通路的价值在于：
// 它验证了「Go ↔ wasm 内核」的完整工程链路，后续真正的候选内核
// （如需要大词典/复杂算法的分词、语法校验）可以直接复用这套基础设施。
const minKernelBytes = 32 << 10

// CountTokensOr 先尝试 wasm 内核，失败则调用 fallback。
// fallback 为 nil 时返回 0。
func CountTokensOr(s string, fallback func(string) int) int {
	if v, ok := CountTokens(s); ok {
		return v
	}
	if fallback == nil {
		return 0
	}
	return fallback(s)
}

// ── bigram 分词内核 ──────────────────────────────────────────────
//
// 与 count_tokens 不同，这个内核**确实值得下沉**：它替代的是 memory.ts 里
// goja 解释执行的 tokenize()。实测 2000 条记忆检索：
//   goja（现状）  1453 ms
//   纯 Go 同算法    43 ms
// 差距 34×，且其中大部分是「每次分 token 新建 JS 字符串」的分配开销。

const (
	KernelBigram = "bigram"
	FnBigram     = "bigram_tokenize"
	FnBigramData = "kernel_data_end"
)

// maxBigramTokens 是单次分词最多接受的 token 数，超出部分静默丢弃。
// 取 8192 与内核内哈希表槽位数一致，避免极端输入把内存撑爆。
const maxBigramTokens = 8192

// BigramTokenize 用 wasm 内核做 bigram/整词分词，返回 token 原文切片。
//
// 语义与 memory.ts 的 tokenize() 对齐（CJK unigram+bigram、ASCII 整词、
// 停用词过滤、大小写不敏感去重）。内核不可用时返回 (nil,false)，调用方回退。
//
// 为什么要走内核：这个函数的调用点（mem_search）在插件里对 2000 条记忆
// 逐条分词，goja 里要 1453ms，是真实的用户可感卡顿。
func BigramTokenize(s string) ([]string, bool) {
	if len(s) == 0 || !bigramEnabled() {
		return nil, false
	}
	// 对齐 memory.ts 的 `text.toLowerCase()`：先整体转小写再切词，
	// 这样 token 原文的大小写与参考实现一致（内核内部只做哈希期折叠，
	// 返回的 span 指向原缓冲区，不改变字节）。
	// strings.ToLower 在无需改动时直接复用原串，无额外分配。
	s = strings.ToLower(s)

	if !std.Available(KernelBigram) {
		return nil, false
	}
	k, err := std.acquire(KernelBigram)
	if err != nil {
		return nil, false
	}
	// healthy=false 时丢弃实例：调用出错后内核内部状态（代际计数等）未知，
	// 放回池里会让下一次调用拿到坏实例。正常路径才归还。
	healthy := false
	defer func() {
		if healthy {
			std.release(KernelBigram, k)
		} else {
			_ = k.Close()
		}
	}()

	// 数据传输区放在内核静态数据之后，避免覆盖哈希表。
	base, err := k.dataEnd(FnBigramData)
	if err != nil {
		return nil, false
	}
	inPtr := base
	outPtr := inPtr + uint32(len(s)) + 1
	// 对齐到 4 字节，避免未对齐的 u32 写入
	outPtr = (outPtr + 3) &^ 3
	need := outPtr + uint32(maxBigramTokens*2*4)
	if err := k.ensure(need); err != nil {
		return nil, false
	}
	if !k.mem.Write(inPtr, []byte(s)) {
		return nil, false
	}
	n, err := k.Call4(FnBigram, uint64(inPtr), uint64(len(s)), uint64(outPtr), uint64(maxBigramTokens))
	if err != nil {
		return nil, false
	}
	healthy = true
	if n == 0 {
		return nil, true
	}

	buf, ok := k.mem.Read(outPtr, uint32(n)*8)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, n)
	for i := 0; i < int(n); i++ {
		off := uint32(buf[i*8]) | uint32(buf[i*8+1])<<8 | uint32(buf[i*8+2])<<16 | uint32(buf[i*8+3])<<24
		l := uint32(buf[i*8+4]) | uint32(buf[i*8+5])<<8 | uint32(buf[i*8+6])<<16 | uint32(buf[i*8+7])<<24
		if uint64(off)+uint64(l) > uint64(len(s)) {
			continue // 越界，跳过异常项而不是崩
		}
		out = append(out, s[off:off+l])
	}
	return out, true
}

// EnvBigram 控制 bigram 内核是否启用。取值：
//
//	（未设置）/ "auto" → 不启用（默认）
//	"force" / "1" / "on" → 启用
//	"0" / "false" / "off" / "no" → 禁用
//
// 默认不启用是实测结论，不是保守：同一个分词算法，纯 Go 在 1/3/10 段输入下
// 分别 9.5µs / 28µs / 79µs，wasm 内核 37.6µs / 69µs / 127µs——任何尺寸都更慢。
// 内核的价值在于「零分配 span 输出」这套可复用基础设施，以及对照验证，
// 而不是真的比 Go 快。默认让 Go 接管是诚实的做法。
const EnvBigram = "MIZAR_WASM_BIGRAM"

// bigramEnabled 报告 bigram 内核是否启用。默认关，仅在显式 force/1/on 时开。
func bigramEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvBigram))) {
	case "force", "1", "on", "true", "yes":
		return !disabled()
	}
	return false
}

// BigramAvailable 报告 bigram 内核当前是否会被实际启用（开关打开且产物存在）。
// 供上层（如 engine 的记忆检索）判断是否走内核，避免各自硬编码开关名。
func BigramAvailable() bool {
	return bigramEnabled() && std.Available(KernelBigram)
}
