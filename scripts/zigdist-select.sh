#!/usr/bin/env bash
# zigdist-select.sh —— 控制 internal/zigboot/zigdist/ 里"哪些平台的 Zig 归档参与 go:embed"。
#
# 为什么需要它：
#   go:embed all:zigdist 会把目录下的**所有**归档都打进二进制。若同时放
#   x86_64 和 aarch64 两包，任何架构的二进制都会平白增大 100+ MB。
#   发版时应当"每个二进制只嵌自己那份"：编 amd64 前只留 x86_64 归档，
#   编 arm64 前只留 aarch64 归档。本脚本就是干这件事的。
#
# 用法:
#   scripts/zigdist-select.sh x86_64-linux          # 只保留 x86_64 归档（缓存缺失则自动从 ~/.cache/zig 复制）
#   scripts/zigdist-select.sh aarch64-linux         # 只保留 aarch64 归档
#   scripts/zigdist-select.sh x86_64-linux aarch64-linux   # 保留多个（通用大包）
#   scripts/zigdist-select.sh all                   # 把所有已下载的归档都放进 zigdist/
#   scripts/zigdist-select.sh none                  # 清空（只剩 .gitkeep，二进制不嵌任何 Zig）
#
# 归档来源：~/.cache/zig/zig-<平台>-<版本>.tar.xz（可选 ZIG_CACHE_DIR 覆盖）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EMBED_DIR="$ROOT/internal/zigboot/zigdist"
CACHE_DIR="${ZIG_CACHE_DIR:-$HOME/.cache/zig}"

# 版本号以 zigboot.Version 为准，脚本里兜底 0.17.1
VERSION="$(grep -oP 'const Version = "\K[0-9.]+' "$ROOT/internal/zigboot/zigboot.go" 2>/dev/null || echo 0.17.1)"

if [ $# -eq 0 ]; then
  echo "用法: $0 <平台名... | all | none>" >&2
  echo "  平台名即官方归档名去掉 zig- 前缀与版本后缀，如 x86_64-linux、aarch64-linux" >&2
  echo "  例:  $0 x86_64-linux" >&2
  exit 1
fi

mkdir -p "$EMBED_DIR"
# 保证目录非空，go:embed all:zigdist 在有 .gitkeep 时不会报错
touch "$EMBED_DIR/.gitkeep"

MODE="$1"

if [ "$MODE" = "all" ]; then
  n=0
  for f in "$CACHE_DIR"/zig-*-"$VERSION".tar.xz "$CACHE_DIR"/zig-*-"$VERSION".zip; do
    [ -f "$f" ] || continue
    cp -f "$f" "$EMBED_DIR/"
    n=$((n + 1))
  done
  echo "✓ 已放入全部 $n 个归档到 zigdist/（二进制会显著增大）"
  ls -1 "$EMBED_DIR" | grep -v '^.gitkeep$' | sed 's/^/    /' || true
  exit 0
fi

if [ "$MODE" = "none" ]; then
  found=0
  for f in "$EMBED_DIR"/*.tar.xz "$EMBED_DIR"/*.zip; do
    [ -f "$f" ] || continue
    rm -f "$f"
    found=$((found + 1))
  done
  echo "✓ 已清空 zigdist/（移除 $found 个归档），此二进制将不含 Zig"
  exit 0
fi

# ---- 指定平台：先确保它们都在 zigdist/，再删掉其它 ----
KEEP=()
for plat in "$@"; do
  # 允许用户偷懒写 amd64 / arm64
  case "$plat" in
    amd64|x64)   plat="x86_64-linux" ;;
    arm64|armv8) plat="aarch64-linux" ;;
  esac
  case "$plat" in
    *-windows) EXT=".zip" ;;
    *)         EXT=".tar.xz" ;;
  esac
  name="zig-${plat}-${VERSION}${EXT}"
  KEEP+=("$name")

  if [ ! -f "$EMBED_DIR/$name" ]; then
    if [ -f "$CACHE_DIR/$name" ]; then
      cp -f "$CACHE_DIR/$name" "$EMBED_DIR/$name"
      echo "  + 从缓存复制 $name"
    else
      echo "  ⚠ 缺少 $name（缓存 $CACHE_DIR 里也没有）" >&2
      echo "    先执行: scripts/fetch-zig.sh --arch $plat" >&2
    fi
  fi
done

# 删除所有不在 KEEP 里的归档
for f in "$EMBED_DIR"/*.tar.xz "$EMBED_DIR"/*.zip; do
  [ -f "$f" ] || continue
  base="$(basename "$f")"
  keep=0
  for k in "${KEEP[@]}"; do [ "$base" = "$k" ] && keep=1; done
  if [ "$keep" = "0" ]; then
    rm -f "$f"
    echo "  - 移除 $base"
  fi
done

echo "✓ zigdist/ 现在只含:"
ls -lh "$EMBED_DIR" | grep -v '^total' | grep -v '.gitkeep' | awk '{print "    "$5, $9}'
