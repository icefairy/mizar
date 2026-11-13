#!/usr/bin/env bash
# build-win7.sh —— 产出可在 Windows 7 SP1 / Server 2008 R2 上运行的 mizar.exe
#
# 为什么需要单独一条构建链：
#   Go 1.21 起官方最低要求 Windows 10 / Server 2016。用官方 SDK 编译的
#   mizar.exe 在 Win7 上会在 runtime 初始化阶段直接崩，栈形如：
#     internal/runtime/syscall/windows/asm_windows_amd64.s:NN +0x.. fp=.. sp=.. pc=..
#   原因至少两处（均在进程 main 之前）：
#     1) runtime 用 ProcessPrng（bcryptprimitives.dll，Win8+）取随机数 → Win7 上
#        GetProcAddress 返回 0，随后 stdcall 到空指针崩溃；
#     2) 长路径支持依赖 RtlGetCurrentPeb + PEB 位域（Win10 15063+）。
#   两者都无法在用户代码里 try/catch，只能换一个打了兼容补丁的 Go 工具链。
#
# 方案：用 XTLS/go-win7 的补丁打一遍 Go 源码（BCryptGenRandom 替代 ProcessPrng、
# 去掉 PEB 长路径劫持、旧控制台句柄回退、LoadLibraryA 回退），自行 make.bash
# 出 SDK，再用该 SDK 编译 mizar。代码本身零改动。
#
# 用法：
#   scripts/build-win7.sh                 # 用缓存的 SDK（没有就先构建）
#   GOWIN7ROOT=/data/gowin7/go scripts/build-win7.sh
#   scripts/build-win7.sh --rebuild-sdk    # 强制重建 SDK
#
# 前置：
#   - 一个能用的官方 Go（做 bootstrap），默认 GOROOT_BOOTSTRAP=/usr/local/go
#   - 网络能取到 go1.26.3.src.tar.gz 与 go-win7 补丁（本仓库已缓存补丁到
#     third_party/go-win7/，见该目录 README）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_VERSION="${GO_VERSION:-1.26.3}"
# 补丁按 "1-26" 这样的主次版本命名（go-1-26.diff / legacy-1-26.diff）
GO_MM="$(echo "$GO_VERSION" | cut -d. -f1-2 | tr . -)"
SDK="${GOWIN7ROOT:-/data/gowin7/go}"
WORK="${GOWIN7WORK:-/data/gowin7}"
PATCH_DIR="$ROOT/third_party/go-win7"
OUT="$ROOT/dist/mizar-win7.exe"

need_sdk=0
if [ "${1:-}" = "--rebuild-sdk" ] || [ ! -x "$SDK/bin/go" ]; then
  need_sdk=1
fi

if [ "$need_sdk" = "1" ]; then
  echo ">>> 构建 Win7 兼容 Go SDK（go${GO_VERSION} + go-win7 补丁）"
  mkdir -p "$WORK"
  if [ ! -d "$WORK/go/src" ]; then
    TARBALL="$WORK/go$GO_VERSION.src.tar.gz"
    if [ ! -f "$TARBALL" ]; then
      # 本地没有就自动下载（内网无网时可预先放好该文件，脚本不会联网）。
      # 优先国内镜像，失败退官方源。
      echo "  ↓ 下载 go$GO_VERSION 源码（约 25MB）"
      for u in \
        "https://mirrors.aliyun.com/golang/go$GO_VERSION.src.tar.gz" \
        "https://go.dev/dl/go$GO_VERSION.src.tar.gz" \
        "https://golang.google.cn/dl/go$GO_VERSION.src.tar.gz"; do
        if command -v curl >/dev/null 2>&1 && curl -fsSL --retry 2 -o "$TARBALL.part" "$u"; then
          mv "$TARBALL.part" "$TARBALL"; break
        elif command -v wget >/dev/null 2>&1 && wget -q -O "$TARBALL.part" "$u"; then
          mv "$TARBALL.part" "$TARBALL"; break
        fi
        echo "    失败: $u"
      done
      [ -f "$TARBALL" ] || {
        echo "!! 无法获取 go$GO_VERSION.src.tar.gz"
        echo "   可手动下载后放到: $TARBALL"
        exit 1
      }
    fi
    tar -C "$WORK" -xzf "$TARBALL"
  fi
  cd "$WORK/go"
  for p in "$PATCH_DIR/go-$GO_MM.diff" "$PATCH_DIR/legacy-$GO_MM.diff"; do
    echo "  patch $(basename "$p")"
    patch -p1 --forward --silent < "$p" || echo "  （已打过，跳过）"
  done
  # bootstrap 用「当前 PATH 里的官方 Go」优先，其次 /usr/local/go。
  # CI（actions/setup-go）里 Go 装在 runner 的 toolcache，不在 /usr/local/go。
  if [ -z "${GOROOT_BOOTSTRAP:-}" ]; then
    if command -v go >/dev/null 2>&1; then
      GOROOT_BOOTSTRAP="$(go env GOROOT)"
    else
      GOROOT_BOOTSTRAP=/usr/local/go
    fi
  fi
  export GOROOT_BOOTSTRAP
  export GOTOOLCHAIN=local
  cd src && ./make.bash
fi

echo ">>> 用 Win7 SDK 编译 Windows/amd64"
export GOROOT="$SDK"
export PATH="$SDK/bin:$PATH"
export GOTOOLCHAIN=local
export GOFLAGS=-mod=mod
export CGO_ENABLED=0
export GOAMD64=v1

cd "$ROOT"
# 内嵌 Zig（x86_64-windows）以保持 /zig 可用；缓存缺失时脚本会自动跳过。
"$ROOT/scripts/zigdist-select.sh" x86_64-windows || true

# -X main.buildTag 让产物自报家门：-version 会显示 "mizar vX.Y.Z (win7-compat)"，
# Web UI 状态栏同样显示。现场一眼能分清装的是官方版还是 Win7 兼容版。
GOOS=windows GOARCH=amd64 "$SDK/bin/go" build -trimpath \
  -ldflags="-s -w -X main.buildTag=win7-compat" -o "$OUT" ./cmd/mizar

"$ROOT/scripts/zigdist-select.sh" none >/dev/null 2>&1 || true

echo ">>> 产物"
ls -lh "$OUT" | awk '{print "   ", $5, $9}'
echo ">>> PE 自检"
"$ROOT/scripts/pe-check-win7.py" "$OUT"
