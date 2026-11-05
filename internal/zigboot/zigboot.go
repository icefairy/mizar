// Package zigboot 把 Zig 工具链「随二进制内嵌、按需释放到本地缓存」。
//
// 设计要点：
//   - 编译期：scripts/fetch-zig.sh 按目标架构下载官方归档，放进 zigdist/ 后
//     由 go:embed 打进二进制（未下载时目录为空，二进制照常构建，只是 /zig 会提示）。
//   - 运行期：首次执行 /zig init 时把内嵌归档解压到用户缓存目录
//     （Linux 通常为 ~/.cache/mizar/zig/<平台>/），之后直接用其中的 zig 可执行文件。
//   - 离线友好：内网无外网时，只要发版机器提前把归档内嵌好，目标机拷一个二进制即可。
//
// 归档目录（zigdist/）默认被 .gitignore 忽略：它是构建产物，不是源码。
package zigboot

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ulikunitz/xz"
)

//go:embed all:zigdist
var FS embed.FS

// Version 期望的 Zig 版本（与 ziglang.org 下载页一致）。
const Version = "0.17.1"

// target 返回 (平台标识, 归档扩展名, 是否支持)。
// 平台标识即官方归档名去掉 "zig-" 前缀与版本后缀的部分，如 x86_64-linux。
func target() (string, string, error) {
	var plat, ext string
	switch runtime.GOOS {
	case "linux":
		ext = ".tar.xz"
		switch runtime.GOARCH {
		case "amd64":
			plat = "x86_64-linux"
		case "arm64":
			plat = "aarch64-linux"
		case "arm":
			plat = "arm-linux"
		case "386":
			plat = "x86-linux"
		case "riscv64":
			plat = "riscv64-linux"
		case "ppc64le":
			plat = "powerpc64le-linux"
		case "loong64":
			plat = "loongarch64-linux"
		case "s390x":
			plat = "s390x-linux"
		}
	case "darwin":
		ext = ".tar.xz"
		switch runtime.GOARCH {
		case "amd64":
			plat = "x86_64-macos"
		case "arm64":
			plat = "aarch64-macos"
		}
	case "windows":
		ext = ".zip"
		switch runtime.GOARCH {
		case "amd64":
			plat = "x86_64-windows"
		case "arm64":
			plat = "aarch64-windows"
		case "386":
			plat = "x86-windows"
		}
	}
	if plat == "" {
		return "", "", fmt.Errorf("当前平台 %s/%s 无官方 Zig 归档", runtime.GOOS, runtime.GOARCH)
	}
	return plat, ext, nil
}

// ArchiveName 当前平台对应的归档文件名，如 zig-x86_64-linux-0.17.1.tar.xz。
func ArchiveName() (string, error) {
	plat, ext, err := target()
	if err != nil {
		return "", err
	}
	return "zig-" + plat + "-" + Version + ext, nil
}

// CacheDir 返回本地解压缓存根目录（默认 ~/.cache/mizar/zig）。
//
// 环境变量 MIZAR_ZIG_CACHE 可覆盖（便于测试隔离，也方便把工具链放到
// 空间更大的分区）。
func CacheDir() string {
	if v := os.Getenv("MIZAR_ZIG_CACHE"); v != "" {
		return v
	}
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return filepath.Join(".cache", "mizar", "zig")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "mizar", "zig")
}

// embeddedArchive 在 embed 里找当前平台归档；返回 (归档路径, 大小, 是否找到)。
func embeddedArchive() (string, int64, bool) {
	entries, err := fs.ReadDir(FS, "zigdist")
	if err != nil {
		return "", 0, false
	}
	plat, ext, terr := target()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		// 允许归档名带版本号，也允许用户改版本；只按平台+扩展名匹配。
		if strings.HasSuffix(n, ext) && strings.Contains(n, plat) {
			info, ierr := e.Info()
			var size int64
			if ierr == nil {
				size = info.Size()
			}
			_ = terr
			return "zigdist/" + n, size, true
		}
	}
	return "", 0, false
}

// Status 返回人类可读的状态摘要，用于 /zig 无参调用。
func Status() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Zig 工具链版本: %s\n", Version)
	plat, ext, err := target()
	if err != nil {
		fmt.Fprintf(&sb, "当前平台: %s/%s（不支持）\n", runtime.GOOS, runtime.GOARCH)
		return sb.String()
	}
	fmt.Fprintf(&sb, "当前平台: %s（归档扩展名 %s）\n", plat, ext)

	if p, size, ok := embeddedArchive(); ok {
		fmt.Fprintf(&sb, "内嵌归档: ✅ %s（%.1f MB）\n", filepath.Base(p), float64(size)/(1<<20))
	} else {
		fmt.Fprintf(&sb, "内嵌归档: ❌ 未内嵌（此二进制不含 %s 的 Zig）\n", plat)
		sb.WriteString("           发版机器执行: scripts/fetch-zig.sh --embed 后重新构建\n")
	}

	dir := CacheDir()
	zigBin := ZigBinary()
	if st, serr := os.Stat(zigBin); serr == nil && !st.IsDir() {
		fmt.Fprintf(&sb, "本地已释放: ✅ %s（%.1f MB）\n", zigBin, float64(st.Size())/(1<<20))
	} else {
		fmt.Fprintf(&sb, "本地已释放: ❌ 尚未释放到 %s\n", dir)
		sb.WriteString("           执行 /zig init 释放\n")
	}
	fmt.Fprintf(&sb, "缓存目录: %s\n", dir)
	return sb.String()
}

