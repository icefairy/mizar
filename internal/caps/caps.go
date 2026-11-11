// Package caps 探测当前运行环境的「实际能力」：有哪些 shell 可执行文件、
// 常用用户态工具（awk/sed/grep/tar…）是否存在、有没有可写的临时目录，
// 以及是否运行在 Android / Termux / root 环境里。
//
// 为什么需要它：
//   - internal/shell 只按 PATH 找 shell，而 su -c、Magisk、受限容器常常重置
//     PATH，导致 /system/bin/sh 明明存在却 LookPath 失败（见 android 场景）。
//   - 系统提示需要向模型如实声明环境限制（Android 是 mksh+toybox、可能无 awk、
//     无 /tmp），否则模型照写 bashism / GNU 专有选项，补了 shell 也照样失败。
//
// 本包只读文件系统与环境变量，不做任何写操作（TempDir 会做一次真实写入自检）。
package caps

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Env 是当前环境的只读能力快照。
type Env struct {
	GOOS    string            // runtime.GOOS
	Android bool              // 运行在 Android（/system/bin/sh 存在，或 ANDROID_ROOT 有值）
	Termux  bool              // Termux 用户态（$PREFIX 指向 com.termux）
	Root    bool              // 以 root（uid 0）运行
	Tools   map[string]string // 工具名 → 解析出的路径（找不到的工具不会出现在 map 里）
	TempDir string            // 可写的临时目录（空串 = 没有可写目录）
	// ExtraBins 是「存在但可能不在 PATH 里」的 bin 目录（供 shell 兜底搜索）。
	ExtraBins []string
}

// Has 判断某个用户态工具是否可用（PATH 里能找到）。
func (e Env) Has(name string) bool {
	_, ok := e.Tools[name]
	return ok
}

// Missing 返回 names 中不可用的工具名（保持入参顺序，便于稳定输出）。
func (e Env) Missing(names ...string) []string {
	var out []string
	for _, n := range names {
		if !e.Has(n) {
			out = append(out, n)
		}
	}
	return out
}

// probeTools 是启动时一次性探测的用户态工具清单。
// 只放「模型写命令时高频依赖、且各平台差异大」的那批。
var probeTools = []string{
	"sh", "bash", "ash", "zsh",
	"awk", "sed", "grep", "find", "xargs", "which",
	"tar", "gzip", "curl", "wget", "nc", "ps", "kill",
	"git", "python3", "python", "node",
}

// extraBinCandidates 是「系统里常见、但可能不在 PATH」的 bin 目录。
// 顺序即优先级：Android/root 在前（这些环境最容易丢 PATH）。
var extraBinCandidates = []string{
	"/system/bin",                         // Android：mksh、toybox 所在
	"/system/xbin",                        // Android 旧版
	"/vendor/bin",                         // Android vendor 分区
	"/data/adb/magisk",                    // Magisk（含 busybox）
	"/data/adb/ksu/bin",                   // KernelSU（含 busybox）
	"/data/adb/ap/bin",                    // APatch
	"/data/data/com.termux/files/usr/bin", // Termux 用户态
	"/data/local/tmp",                     // adb shell 常用临时目录
	"/usr/local/bin",
	"/opt/homebrew/bin", // macOS ARM
}

var (
	once   sync.Once
	cached Env
)

// Get 返回缓存的探测结果（进程内只探测一次）。
func Get() Env {
	once.Do(func() { cached = detect() })
	return cached
}

// Reset 清空缓存，下次 Get 重新探测（测试用）。
func Reset() {
	once = sync.Once{}
}

func detect() Env {
	e := Env{GOOS: runtime.GOOS, Tools: map[string]string{}}

	// --- 运行环境身份 ---
	prefix := os.Getenv("PREFIX")
	e.Termux = strings.Contains(prefix, "com.termux")
	e.Android = e.Termux ||
		os.Getenv("ANDROID_ROOT") != "" ||
		fileExists("/system/bin/sh")
	e.Root = isRoot()

	// --- 工具探测 ---
	for _, name := range probeTools {
		if p, err := exec.LookPath(name); err == nil {
			e.Tools[name] = p
		}
	}

	// --- PATH 外的 bin 目录 ---
	// Termux 的 $PREFIX/bin 优先，其余按候选顺序。
	seen := map[string]bool{}
	addBin := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			seen[dir] = true
			e.ExtraBins = append(e.ExtraBins, dir)
		}
	}
	if e.Termux {
		addBin(filepath.Join(prefix, "bin"))
	}
	for _, dir := range extraBinCandidates {
		addBin(dir)
	}

	e.TempDir = detectTempDir()
	return e
}

// detectTempDir 找一个真实可写的临时目录。
//
// 顺序：$TMPDIR → os.TempDir() → ~/.cache/mizar/tmp → Android /data/local/tmp。
// 每个候选都做一次真实写入自检——Android 上 /tmp 常常存在但只读，
// 只看存在性会误判，等到 dumpToTemp 落盘失败就晚了。
func detectTempDir() string {
	home, _ := os.UserHomeDir()
	cands := []string{
		os.Getenv("TMPDIR"),
		os.TempDir(),
	}
	if home != "" {
		cands = append(cands, filepath.Join(home, ".cache", "mizar", "tmp"))
	}
	cands = append(cands, "/data/local/tmp")

	for _, dir := range cands {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		f, err := os.CreateTemp(dir, ".mizar-probe-*")
		if err != nil {
			continue
		}
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return dir
	}
	return ""
}

// Notes 返回给模型看的环境限制说明（每条一句，可能为空）。
// 只陈述「会影响命令写法」的事实，不啰嗦。
func (e Env) Notes() []string {
	var notes []string

	if e.Android {
		notes = append(notes, "运行在 Android 上：默认 shell 是 mksh（不是 bash），"+
			"用户态工具由 toybox 提供，只支持 POSIX 语法——不要用 [[ ]]、数组、<<<、set -o pipefail")
		if !e.Termux {
			notes = append(notes, "非 Termux 环境：/app 私有目录与 /tmp 通常不可写，临时文件请落在 $HOME 下")
		}
	}
	if miss := e.Missing("awk"); len(miss) > 0 {
		notes = append(notes, "本机没有 awk：文本加工请用内置 grep/read 工具或 Go/内置脚本，不要依赖 awk")
	}
	if e.GOOS != "windows" && e.TempDir == "" {
		notes = append(notes, "未找到可写的临时目录（/tmp 不可用），大输出落盘可能失败")
	}
	if e.Root && e.Android {
		notes = append(notes, "当前以 root 运行，注意文件属主/权限与 SELinux 限制")
	}
	return notes
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Home 返回用户主目录（取不到返回空串）。
func Home() string {
	h, _ := os.UserHomeDir()
	return h
}