// ZigBinary 返回释放后 zig 可执行文件路径（Linux/macOS 为 .../zig，Windows 为 .../zig.exe）。
func ZigBinary() string {
	name := "zig"
	if runtime.GOOS == "windows" {
		name = "zig.exe"
	}
	plat, _, err := target()
	if err != nil {
		return filepath.Join(CacheDir(), name)
	}
	return filepath.Join(CacheDir(), plat, name)
}

// Release 把内嵌归档解压到缓存目录，返回 zig 可执行文件路径。
// force=true 时先删除旧目录再解压。
//
// 已释放且 zig 可执行文件存在时直接复用（幂等），不重复解压。
func Release(force bool) (string, error) {
	plat, _, err := target()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(CacheDir(), plat)
	zigBin := ZigBinary()

	if !force {
		if st, serr := os.Stat(zigBin); serr == nil && !st.IsDir() {
			return zigBin, nil
		}
	} else {
		_ = os.RemoveAll(dst)
	}

	archive, _, ok := embeddedArchive()
	if !ok {
		return "", fmt.Errorf("此二进制未内嵌 %s 的 Zig 归档；请在发版机器上运行 scripts/fetch-zig.sh --embed 后重新构建", plat)
	}
	// 原子解压：先解到同级的临时目录，成功后再 rename 到最终位置。
	// 避免解压中途失败/中断留下「半个 zig 可执行文件」，被下一次 Release 的
	// 幂等短路误判为已就绪（曾实测踩过：EOF 中断后残留 92MB 残次 zig）。
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("创建缓存目录失败: %w", err)
	}
	tmp := dst + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}
	if err := extractEmbedded(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	// 校验临时目录里确实有可执行文件，再原子替换。
	tmpBin := filepath.Join(tmp, filepath.Base(zigBin))
	if st, serr := os.Stat(tmpBin); serr != nil || st.IsDir() {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("解压完成但未找到 zig 可执行文件: %s", tmpBin)
	}
	_ = os.Chmod(tmpBin, 0o755)
	_ = os.RemoveAll(dst)
	if rerr := os.Rename(tmp, dst); rerr != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("安装 Zig 工具链失败: %w", rerr)
	}
	if st, serr := os.Stat(zigBin); serr != nil || st.IsDir() {
		return "", fmt.Errorf("解压完成但未找到 zig 可执行文件: %s", zigBin)
	}
	_ = os.Chmod(zigBin, 0o755)
	return zigBin, nil
}

// extractEmbedded 解压 embed（.tar.xz 或 .zip）到 dst。
// 归档顶层目录会被剥掉：zig-x86_64-linux-0.17.1/zig → dst/zig。
func extractEmbedded(archive, dst string) error {
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, dst)
	}
	return extractTarXz(archive, dst)
}

func extractTarXz(archive, dst string) error {
	f, err := FS.Open(archive)
	if err != nil {
		return fmt.Errorf("打开内嵌归档失败: %w", err)
	}
	defer f.Close()
	xzr, err := xz.NewReader(f)
	if err != nil {
		return fmt.Errorf("xz 解压初始化失败: %w", err)
	}
	tr := tar.NewReader(xzr)
	for {
		hdr, herr := tr.Next()
		if herr == io.EOF {
			return nil
		}
		if herr != nil {
			return fmt.Errorf("读取 tar 失败: %w", herr)
		}
		rel := stripTopDir(hdr.Name)
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, oerr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o777)
			if oerr != nil {
				return oerr
			}
			if _, cerr := io.Copy(out, tr); cerr != nil {
				out.Close()
				return cerr
			}
			if cerr := out.Close(); cerr != nil {
				return cerr
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if serr := os.Symlink(hdr.Linkname, target); serr != nil {
				// 符号链接失败不致命（zig 主体不依赖），继续。
				continue
			}
		}
	}
}

func extractZip(archive, dst string) error {
	data, err := FS.ReadFile(archive)
	if err != nil {
		return fmt.Errorf("读取内嵌归档失败: %w", err)
	}
	// 用 bytes.Reader 直接包住 []byte：早前写成 strings.NewReader(string(data))
	// 会把整个归档（Windows 包 ~95MB）额外拷一份 string，白吃内存。
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("zip 解析失败: %w", err)
	}
	for _, zf := range zr.File {
		rel := stripTopDir(zf.Name)
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, oerr := zf.Open()
		if oerr != nil {
			return oerr
		}
		out, cerr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, zf.Mode()&0o777)
		if cerr != nil {
			rc.Close()
			return cerr
		}
		if _, cperr := io.Copy(out, rc); cperr != nil {
			out.Close()
			rc.Close()
			return cperr
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// stripTopDir 去掉归档路径的第一段目录。
func stripTopDir(name string) string {
	name = strings.TrimPrefix(name, "./")
	idx := strings.IndexAny(name, "/\\")
	if idx < 0 {
		return ""
	}
	return name[idx+1:]
}

// Clean 删除本地解压缓存。
func Clean() error {
	return os.RemoveAll(CacheDir())
}
